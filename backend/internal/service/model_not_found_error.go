package service

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

var upstreamModelNotFoundKeywords = []string{"model not found", "unknown model", "not found"}

// 兼容上游可能用 401 表示模型不存在；显式认证错误码必须优先，不能被正文关键词掩盖。
func isOpenAICompatibleModelNotFoundBody(body []byte) bool {
	if code := strings.TrimSpace(extractUpstreamErrorCode(body)); code != "" {
		return strings.EqualFold(code, "model_not_found")
	}
	message := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	if message == "" && !gjson.ValidBytes(body) {
		message = strings.ToLower(strings.TrimSpace(string(body)))
	}
	return strings.Contains(message, "unknown provider for model") ||
		strings.Contains(message, "unknown model") ||
		strings.Contains(message, "model not found") ||
		strings.Contains(message, "model is not supported")
}

func isUpstreamModelNotFoundError(statusCode int, body []byte) bool {
	if statusCode != http.StatusNotFound {
		return false
	}
	normalized := normalizeModelNotFoundBody(body)
	if normalized == "" || !strings.Contains(normalized, "model") {
		return false
	}
	return containsModelNotFoundKeyword(normalized)
}

func isModelNotFoundError(statusCode int, body []byte) bool {
	return isUpstreamModelNotFoundError(statusCode, body) || statusCode == http.StatusNotFound
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

func containsModelNotFoundKeyword(normalizedBody string) bool {
	if normalizedBody == "" {
		return false
	}
	for _, keyword := range upstreamModelNotFoundKeywords {
		if strings.Contains(normalizedBody, keyword) {
			return true
		}
	}
	return false
}

func normalizeModelNotFoundBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	normalized := strings.ToLower(string(body))
	normalized = strings.NewReplacer("_", " ", "-", " ", "\n", " ", "\r", " ", "\t", " ").Replace(normalized)
	return strings.Join(strings.Fields(normalized), " ")
}
