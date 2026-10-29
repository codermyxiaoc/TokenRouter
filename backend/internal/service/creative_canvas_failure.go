package service

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// creativeProviderFailureCode 只从受限诊断文本归类，不把供应商原文、凭据或素材地址传给用户。
// 分类仅用于展示；重试、受理确认和资金处理继续由原任务状态机决定。
func creativeProviderFailureCode(status int, diagnostic string) string {
	// 明确的 HTTP 认证、支付和服务故障优先，不能被错误文案中提及的审核子系统误导。
	switch {
	case status == http.StatusUnauthorized:
		return "PROVIDER_AUTH_FAILED"
	case status == http.StatusPaymentRequired:
		return "PROVIDER_QUOTA_EXHAUSTED"
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return "PROVIDER_TIMEOUT"
	case status >= 500:
		return "PROVIDER_UNAVAILABLE"
	}
	if code := creativeProviderDiagnosticCode(diagnostic); code != "" {
		return code
	}
	switch status {
	case http.StatusForbidden:
		return "PROVIDER_ACCESS_DENIED"
	case http.StatusTooManyRequests:
		return "PROVIDER_RATE_LIMITED"
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return "PROVIDER_INVALID_PARAMS"
	case http.StatusNotFound:
		return "PROVIDER_ENDPOINT_NOT_FOUND"
	}
	return ""
}

// 诊断匹配只接受错误类别或明确拒绝语义，不把任意 safety/moderation 子串当成内容违规。
func creativeProviderDiagnosticCode(diagnostic string) string {
	if len(diagnostic) > 8192 {
		return ""
	}
	value := strings.ToLower(diagnostic)
	has := func(parts ...string) bool {
		for _, part := range parts {
			if strings.Contains(value, part) {
				return true
			}
		}
		return false
	}
	switch {
	case has("content_policy", "content policy", "content_filter", "safety violation", "safety policy", "blocked by safety", "moderation blocked", "moderation rejected", "failed moderation", "nsfw", "敏感内容", "内容审核未通过", "内容审核失败", "内容违规"):
		return "PROVIDER_CONTENT_BLOCKED"
	case has("insufficient_quota", "insufficient balance", "quota exceeded", "quota_exceeded", "credit balance", "余额不足", "额度不足", "配额"):
		return "PROVIDER_QUOTA_EXHAUSTED"
	case has("model_not_found", "model not found", "unsupported model", "invalid model", "模型不存在", "模型不可用"):
		return "PROVIDER_MODEL_UNAVAILABLE"
	case has("rate_limit", "rate limit", "too many requests"):
		return "PROVIDER_RATE_LIMITED"
	case has("timed out", "timeout", "deadline exceeded", "超时"):
		return "PROVIDER_TIMEOUT"
	case has("invalid_parameter", "invalid parameter", "invalid_params", "invalid params"):
		return "PROVIDER_INVALID_PARAMS"
	}
	return ""
}

// 仅提取有界错误字段，优先使用机器错误码；不把提示词、素材地址等普通响应内容用于分类。
func creativeProviderResponseDiagnostic(raw []byte) string {
	if len(raw) == 0 || len(raw) > 1<<20 || !gjson.ValidBytes(raw) {
		return ""
	}
	readFields := func(paths []string) string {
		var result strings.Builder
		for _, path := range paths {
			part := gjson.GetBytes(raw, path)
			if part.Type == gjson.String && len(part.Str) <= 1024 && result.Len()+len(part.Str)+1 <= 8192 {
				result.WriteString(part.Str)
				result.WriteByte(' ')
			}
		}
		return result.String()
	}
	code := readFields([]string{"error.code", "error.type", "code", "type", "task.error.code", "task.error.type", "output.code", "output.error.code", "output.error.type"})
	if creativeProviderDiagnosticCode(code) != "" {
		return code
	}
	return readFields([]string{"error.message", "error", "message", "detail", "msg", "base_resp.status_msg", "task.error.message", "task.error", "output.message", "output.error.message", "data.task_status_msg", "data.0.task_status_msg", "err_msg"})
}

