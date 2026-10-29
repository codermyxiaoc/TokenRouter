package service

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

const DefaultImageModelCooldown = 10 * time.Second

// ImageModelCooldownCache 按实际账号、上游模型和图片操作共享故障窗口，不封禁整个分组。
// 这里的模型必须已完成映射，读写时不能再次套用账号映射。
type ImageModelCooldownCache interface {
	GetImageModelCooldown(context.Context, int64, string, string) (time.Duration, error)
	CooldownImageModel(context.Context, int64, string, string, time.Duration) error
}

// NormalizeImageModelOperation 只接收图片操作，音频和普通文本不会借用生图健康状态。
func NormalizeImageModelOperation(operation string) string {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case CreativeOperationGenerate:
		return CreativeOperationGenerate
	case CreativeOperationEdit:
		return CreativeOperationEdit
	case CreativeOperationInpaint:
		return CreativeOperationInpaint
	default:
		return ""
	}
}

// ImageModelCooldownRemaining 使用缓存的有界本地降级结果；未知状态不能伪装成账号故障。
func ImageModelCooldownRemaining(ctx context.Context, cache any, accountID int64, finalModel, operation string) time.Duration {
	store, ok := cache.(ImageModelCooldownCache)
	operation = NormalizeImageModelOperation(operation)
	if !ok || store == nil || accountID <= 0 || strings.TrimSpace(finalModel) == "" || operation == "" {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	budget, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	remaining, _ := store.GetImageModelCooldown(budget, accountID, strings.TrimSpace(finalModel), operation)
	if remaining > 0 {
		return remaining
	}
	return 0
}

// RecordImageModelCooldown 仅由已分类的上游故障调用；客户端取消和本地存储失败不应进入这里。
// 实际故障记录不依赖客户端仍保持连接，写入拥有独立且有限的预算。
func RecordImageModelCooldown(ctx context.Context, cache any, accountID int64, finalModel, operation string, duration time.Duration) error {
	store, ok := cache.(ImageModelCooldownCache)
	operation = NormalizeImageModelOperation(operation)
	if !ok || store == nil || accountID <= 0 || strings.TrimSpace(finalModel) == "" || operation == "" {
		return nil
	}
	if duration <= 0 {
		duration = DefaultImageModelCooldown
	}
	if ctx == nil {
		ctx = context.Background()
	}
	budget, cancel := context.WithTimeout(context.WithoutCancel(ctx), 250*time.Millisecond)
	defer cancel()
	return store.CooldownImageModel(budget, accountID, strings.TrimSpace(finalModel), operation, min(duration, time.Hour))
}

func (s *OpenAIGatewayService) ImageModelCooldownRemaining(ctx context.Context, accountID int64, finalModel, operation string) time.Duration {
	if s == nil {
		return 0
	}
	return ImageModelCooldownRemaining(ctx, s.cache, accountID, finalModel, operation)
}

func (s *OpenAIGatewayService) RecordImageModelCooldown(ctx context.Context, accountID int64, finalModel, operation string, duration time.Duration) error {
	if s == nil {
		return nil
	}
	return RecordImageModelCooldown(ctx, s.cache, accountID, finalModel, operation, duration)
}

func (s *GatewayService) ImageModelCooldownRemaining(ctx context.Context, accountID int64, finalModel, operation string) time.Duration {
	if s == nil {
		return 0
	}
	return ImageModelCooldownRemaining(ctx, s.cache, accountID, finalModel, operation)
}

func (s *GatewayService) RecordImageModelCooldown(ctx context.Context, accountID int64, finalModel, operation string, duration time.Duration) error {
	if s == nil {
		return nil
	}
	return RecordImageModelCooldown(ctx, s.cache, accountID, finalModel, operation, duration)
}

// ImageModelFailureTracker 记录同一“账号 + 实际模型 + 操作”的连续失败次数，用于逐级延长冷却。
// 成功一次即清零；未实现时退回固定冷却。
type ImageModelFailureTracker interface {
	IncrImageModelFailures(ctx context.Context, accountID int64, model, operation string, window time.Duration) (int, error)
	ResetImageModelFailures(ctx context.Context, accountID int64, model, operation string) error
}

// ImageModelCooldownAdmin 供管理端查看与手动解除冷却。
type ImageModelCooldownAdmin interface {
	ListImageModelCooldowns(ctx context.Context, accountID int64) ([]ImageModelCooldownEntry, error)
	ClearImageModelCooldown(ctx context.Context, accountID int64, model, operation string) error
}

// ImageModelCooldownEntry 是一条正在生效的生图冷却。
type ImageModelCooldownEntry struct {
	AccountID        int64  `json:"account_id"`
	Model            string `json:"model"`
	Operation        string `json:"operation"`
	RemainingSeconds int64  `json:"remaining_seconds"`
	Failures         int    `json:"failures"`
}

// ImageModelCooldownPolicy 决定各类生图故障的冷却时长。
// @project-doc docs/architecture/account_scheduling_and_cache.md#image_model_cooldown
type ImageModelCooldownPolicy struct {
	// Unavailable 用于模型不存在、鉴权失败、余额不足等需要人工处理的故障。
	Unavailable time.Duration
	// RateLimited 用于 429；上游已明确限流，等待一段时间即可恢复。
	RateLimited time.Duration
	// Rejected 用于其它账号级拒绝（401/403/404/405），按连续次数逐级延长。
	Rejected []time.Duration
	// Unknown 用于 5xx、超时与传输错误，按连续次数逐级延长。
	Unknown []time.Duration
	// StrikeWindow 是连续失败计数的保留时长，超过后重新从第一档开始。
	StrikeWindow time.Duration
}

// DefaultImageModelCooldownPolicy 是默认分级：模型不可用 1 小时、429 一分钟、未知故障 2→10→30 分钟。
func DefaultImageModelCooldownPolicy() ImageModelCooldownPolicy {
	return ImageModelCooldownPolicy{
		Unavailable:  time.Hour,
		RateLimited:  time.Minute,
		Rejected:     []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute},
		Unknown:      []time.Duration{2 * time.Minute, 10 * time.Minute, 30 * time.Minute},
		StrikeWindow: 2 * time.Hour,
	}
}

