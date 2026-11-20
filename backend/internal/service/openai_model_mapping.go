package service

import "strings"

// resolveOpenAIForwardModel 解析 OpenAI 兼容转发使用的最终模型。
// messagesDispatchMappedModel 是渠道映射后再执行分组映射得到的账号层模型 D；
// 非空时账号映射必须以 D 为输入，普通 OpenAI 请求必须传空。
func resolveOpenAIForwardModel(account *Account, requestedModel, messagesDispatchMappedModel string) string {
	messagesDispatchMappedModel = strings.TrimSpace(messagesDispatchMappedModel)
	accountLayerModel := requestedModel
	if messagesDispatchMappedModel != "" {
		accountLayerModel = messagesDispatchMappedModel
	}
	if account == nil {
		return accountLayerModel
	}
	return account.GetMappedModel(accountLayerModel)
}

// openAIOAuthForeignModelPrefixes 列出明确属于其他厂商家族的模型名前缀。
// Codex 上游不可能服务这些模型：转发阶段 normalizeOpenAIModelForUpstream
// 对未知模型原样透传，上游必然返回不可重试的 400。
//
// 这些家族沿用无账号映射时的过滤规则；Claude 在普通 OAuth 上始终按最终目标检查。
var openAIOAuthForeignModelPrefixes = []string{
	"deepseek-",
	"glm-",
	"kimi-",
	"moonshot-",
	"qwen-",
	"qwen2-",
	"qwen3-",
	"qwen4-",
	"qwq-",
	"minimax-",
	"gemini-",
	"gemma-",
	"grok-",
	"doubao-",
	"hunyuan-",
	"llama-",
	"llama2-",
	"llama3-",
	"meta-llama",
	"mistral-",
	"mixtral-",
	"baichuan-",
	"ernie-",
	"step-",
	"seed-",
	"yi-",
}

// 普通 OAuth 的资格由实际要发往 Codex 的目标决定；透传继续由账号自己的过滤规则控制。
func (a *Account) isOpenAIOAuthServableModel(upstreamModel string) bool {
	if !a.IsOpenAIOAuth() || a.IsOpenAIPassthroughEnabled() {
		return true
	}
	model := strings.ToLower(lastOpenAIModelSegment(upstreamModel))
	if strings.HasPrefix(model, "claude-") {
		return false
	}
	if model == "" || len(a.GetModelMapping()) != 0 {
		return true
	}
	// Kimi Code 官方 bare model ID 没有厂商前缀，前缀黑名单无法识别。
	if model == "k3" || model == "k3-256k" {
		return false
	}
	for _, prefix := range openAIOAuthForeignModelPrefixes {
		if strings.HasPrefix(model, prefix) {
			return false
		}
	}
	return true
}

// deepseekServableModels 使用同步自上游 v0.2.5 的默认模型快照；管理员显式映射/白名单可扩展。
var deepseekServableModels = []string{
	"deepseek-flash",
	"deepseek-v4-pro",
	"deepseek-v4-flash",
	"deepseek-v4-flash-vision-exp",
	"deepseek-v4-pro-0813",
}

// isDeepseekServableModel 仅为无显式模型配置的账号校验目录，先移除客户端上下文后缀。
func isDeepseekServableModel(requestedModel string) bool {
	// 官方 Claude Code 接入文档要求 ANTHROPIC_MODEL=deepseek-flash[1m]：
	// [1m] 是客户端上下文选择器，先按同一规则归一化再比对白名单。
	model := strings.ToLower(normalizeClaudeCodeLongContextModel(strings.TrimSpace(requestedModel)))
	if model == "" {
		return true // 空模型交由上层必填校验处理
	}
	for _, servable := range deepseekServableModels {
		if model == servable {
			return true
		}
	}
	return false
}

// resolveOpenAICompactForwardModel determines the compact-only upstream model
// for /responses/compact requests. It never affects normal /responses traffic.
// When no compact-specific mapping matches, the input model is returned as-is.
func resolveOpenAICompactForwardModel(account *Account, model string) string {
	trimmedModel := strings.TrimSpace(model)
	if trimmedModel == "" || account == nil {
		return trimmedModel
	}

	mappedModel, matched := account.ResolveCompactMappedModel(trimmedModel)
	if !matched {
		return trimmedModel
	}
	if trimmedMapped := strings.TrimSpace(mappedModel); trimmedMapped != "" {
		return trimmedMapped
	}
	return trimmedModel
}
