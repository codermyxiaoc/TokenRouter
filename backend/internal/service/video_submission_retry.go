package service

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// VideoTaskRetryRepository 在原任务和原预算上迁移明确拒绝的尝试，不允许重放受理未知的生成。
type VideoTaskRetryRepository interface {
	SwitchRejected(context.Context, string, string, VideoUpstreamTarget, VideoRequestMetadata, float64, int) (*VideoTaskRecord, error)
}

// asyncVideoTaskID 返回严格的字符串任务 ID；异常或冲突字段仍阻止重试，避免误判未受理。
func asyncVideoTaskID(body []byte) (id string, present bool) {
	if !gjson.ValidBytes(body) {
		return "", false
	}
	for _, path := range []string{"id", "request_id", "task_id", "data.id", "data.request_id", "data.task_id", "video.id", "video.request_id", "video.task_id", "output.task_id", "task.id", "task.task_id"} {
		v := gjson.GetBytes(body, path)
		if !v.Exists() || v.Type == gjson.Null || (v.Type == gjson.String && strings.TrimSpace(v.String()) == "") {
			continue
		}
		present = true
		candidate := strings.TrimSpace(v.String())
		if v.Type != gjson.String || validateUpstreamPathSegment("video task ID", candidate) != nil || (id != "" && candidate != id) {
			return "", true
		}
		id = candidate
	}
	return id, present
}

// VideoCreationDefinitelyRejected 仅放行明确未受理的上游错误；退款规则不能作为重提证据。
func VideoCreationDefinitelyRejected(status int, body []byte) bool {
	if status < 400 || status > 599 {
		return false
	}
	// request_id 在万相等协议中只是追踪号，Grok 调用方另行按自身协议检查它。
	for _, path := range []string{"TaskId", "id", "task_id", "data.id", "data.task_id", "video.id", "video.task_id", "output.task_id", "task.id", "task.task_id"} {
		v := gjson.GetBytes(body, path)
		if v.Exists() && v.Type != gjson.Null && (v.Type != gjson.String || strings.TrimSpace(v.String()) != "") {
			return false
		}
	}
	if videoCreationHasAcceptanceEvidence(body) {
		return false
	}
	if isGrokContentPolicyRejection(status, body) {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.code").String()))
	if code == "" {
		code = strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "code").String()))
	}
	for _, marker := range []string{"content_policy", "contentpolicy", "safety", "moderation", "sensitive", "违规", "审核", "敏感"} {
		if strings.Contains(code+" "+message, marker) {
			return false
		}
	}
	// 这几类完整 HTTP 响应表示认证、余额或准入失败，不包含可继续查询的任务。
	switch status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	case http.StatusMethodNotAllowed:
		return true
	}
	if !gjson.ValidBytes(body) {
		return false
	}
	combined := strings.ReplaceAll(code, "_", "") + " " + message
	for _, marker := range []string{"modelnotfound", "model not found", "model not supported", "unsupported model", "endpoint not supported", "unsupported endpoint"} {
		if (status == 400 || status == 404) && strings.Contains(combined, marker) {
			return true
		}
	}
	// 泛化 internal_error、HTML 网关页和超时可能发生在受理之后，不能据此生成第二份产物。
	if status >= 500 {
		for _, marker := range []string{"serviceunavailable", "service unavailable", "serverbusy", "server busy", "overloaded", "at capacity", "no available channel", "no available account", "not accepted", "未受理"} {
			if strings.Contains(combined, marker) {
				return true
			}
		}
	}
	return false
}

// 只检查协议定义的任务包与产物字段，不递归扫描 prompt、input 或任意用户回显。
// 没有任务 ID 不等于没有生成：已有成功状态、产物或正数用量时禁止再次 POST。
func videoCreationHasAcceptanceEvidence(body []byte) bool {
	root := gjson.ParseBytes(body)
	for _, path := range []string{"", "task", "data", "output", "video", "data.task", "output.task", "tasks"} {
		value := root
		if path != "" {
			value = root.Get(path)
		}
		if value.IsArray() {
			for _, item := range value.Array() {
				if videoCreationTaskHasAcceptanceEvidence(item) {
					return true
				}
			}
		} else if videoCreationTaskHasAcceptanceEvidence(value) {
			return true
		}
	}
	return false
}