var (
	imageModelCooldownPolicyMu sync.RWMutex
	imageModelCooldownPolicy   = DefaultImageModelCooldownPolicy()
)

// SetImageModelCooldownPolicy 在启动时按配置覆盖默认分级；非正数项保留默认值。
func SetImageModelCooldownPolicy(policy ImageModelCooldownPolicy) {
	defaults := DefaultImageModelCooldownPolicy()
	if policy.Unavailable <= 0 {
		policy.Unavailable = defaults.Unavailable
	}
	if policy.RateLimited <= 0 {
		policy.RateLimited = defaults.RateLimited
	}
	if len(policy.Rejected) == 0 {
		policy.Rejected = defaults.Rejected
	}
	if len(policy.Unknown) == 0 {
		policy.Unknown = defaults.Unknown
	}
	if policy.StrikeWindow <= 0 {
		policy.StrikeWindow = defaults.StrikeWindow
	}
	imageModelCooldownPolicyMu.Lock()
	imageModelCooldownPolicy = policy
	imageModelCooldownPolicyMu.Unlock()
}

func currentImageModelCooldownPolicy() ImageModelCooldownPolicy {
	imageModelCooldownPolicyMu.RLock()
	defer imageModelCooldownPolicyMu.RUnlock()
	return imageModelCooldownPolicy
}

// ImageModelCooldownPolicyFromConfig 把秒级配置转换为策略；未填写项交给 SetImageModelCooldownPolicy 补默认值。
func ImageModelCooldownPolicyFromConfig(cfg config.ImageModelCooldownConfig) ImageModelCooldownPolicy {
	steps := func(values []int) []time.Duration {
		var result []time.Duration
		for _, value := range values {
			if value > 0 {
				result = append(result, time.Duration(value)*time.Second)
			}
		}
		return result
	}
	return ImageModelCooldownPolicy{
		Unavailable:  time.Duration(cfg.UnavailableSeconds) * time.Second,
		RateLimited:  time.Duration(cfg.RateLimitedSeconds) * time.Second,
		Rejected:     steps(cfg.RejectedSeconds),
		Unknown:      steps(cfg.UnknownSeconds),
		StrikeWindow: time.Duration(cfg.StrikeWindowSeconds) * time.Second,
	}
}

