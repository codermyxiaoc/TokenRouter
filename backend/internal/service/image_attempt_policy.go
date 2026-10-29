package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

// 图片生成预算只用于显式挂载策略的入口，不改变文本、视频或管理员指定账号测试。
const ImageAttemptMaxSends = 2
const DefaultPublicImageAttemptTimeout = 15 * time.Minute

type ImageAttemptOutcome string

const (
	ImageAttemptNotSent     ImageAttemptOutcome = "not_sent"
	ImageAttemptRejected    ImageAttemptOutcome = "rejected"
	ImageAttemptUnknown     ImageAttemptOutcome = "unknown"
	ImageAttemptAccepted    ImageAttemptOutcome = "accepted"
	ImageAttemptResultReady ImageAttemptOutcome = "result_ready"
)

// 决策只包含固定类别，不保存上游正文、参考图或凭据。
type ImageAttemptDecision struct {
	Outcome               ImageAttemptOutcome
	RetryDifferentAccount bool
	AttemptFault          bool
	Reason                string
	// UpstreamStatus 是分类依据的 HTTP 状态（传输错误为 0），只用于按故障类型分级冷却。
	UpstreamStatus int
}

type imageAttemptPolicyKey struct{}

type imageAttemptPolicy struct {
	mu     sync.Mutex
	origin context.Context
	cancel context.CancelFunc
	// parent 是安装策略前的上下文，用于区分“策略自身被收尾取消”与“客户端取消”。
	parent     context.Context
	deadline   time.Time
	accounts   map[int64]struct{}
	sends      int
	last       ImageAttemptDecision
	lastSet    bool
	mayProceed bool
	// allCooling 表示本组没有可用账号（含全部处于生图冷却），且一次都没有发送。
	allCooling bool
}

type imageAttemptError struct {
	err      error
	decision ImageAttemptDecision
}

func (e *imageAttemptError) Error() string { return e.err.Error() }
func (e *imageAttemptError) Unwrap() error { return e.err }

// WithImageAttemptPolicy 固定首次执行的总截止时间；后续换号和内部调用不得刷新预算。
func WithImageAttemptPolicy(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ImageAttemptHasPolicy(ctx) {
		return ctx, func() {}
	}
	if timeout <= 0 {
		timeout = DefaultPublicImageAttemptTimeout
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	deadline, _ := ctx.Deadline()
	policy := &imageAttemptPolicy{origin: ctx, cancel: cancel, parent: parent, deadline: deadline, accounts: map[int64]struct{}{}, mayProceed: true}
	return context.WithValue(ctx, imageAttemptPolicyKey{}, policy), cancel
}

// ImageAttemptCancelledByCleanupOnly 判断上下文的取消是否仅来自生图策略的收尾取消（处理器返回时的 cancel），
// 而客户端及外层上下文仍然有效。生图总截止到期不算：超时后必须停止，不能借换组继续执行。
func ImageAttemptCancelledByCleanupOnly(ctx context.Context) bool {
	policy := imagePolicyFromContext(ctx)
	if policy == nil || ctx.Err() == nil || policy.parent == nil {
		return false
	}
	return errors.Is(policy.origin.Err(), context.Canceled) && policy.parent.Err() == nil
}

func imagePolicyFromContext(ctx context.Context) *imageAttemptPolicy {
	if ctx == nil {
		return nil
	}
	policy, _ := ctx.Value(imageAttemptPolicyKey{}).(*imageAttemptPolicy)
	return policy
}

func ImageAttemptHasPolicy(ctx context.Context) bool { return imagePolicyFromContext(ctx) != nil }

// CancelImageAttemptPolicy 供外层请求收尾释放后来在模型分派阶段建立的预算计时器。
func CancelImageAttemptPolicy(ctx context.Context) {
	if policy := imagePolicyFromContext(ctx); policy != nil && policy.cancel != nil {
		policy.cancel()
	}
}

// DetachImageAttemptContext 允许已发送请求在客户端离开后取回产物，但保留首次执行截止。
func DetachImageAttemptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if policy := imagePolicyFromContext(ctx); policy != nil {
		return context.WithDeadline(context.WithoutCancel(ctx), policy.deadline)
	}
	return detachUpstreamContext(ctx)
}