func videoCreationTaskHasAcceptanceEvidence(task gjson.Result) bool {
	if !task.IsObject() {
		return false
	}
	// MiniMax H3 的 TaskId 也证明可能已经受理，不能在错误 HTTP 下安全重提。
	for _, field := range []string{"TaskId", "id", "task_id"} {
		value := task.Get(field)
		if value.Exists() && value.Type != gjson.Null && (value.Type != gjson.String || strings.TrimSpace(value.String()) != "") {
			return true
		}
	}
	for _, field := range []string{"accepted", "status", "task_status"} {
		switch strings.ToLower(strings.TrimSpace(task.Get(field).String())) {
		case "true", "created", "queued", "pending", "processing", "running", "in_progress", "submitted", "completed", "complete", "success", "succeeded", "succeed", "done":
			return true
		}
	}
	for _, path := range []string{"url", "video_url", "download_url", "content.url", "content.video_url", "video.url"} {
		if value := task.Get(path); value.Type == gjson.String && strings.TrimSpace(value.String()) != "" {
			return true
		}
	}
	for _, path := range []string{"videos", "task_result.videos"} {
		for _, video := range task.Get(path).Array() {
			if value := video.Get("url"); value.Type == gjson.String && strings.TrimSpace(value.String()) != "" {
				return true
			}
		}
	}
	for _, field := range []string{"input_tokens", "output_tokens", "prompt_tokens", "completion_tokens", "total_tokens", "input_seconds", "output_seconds", "total_seconds", "duration"} {
		value := task.Get("usage." + field)
		if value.Type == gjson.Number && value.Float() > 0 && !math.IsInf(value.Float(), 0) && !math.IsNaN(value.Float()) {
			return true
		}
	}
	return false
}

// VideoTaskCanRetrySubmission 由协调器和事务仓储同时校验，避免绕过已受理或已结算状态。
func VideoTaskCanRetrySubmission(t *VideoTaskRecord) bool {
	return t != nil && t.Status == "failed" && t.BillingStatus == "reserved" && !t.EffectsDone &&
		t.UpstreamTaskID == "" && VideoCreationDefinitelyRejected(t.CreateResponseStatus, t.CreateResponse)
}

// VideoTaskRetryTargetCompatible 保持已冻结请求的参数和计价含义，模型映射可变但转换协议不可变。
func VideoTaskRetryTargetCompatible(task *VideoTaskRecord, target VideoUpstreamTarget, metadata VideoRequestMetadata) bool {
	if task == nil || task.Target.Endpoint != target.Endpoint {
		return false
	}
	profile := func(value string) string {
		if value == "" {
			return VideoOpenAIProfilePassthrough
		}
		return value
	}
	if profile(task.Target.OpenAIProfile) != profile(target.OpenAIProfile) {
		return false
	}
	original := task.Metadata
	if original.Resolution != metadata.Resolution || original.Size != metadata.Size ||
		original.DurationSeconds != metadata.DurationSeconds || original.HasReferenceVideo != metadata.HasReferenceVideo {
		return false
	}
	if (original.ReferenceImageCount == nil) != (metadata.ReferenceImageCount == nil) {
		return false
	}
	return original.ReferenceImageCount == nil || *original.ReferenceImageCount == *metadata.ReferenceImageCount
}

// nextRejectedVideoAccount 只负责迁移明确失败的归属，不再次预扣或重新计算用户价卡。
func (s *VideoTaskService) nextRejectedVideoAccount(ctx context.Context, key *APIKey, req *VideoTaskSubmitRequest, task *VideoTaskRecord) (*VideoTaskRecord, *VideoUpstreamSelection, error) {
	repo, ok := s.repo.(VideoTaskRetryRepository)
	if !ok {
		return nil, nil, nil
	}
	if req.ExcludedAccountIDs == nil {
		req.ExcludedAccountIDs = make(map[int64]struct{})
	}
	req.ExcludedAccountIDs[task.AccountID] = struct{}{}
	MarkSmartRoutingAccountFailed(ctx, task.AccountID)
	for ctx.Err() == nil {
		next, err := s.upstream.SelectAccount(ctx, key, *req)
		if err != nil || next == nil || next.Account == nil {
			return nil, nil, nil
		}
		if _, excluded := req.ExcludedAccountIDs[next.Account.ID]; excluded {
			if next.Release != nil {
				next.Release()
			}
			return nil, nil, nil
		}
		req.ExcludedAccountIDs[next.Account.ID] = struct{}{}
		limit := int(videoCredentialNumber(next.Account, "video_max_pending_tasks"))
		if limit <= 0 {
			limit = 10
		}
		// 同一入站协议不允许在换号时改变厂商参数含义。
		if !VideoTaskRetryTargetCompatible(task, next.Target, next.Metadata) {
			if next.Release != nil {
				next.Release()
			}
			continue
		}
		moved, err := repo.SwitchRejected(ctx, task.ID, task.LeaseToken, next.Target, next.Metadata, next.Account.BillingRateMultiplier(), limit)
		if err == nil {
			return moved, next, nil
		}
		if next.Release != nil {
			next.Release()
		}
		if !errors.Is(err, ErrVideoTaskBusy) {
			return nil, nil, err
		}
	}
	return nil, nil, nil
}