// cooldownFor 按故障类型与第几次连续失败（从 1 开始）返回冷却时长，最长一小时。
func (p ImageModelCooldownPolicy) cooldownFor(decision ImageAttemptDecision, strike int) time.Duration {
	ladder := func(steps []time.Duration) time.Duration {
		if len(steps) == 0 {
			return DefaultImageModelCooldown
		}
		index := min(max(strike, 1), len(steps)) - 1
		return steps[index]
	}
	var duration time.Duration
	switch {
	case decision.Reason == "image_account_model_unavailable":
		duration = p.Unavailable
	case decision.UpstreamStatus == http.StatusTooManyRequests:
		duration = p.RateLimited
	case decision.UpstreamStatus == http.StatusUnauthorized || decision.UpstreamStatus == http.StatusForbidden:
		// 凭据失效需要人工处理，直接按不可用对待。
		duration = p.Unavailable
	case decision.Outcome == ImageAttemptRejected:
		duration = ladder(p.Rejected)
	default:
		duration = ladder(p.Unknown)
	}
	// 本包另有同名 max(int,int)，这里显式比较时长上下限，不依赖内置函数。
	if duration < time.Second {
		duration = time.Second
	}
	if duration > time.Hour {
		duration = time.Hour
	}
	return duration
}

// RecordImageModelFailure 按故障类型与连续失败次数写入冷却；计数不可用时退回第一档。
// 只由已分类的账号故障调用（decision.AttemptFault 为真）。
func RecordImageModelFailure(ctx context.Context, cache any, accountID int64, finalModel, operation string, decision ImageAttemptDecision) error {
	operation = NormalizeImageModelOperation(operation)
	if accountID <= 0 || strings.TrimSpace(finalModel) == "" || operation == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	policy := currentImageModelCooldownPolicy()
	// 窗口仍在生效时缓存层不会续期，也不再累加计数，避免并发失败把一次故障计成多次。
	if ImageModelCooldownRemaining(context.WithoutCancel(ctx), cache, accountID, finalModel, operation) > 0 {
		return nil
	}
	strike := 1
	if tracker, ok := cache.(ImageModelFailureTracker); ok && tracker != nil {
		budget, cancel := context.WithTimeout(context.WithoutCancel(ctx), 250*time.Millisecond)
		if count, err := tracker.IncrImageModelFailures(budget, accountID, strings.TrimSpace(finalModel), operation, policy.StrikeWindow); err == nil && count > 0 {
			strike = count
		}
		cancel()
	}
	return RecordImageModelCooldown(ctx, cache, accountID, finalModel, operation, policy.cooldownFor(decision, strike))
}

// RecordImageModelSuccess 在确认产出图片后清零连续失败计数，冷却窗口按原期限自然结束。
func RecordImageModelSuccess(ctx context.Context, cache any, accountID int64, finalModel, operation string) {
	operation = NormalizeImageModelOperation(operation)
	tracker, ok := cache.(ImageModelFailureTracker)
	if !ok || tracker == nil || accountID <= 0 || strings.TrimSpace(finalModel) == "" || operation == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	budget, cancel := context.WithTimeout(context.WithoutCancel(ctx), 250*time.Millisecond)
	defer cancel()
	_ = tracker.ResetImageModelFailures(budget, accountID, strings.TrimSpace(finalModel), operation)
}

func (s *OpenAIGatewayService) RecordImageModelFailure(ctx context.Context, accountID int64, finalModel, operation string, decision ImageAttemptDecision) error {
	if s == nil {
		return nil
	}
	return RecordImageModelFailure(ctx, s.cache, accountID, finalModel, operation, decision)
}

func (s *OpenAIGatewayService) RecordImageModelSuccess(ctx context.Context, accountID int64, finalModel, operation string) {
	if s != nil {
		RecordImageModelSuccess(ctx, s.cache, accountID, finalModel, operation)
	}
}

