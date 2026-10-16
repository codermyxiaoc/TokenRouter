package service

import "strings"

// GPT-5.5 已退出 Codex 订阅账号；只检查真实上游目标，不改变 API Key 和其它平台能力。
// @project-doc docs/interfaces/openai_upstream.md#openai_model_retirement
func isRetiredOpenAISubscriptionModel(account *Account, upstreamModel string) bool {
	if account == nil || !account.IsOpenAI() || (!account.IsOpenAIOAuthLike() && !account.IsShadow()) {
		return false
	}
	model := canonicalizeOpenAIModelAliasSpelling(upstreamModel)
	return model == "gpt-5.5" || strings.HasPrefix(model, "gpt-5.5-")
}

// IsRetiredOpenAISubscriptionModel 供目录按已解析的最终模型过滤，避免再次应用别名映射。
func IsRetiredOpenAISubscriptionModel(account *Account, upstreamModel string) bool {
	return isRetiredOpenAISubscriptionModel(account, upstreamModel)
}

// 仅在所有支持公开名称的账号都指向已退役目标时隐藏；显式替代映射和 API Key 保留。
func openAIModelRetiredForAccounts(accounts []Account, modelID string) bool {
	retired := false
	for i := range accounts {
		account := &accounts[i]
		if !account.IsOpenAI() {
			continue
		}
		if isRetiredOpenAISubscriptionModel(account, resolveOpenAIAccountUpstreamModelForRequest(account, modelID, false)) {
			retired = true
		} else if account.IsModelSupported(modelID) {
			return false
		}
	}
	return retired
}

// FilterRetiredOpenAIModelIDs 接收已经投影的公开名称，绝不把上游源 ID 当作请求别名。
func FilterRetiredOpenAIModelIDs(accounts []Account, models []string) []string {
	filtered := make([]string, 0, len(models))
	for _, model := range models {
		if !openAIModelRetiredForAccounts(accounts, model) {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

// 支持 compact 专用映射，同时继续执行当前项目的最终白名单和平台能力校验。
func supportsOpenAIRequestModel(account *Account, requestedModel string, requireCompact bool) bool {
	if account == nil {
		return false
	}
	if account.IsOpenAI() && isRetiredOpenAISubscriptionModel(account, resolveOpenAIAccountUpstreamModelForRequest(account, requestedModel, requireCompact)) {
		return false
	}
	return account.isModelMappingSupported(requestedModel)
}