// BeginImageAttempt 必须紧邻真正生成 POST 的 HTTP 调用。每账号最多发送一次，总计最多两次。
func BeginImageAttempt(ctx context.Context, accountID int64, finalModel, operation string) error {
	policy := imagePolicyFromContext(ctx)
	if policy == nil {
		return nil
	}
	policy.mu.Lock()
	defer policy.mu.Unlock()
	if err := policy.origin.Err(); err != nil {
		return WrapImageAttemptError(err, ImageAttemptDecision{Outcome: ImageAttemptNotSent, Reason: "image_execution_deadline"})
	}
	_, repeated := policy.accounts[accountID]
	if accountID <= 0 || strings.TrimSpace(finalModel) == "" || strings.TrimSpace(operation) == "" || repeated || policy.sends >= ImageAttemptMaxSends || !policy.mayProceed {
		return WrapImageAttemptError(errors.New("image generation attempt budget exhausted; request will not be resubmitted"), ImageAttemptDecision{Outcome: ImageAttemptNotSent, Reason: "image_attempt_budget_exhausted"})
	}
	policy.accounts[accountID] = struct{}{}
	policy.sends++
	policy.last = ImageAttemptDecision{Outcome: ImageAttemptUnknown, Reason: "image_submission_unknown"}
	policy.lastSet, policy.mayProceed = true, false
	return nil
}

func ImageAttemptSendCount(ctx context.Context) int {
	policy := imagePolicyFromContext(ctx)
	if policy == nil {
		return 0
	}
	policy.mu.Lock()
	defer policy.mu.Unlock()
	return policy.sends
}

func ImageAttemptLastDecision(ctx context.Context) (ImageAttemptDecision, bool) {
	policy := imagePolicyFromContext(ctx)
	if policy == nil {
		return ImageAttemptDecision{}, false
	}
	policy.mu.Lock()
	defer policy.mu.Unlock()
	return policy.last, policy.lastSet
}

func ImageAttemptCanSwitch(decision ImageAttemptDecision) bool {
	return decision.RetryDifferentAccount && (decision.Outcome == ImageAttemptNotSent || decision.Outcome == ImageAttemptRejected)
}

// MarkImageAttemptGroupUnsent 由入口在本组没有可用账号（含全部处于生图冷却）且一次都未发送时调用，
// 作为“未发送”的正向证据，允许智能路由换到下一个分组。
func MarkImageAttemptGroupUnsent(ctx context.Context) {
	if policy := imagePolicyFromContext(ctx); policy != nil {
		policy.mu.Lock()
		defer policy.mu.Unlock()
		if !policy.lastSet && policy.sends == 0 {
			policy.allCooling = true
		}
	}
}

// ImageAttemptAllowsGroupFallback 判断智能路由能否把生图请求换到下一个分组：
// 只接受“全组冷却未发送”或“最后一次为确定未生成且可换号的拒绝”两种正向证据；
// 结果未知、已受理或缺少证据时一律禁止，避免重复生成和重复扣费。
// @project-doc docs/architecture/account_scheduling_and_cache.md#image_model_cooldown
func ImageAttemptAllowsGroupFallback(ctx context.Context) bool {
	policy := imagePolicyFromContext(ctx)
	if policy == nil {
		return true
	}
	policy.mu.Lock()
	defer policy.mu.Unlock()
	if !policy.lastSet {
		return policy.allCooling && policy.sends == 0
	}
	return ImageAttemptCanSwitch(policy.last)
}

func RecordImageAttemptDecision(ctx context.Context, decision ImageAttemptDecision) {
	if policy := imagePolicyFromContext(ctx); policy != nil {
		policy.mu.Lock()
		defer policy.mu.Unlock()
		// 已受理/产物证据不能被后续错误包装降级成可重试拒绝。
		if policy.lastSet && (policy.last.Outcome == ImageAttemptAccepted || policy.last.Outcome == ImageAttemptResultReady) && decision.Outcome != ImageAttemptResultReady {
			return
		}
		policy.last, policy.lastSet = decision, true
		policy.mayProceed = ImageAttemptCanSwitch(decision)
		if decision.Outcome == ImageAttemptUnknown || decision.Outcome == ImageAttemptAccepted || decision.Outcome == ImageAttemptResultReady {
			MarkSmartRoutingAttemptNonReplayable(ctx, decision.Reason)
		} else if policy.sends >= ImageAttemptMaxSends {
			MarkSmartRoutingAttemptNonReplayable(ctx, "image_attempt_budget_exhausted")
		}
	}
}