// HTTP 执行入口保留结构化错误码供安全分类，但错误对象中只保存本站固定文案。
func creativeHTTPResponseError(status int, raw []byte) *CreativeUpstreamError {
	return creativeHTTPStatusError(status, creativeProviderResponseDiagnostic(raw))
}

// creativeProviderFailureMessage 的全部文案由本站提供，未知类别不使用原包兜底。
func creativeProviderFailureMessage(code string) string {
	switch code {
	case "PROVIDER_CONTENT_BLOCKED":
		return "上游内容审核未通过，请调整提示词或参考素材"
	case "PROVIDER_AUTH_FAILED":
		return "上游账号认证失败，请联系管理员检查账号凭据"
	case "PROVIDER_ACCESS_DENIED":
		return "上游账号没有当前模型的调用权限，请联系管理员检查授权"
	case "PROVIDER_QUOTA_EXHAUSTED":
		return "上游账号余额或额度不足，请联系管理员处理；这不是您的站内余额不足"
	case "PROVIDER_MODEL_UNAVAILABLE":
		return "上游不支持当前模型，请联系管理员检查模型映射"
	case "PROVIDER_RATE_LIMITED":
		return "上游请求频率或并发已达限制，请稍后再试"
	case "PROVIDER_TIMEOUT":
		return "上游生成请求超时，未取得可交付结果"
	case "PROVIDER_INVALID_PARAMS":
		return "上游拒绝生成参数，请检查所选模型的尺寸、比例、时长和参考素材要求"
	case "PROVIDER_ENDPOINT_NOT_FOUND":
		return "上游生成端点不存在，请联系管理员检查服务地址和端点配置"
	case "PROVIDER_UNAVAILABLE":
		return "上游生成服务暂不可用，请稍后再试"
	case CreativeResultUnavailableCode:
		return "上游未返回可用结果，本次未扣费，预扣已释放"
	case creativeOutputUndeliveredCode:
		return "成品未能保存交付，本次未扣费，预扣已释放"
	case "PROVIDER_FAILED":
		return "生成服务执行失败，请联系管理员凭任务编号查看失败原因"
	}
	return ""
}

// creativeVideoFailureCode 仅读取固定错误字段；成功包中的提示词和媒体元数据不能成为错误原因。
func creativeVideoFailureCode(task *VideoTaskRecord) string {
	if task.Status == "cancelled" {
		return "CREATIVE_VIDEO_CANCELLED"
	}
	if task.Status == "expired" {
		return "CREATIVE_VIDEO_EXPIRED"
	}
	if task.SubmissionFailureKind == "timeout" || task.SubmissionFailureKind == "interrupted" {
		return "CREATIVE_VIDEO_PROVIDER_TIMEOUT"
	}
	raw := task.RawResponse
	if len(raw) == 0 {
		raw = task.CreateResponse
	}
	code := creativeProviderFailureCode(task.ResponseStatus, creativeProviderResponseDiagnostic(raw))
	if code == "" {
		return "CREATIVE_VIDEO_FAILED"
	}
	return "CREATIVE_VIDEO_" + code
}

// creativeVideoTerminalMessage 仅用于原视频账本确认已释放的终态；核对中的任务不能宣称退款。
func creativeVideoTerminalMessage(code string) string {
	switch code {
	case "CREATIVE_VIDEO_CANCELLED":
		return "视频任务已取消，预扣已释放"
	case "CREATIVE_VIDEO_EXPIRED":
		return "视频任务已过期，预扣已释放"
	case "CREATIVE_VIDEO_FAILED":
		return "上游视频生成失败，未返回可识别的错误原因；预扣已释放，请联系管理员核对"
	}
	if strings.HasPrefix(code, "CREATIVE_VIDEO_PROVIDER_") {
		if reason := creativeProviderFailureMessage(strings.TrimPrefix(code, "CREATIVE_VIDEO_")); reason != "" {
			return reason + "；预扣已释放"
		}
	}
	return ""
}