func (s *GatewayService) RecordImageModelFailure(ctx context.Context, accountID int64, finalModel, operation string, decision ImageAttemptDecision) error {
	if s == nil {
		return nil
	}
	return RecordImageModelFailure(ctx, s.cache, accountID, finalModel, operation, decision)
}

func (s *GatewayService) RecordImageModelSuccess(ctx context.Context, accountID int64, finalModel, operation string) {
	if s != nil {
		RecordImageModelSuccess(ctx, s.cache, accountID, finalModel, operation)
	}
}

// ErrImageModelCooldownAdminUnavailable 表示当前缓存后端不支持列出或解除生图冷却。
var ErrImageModelCooldownAdminUnavailable = infraerrors.ServiceUnavailable("IMAGE_MODEL_COOLDOWN_UNAVAILABLE", "image model cooldown store is unavailable")

// ListImageModelCooldowns 列出账号下正在生效的生图冷却，供管理端排障。
func (s *OpenAIGatewayService) ListImageModelCooldowns(ctx context.Context, accountID int64) ([]ImageModelCooldownEntry, error) {
	admin, ok := imageModelCooldownAdminFrom(s)
	if !ok {
		return nil, ErrImageModelCooldownAdminUnavailable
	}
	entries, err := admin.ListImageModelCooldowns(ctx, accountID)
	if entries == nil {
		entries = []ImageModelCooldownEntry{}
	}
	return entries, err
}

// ClearImageModelCooldown 由管理员手动解除某个“账号 + 模型 + 操作”的冷却并清零失败计数。
func (s *OpenAIGatewayService) ClearImageModelCooldown(ctx context.Context, accountID int64, model, operation string) error {
	admin, ok := imageModelCooldownAdminFrom(s)
	if !ok {
		return ErrImageModelCooldownAdminUnavailable
	}
	if accountID <= 0 || strings.TrimSpace(model) == "" || NormalizeImageModelOperation(operation) == "" {
		return infraerrors.BadRequest("IMAGE_MODEL_COOLDOWN_INVALID", "account, model and image operation are required")
	}
	return admin.ClearImageModelCooldown(ctx, accountID, strings.TrimSpace(model), NormalizeImageModelOperation(operation))
}

func imageModelCooldownAdminFrom(s *OpenAIGatewayService) (ImageModelCooldownAdmin, bool) {
	if s == nil || s.cache == nil {
		return nil, false
	}
	admin, ok := any(s.cache).(ImageModelCooldownAdmin)
	return admin, ok && admin != nil
}

// WithImageModelOperation 把本次图片操作写入请求上下文，调度器据此跳过冷却中的“账号 + 模型 + 操作”。
func WithImageModelOperation(ctx context.Context, operation string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	operation = NormalizeImageModelOperation(operation)
	if operation == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxkey.ImageModelOperation, operation)
}

// ImageModelOperationFromContext 读取调度阶段使用的图片操作；未标记时返回空串。
func ImageModelOperationFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	operation, _ := ctx.Value(ctxkey.ImageModelOperation).(string)
	return operation
}

// ImageModelScheduleCooling 是调度过滤专用的快速查询，可带短暂的“无冷却”本地缓存。
type ImageModelScheduleCooling interface {
	ImageModelCoolingForSchedule(ctx context.Context, accountID int64, model, operation string) bool
}

// imageCooldownAccountBlocked 判断账号在调度阶段是否应因生图冷却被跳过：仅在显式标记了图片操作的请求中生效，
// 按账号映射后的实际模型和本次图片操作查询，不影响同账号的对话模型。
func imageCooldownAccountBlocked(ctx context.Context, cache any, account *Account, routingModel string) bool {
	operation := ImageModelOperationFromContext(ctx)
	if account == nil || cache == nil || operation == "" {
		return false
	}
	model := resolveAccountUpstreamModel(ctx, account, routingModel)
	if fast, ok := cache.(ImageModelScheduleCooling); ok && fast != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		budget, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		return fast.ImageModelCoolingForSchedule(budget, account.ID, model, operation)
	}
	return ImageModelCooldownRemaining(ctx, cache, account.ID, model, operation) > 0
}