func WrapImageAttemptError(err error, decision ImageAttemptDecision) error {
	if err == nil {
		err = fmt.Errorf("image generation failed: %s", decision.Reason)
	}
	return &imageAttemptError{err: err, decision: decision}
}

func RecordImageAttemptFailure(ctx context.Context, err error, decision ImageAttemptDecision) error {
	RecordImageAttemptDecision(ctx, decision)
	if stored, ok := ImageAttemptLastDecision(ctx); ok && (stored.Outcome == ImageAttemptAccepted || stored.Outcome == ImageAttemptResultReady) {
		decision = stored
	}
	return WrapImageAttemptError(err, decision)
}

func ImageAttemptDecisionForError(err error) (ImageAttemptDecision, bool) {
	var typed *imageAttemptError
	if errors.As(err, &typed) {
		return typed.decision, true
	}
	return ImageAttemptDecision{}, false
}

// 未获得连接的 DNS/dial 错误可证明未发送；写入、读取、超时等其它传输错误均保守视为受理未知。
func ClassifyImageTransportFailure(err error) ImageAttemptDecision {
	decision := ImageAttemptDecision{Outcome: ImageAttemptUnknown, AttemptFault: true, Reason: "image_transport_unknown"}
	if errors.Is(err, context.Canceled) {
		decision.AttemptFault = false
		return decision
	}
	var dns *net.DNSError
	var op *net.OpError
	if errors.As(err, &dns) || errors.As(err, &op) && strings.EqualFold(op.Op, "dial") {
		return ImageAttemptDecision{Outcome: ImageAttemptNotSent, RetryDifferentAccount: true, AttemptFault: true, Reason: "image_connection_not_sent"}
	}
	return decision
}

// ClassifyImageHTTPFailure 不把普通 5xx 当作未受理保证；任务、产物或用量证据优先于 HTTP 错误码。
func ClassifyImageHTTPFailure(status int, body []byte) ImageAttemptDecision {
	decision := classifyImageHTTPFailure(status, body)
	decision.UpstreamStatus = status
	return decision
}

