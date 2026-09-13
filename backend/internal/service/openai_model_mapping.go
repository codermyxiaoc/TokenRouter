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
// 采用保守黑名单而非 Codex 模型白名单：已知 bare ID 做等值排除，
// 未知/自定义别名仍保持“允许”，
// 以兼容渠道级模型映射等“账号选定之后才改写模型名”的部署方式
// （调度过滤看到的是改写前的原始模型名）。前缀分类的先例见
// ResolveThinkingProtocol（thinking_protocol.go）。
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

// isOpenAIOAuthServableModel 判断 OpenAI OAuth 账号能否服务请求模型。
// 默认仍为“允许”，仅排除明确属于其他厂商的模型家族；这类模型
// 原样透传必然被 Codex 上游以不可重试的 400 拒绝，应在调度阶段跳过该账号。
func isOpenAIOAuthServableModel(requestedModel string) bool {
	model := strings.ToLower(lastOpenAIModelSegment(requestedModel))
	if model == "" {
		return true // 空模型交由上层必填校验处理。
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
