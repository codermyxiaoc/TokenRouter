package service

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

// 缺失或不支持的主语必须是模型，不能把“参考图片不存在，但请求使用 model”拼成模型故障。
// 保留带引号的模型 ID、Google models/ 路径和常见的时态，不匹配任意两个关键词的组合。
var upstreamModelMissingMessagePattern = regexp.MustCompile(`(?i)\bmodels?(?:/[a-z0-9_.:/-]+|\s+(?:["'\x60][^"'\x60\r\n]{1,256}["'\x60]|[a-z0-9_.:/-]+))?\s+(?:(?:is|was|are|were)\s+)?(?:not[\s_-]+found|does[\s_-]+not[\s_-]+exist|do[\s_-]+not[\s_-]+exist|not[\s_-]+supported)\b`)

// 兼容上游可能用 401 表示模型不存在；显式认证错误码必须优先，不能被正文关键词掩盖。
func isOpenAICompatibleModelNotFoundBody(body []byte) bool {
	if code := strings.TrimSpace(extractUpstreamErrorCode(body)); code != "" {
		return strings.EqualFold(code, "model_not_found")
	}
	if hasNonModelMissingErrorEvidence(body) {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	if message == "" && !gjson.ValidBytes(body) {
		message = strings.ToLower(strings.TrimSpace(string(body)))
	}
	return isUpstreamModelMissingMessage(message)
}

func isUpstreamModelNotFoundError(statusCode int, body []byte) bool {
	return IsUpstreamModelNotFoundError(statusCode, body)
}

// IsUpstreamModelNotFoundError 只读取上游错误信封中的模型缺失证据，避免将回显的用户正文误判为账号故障。
// 它不判断客户端模型权限；调用方必须先通过本站模型准入，再据此切换当前上游账号。
func IsUpstreamModelNotFoundError(statusCode int, body []byte) bool {
	if statusCode != http.StatusNotFound {
		return false
	}
	message := string(body)
	if gjson.ValidBytes(body) {
		code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.code").String()))
		if code == "model_not_found" {
			return true
		}
		// Google 的 code 为 HTTP 数字；其它明确的错误码不能被 message 覆盖。
		if code != "" && code != "404" && code != "not_found" {
			return false
		}
		if hasNonModelMissingErrorEvidence(body) {
			return false
		}
		message = gjson.GetBytes(body, "error.message").String()
	}
	return isUpstreamModelMissingMessage(message)
}

// 参数位置和 Google 错误类别比自然语言关键词更明确；模型名字段本身仍允许报告不存在。
// OpenAI 常用 invalid_request_error 表达模型缺失，不能仅凭该 type 一概排除。
func hasNonModelMissingErrorEvidence(body []byte) bool {
	if !gjson.ValidBytes(body) {
		return false
	}
	if param := strings.TrimSpace(gjson.GetBytes(body, "error.param").String()); param != "" && !strings.EqualFold(param, "model") {
		return true
	}
	switch strings.ToUpper(strings.TrimSpace(gjson.GetBytes(body, "error.status").String())) {
	case "INVALID_ARGUMENT", "UNAUTHENTICATED", "PERMISSION_DENIED":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.type").String())) {
	case "authentication_error", "permission_error", "rate_limit_error":
		return true
	}
	return false
}

// 只识别错误消息中的模型能力证据；缺少 model 参数或输入文件/图片/缓存均由请求方处理。
func isUpstreamModelMissingMessage(message string) bool {
	normalized := normalizeModelNotFoundBody([]byte(message))
	for _, resource := range []string{"model parameter", "model field", "model argument", "model not found in request", "reference image", "input image", "input file", "cached content"} {
		if strings.Contains(normalized, resource) {
			return false
		}
	}
	if strings.Contains(normalized, "unknown provider for model") ||
		strings.Contains(normalized, "unknown model") ||
		strings.Contains(normalized, "model not found") ||
		strings.Contains(normalized, "requested model or endpoint is unavailable") {
		return true
	}
	return upstreamModelMissingMessagePattern.MatchString(message)
}

func isModelNotFoundError(statusCode int, body []byte) bool {
	return isUpstreamModelNotFoundError(statusCode, body) || statusCode == http.StatusNotFound
}

// upstreamModelNotFoundEventReason 为跨组恢复保存结构化原因，不依赖日志是否保留上游正文。
func upstreamModelNotFoundEventReason(statusCode int, body []byte) string {
	if IsUpstreamModelNotFoundError(statusCode, body) {
		return "model_not_found"
	}
	return ""
}

// openAICodexPlanGatedModelPhrase 匹配 ChatGPT OAuth 套餐无法使用目标模型时的确定性 Codex 400。
// 响应体会先统一为小写并把下划线、连字符折叠为空格，因此也能匹配 error.message 等载体。
const openAICodexPlanGatedModelPhrase = "model is not supported when using codex"

// isOpenAICodexPlanGatedModelError 判断上游是否因 ChatGPT 套餐门控而确定性拒绝目标模型。
// 在套餐变化前重试同一账号不会成功，应像模型不存在一样冷却账号与模型组合。
func isOpenAICodexPlanGatedModelError(statusCode int, body []byte) bool {
	if statusCode != http.StatusBadRequest {
		return false
	}
	normalized := normalizeModelNotFoundBody(body)
	if normalized == "" {
		return false
	}
	return strings.Contains(normalized, openAICodexPlanGatedModelPhrase)
}

func normalizeModelNotFoundBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	normalized := strings.ToLower(string(body))
	normalized = strings.NewReplacer("_", " ", "-", " ", "\n", " ", "\r", " ", "\t", " ").Replace(normalized)
	return strings.Join(strings.Fields(normalized), " ")
}