func classifyImageHTTPFailure(status int, body []byte) ImageAttemptDecision {
	unknown := ImageAttemptDecision{Outcome: ImageAttemptUnknown, AttemptFault: true, Reason: "image_submission_unknown"}
	if gjson.ValidBytes(body) {
		hasTextOutput := false
		for _, path := range []string{"candidates", "response.candidates"} {
			for _, candidate := range gjson.GetBytes(body, path).Array() {
				for _, part := range candidate.Get("content.parts").Array() {
					if part.Get("text").String() != "" {
						hasTextOutput = true
					}
					if part.Get("inlineData.data").String() != "" || part.Get("inline_data.data").String() != "" || part.Get("fileData.fileUri").String() != "" || part.Get("file_data.file_uri").String() != "" {
						return ImageAttemptDecision{Outcome: ImageAttemptResultReady, Reason: "image_output_observed"}
					}
				}
			}
		}
		for _, path := range []string{"output", "response.output"} {
			for _, item := range gjson.GetBytes(body, path).Array() {
				if item.Get("encrypted_content").String() != "" || item.Get("arguments").String() != "" {
					hasTextOutput = true
				}
				if item.Get("type").String() == "image_generation_call" && item.Get("result").String() != "" {
					return ImageAttemptDecision{Outcome: ImageAttemptResultReady, Reason: "image_output_observed"}
				}
				for _, part := range item.Get("content").Array() {
					if part.Get("text").String() != "" {
						hasTextOutput = true
					}
				}
			}
		}
		if hasTextOutput {
			return ImageAttemptDecision{Outcome: ImageAttemptAccepted, Reason: "image_non_image_output"}
		}
		for _, path := range []string{"usage", "response.usage", "data.usage", "data.response.usage", "usageMetadata", "usage_metadata", "response.usageMetadata", "response.usage_metadata"} {
			if gjson.GetBytes(body, path).IsObject() {
				return ImageAttemptDecision{Outcome: ImageAttemptAccepted, Reason: "image_usage_observed"}
			}
		}
		// Responses 和部分兼容图片服务以 id 标识已创建任务，错误状态不能抹掉受理证据。
		for _, path := range []string{"task_id", "data.task_id", "id", "response.id", "data.id"} {
			if strings.TrimSpace(gjson.GetBytes(body, path).String()) != "" {
				return ImageAttemptDecision{Outcome: ImageAttemptAccepted, Reason: "image_task_accepted"}
			}
		}
		for _, item := range gjson.GetBytes(body, "data").Array() {
			if item.Get("url").String() != "" || item.Get("b64_json").String() != "" {
				return ImageAttemptDecision{Outcome: ImageAttemptResultReady, Reason: "image_output_observed"}
			}
		}
	}
	// 部分兼容网关把所有HTTP错误都包装为invalid_request_error；5xx/408不能据此外推未受理。
	if status >= 500 || status == http.StatusRequestTimeout {
		return unknown
	}
	code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.code").String()))
	if code == "" {
		code = strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.type").String()))
	}
	if code == "" {
		code = strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "code").String()))
	}
	for _, fragment := range []string{"content_policy", "safety", "moderation", "invalid_image", "invalid_parameter"} {
		if strings.Contains(code, fragment) {
			return ImageAttemptDecision{Outcome: ImageAttemptRejected, Reason: "image_request_rejected"}
		}
	}
	// Gemini 的安全拒绝也可能通过 HTTP 200 返回，不把它当成账号故障。
	if gjson.GetBytes(body, "promptFeedback.blockReason").String() != "" || gjson.GetBytes(body, "prompt_feedback.block_reason").String() != "" {
		return ImageAttemptDecision{Outcome: ImageAttemptRejected, Reason: "image_safety_rejected"}
	}
	for _, candidate := range gjson.GetBytes(body, "candidates").Array() {
		reason := strings.ToUpper(candidate.Get("finishReason").String())
		if reason == "" {
			reason = strings.ToUpper(candidate.Get("finish_reason").String())
		}
		switch reason {
		case "SAFETY", "IMAGE_SAFETY", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "RECITATION":
			return ImageAttemptDecision{Outcome: ImageAttemptRejected, Reason: "image_safety_rejected"}
		}
	}
	// 模型缺失/余额的确定证据优先于兼容网关宽泛的 invalid_request_error 外壳。
	if isOpenAIImagesInsufficientBalance(body) || isUpstreamModelNotFoundError(status, body) {
		return ImageAttemptDecision{Outcome: ImageAttemptRejected, RetryDifferentAccount: true, AttemptFault: true, Reason: "image_account_model_unavailable"}
	}
	for _, kind := range []string{code, strings.ToLower(gjson.GetBytes(body, "error.type").String()), strings.ToLower(gjson.GetBytes(body, "error.status").String())} {
		if (status == http.StatusBadRequest || status == http.StatusUnprocessableEntity) && (strings.Contains(kind, "invalid_request") || strings.Contains(kind, "invalid_argument")) {
			return ImageAttemptDecision{Outcome: ImageAttemptRejected, Reason: "image_request_rejected"}
		}
	}
	if status >= 500 || status == 408 || status < 400 {
		return unknown
	}
	switch status {
	case 401, 403, 404, 405, 429:
		return ImageAttemptDecision{Outcome: ImageAttemptRejected, RetryDifferentAccount: true, AttemptFault: true, Reason: "image_upstream_rejected"}
	default:
		return ImageAttemptDecision{Outcome: ImageAttemptRejected, Reason: "image_request_rejected"}
	}
}

// ImageAttemptAccountModel 返回冷却和发送共同使用的最终账号模型，不在缓存层再次映射。
func ImageAttemptAccountModel(ctx context.Context, account *Account, routingModel string) string {
	return resolveAccountUpstreamModel(ctx, account, routingModel)
}

func ImageAttemptOperation(parsed *OpenAIImagesRequest) string {
	if parsed != nil && parsed.HasMask {
		return "inpaint"
	}
	if parsed != nil && parsed.IsEdits() {
		return "edit"
	}
	return "generate"
}
