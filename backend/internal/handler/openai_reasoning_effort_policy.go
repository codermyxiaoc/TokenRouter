package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// reasoningEffortPolicyForRequest 返回请求目标平台对应的分组策略。
// 复合 Key 已由鉴权中间件投影到具体分组，这里不重新引入旧的复合平台解析层。
func reasoningEffortPolicyForRequest(c *gin.Context, apiKey *service.APIKey, platform string) (string, []service.ReasoningEffortMapping, string, bool) {
	if apiKey == nil || apiKey.Group == nil || apiKey.Group.Platform != platform {
		return "", nil, "", false
	}
	if effectiveAPIKeyPlatform(c, apiKey) != platform {
		return "", nil, "", false
	}
	return apiKey.Group.MaxReasoningEffort,
		apiKey.Group.ReasoningEffortMappings,
		apiKey.Group.MaxReasoningEffortOverLimit,
		true
}

// openAIReasoningEffortPolicyForRequest 返回 OpenAI 分组策略。
func openAIReasoningEffortPolicyForRequest(c *gin.Context, apiKey *service.APIKey) (string, []service.ReasoningEffortMapping, string, bool) {
	return reasoningEffortPolicyForRequest(c, apiKey, service.PlatformOpenAI)
}

// anthropicReasoningEffortPolicyForRequest 返回 Anthropic 分组策略。
func anthropicReasoningEffortPolicyForRequest(c *gin.Context, apiKey *service.APIKey) (string, []service.ReasoningEffortMapping, string, bool) {
	// /v1/messages 由通用 Anthropic handler 独立承接，不能使用 OpenAI 兼容
	// handler 的默认平台推断，否则 Anthropic 分组会被误判为 OpenAI。
	if apiKey == nil || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformAnthropic {
		return "", nil, "", false
	}
	if c != nil {
		if platform, forced := middleware.GetForcePlatformFromContext(c); forced && platform != service.PlatformAnthropic {
			return "", nil, "", false
		}
	}
	return apiKey.Group.MaxReasoningEffort,
		apiKey.Group.ReasoningEffortMappings,
		apiKey.Group.MaxReasoningEffortOverLimit,
		true
}

// applyOpenAIReasoningEffortPolicyForRequest 在策略改写前保存客户端档位，并执行分组裁决。
func applyOpenAIReasoningEffortPolicyForRequest(c *gin.Context, apiKey *service.APIKey, body []byte) ([]byte, bool, error) {
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	bindRequestedReasoningEffort(c, body, model)
	maxEffort, mappings, overLimit, ok := openAIReasoningEffortPolicyForRequest(c, apiKey)
	if !ok {
		return body, false, nil
	}
	return service.ApplyOpenAIReasoningEffortPolicy(body, maxEffort, mappings, overLimit)
}

// applyAnthropicReasoningEffortPolicyForRequest 在 Anthropic Messages/Responses
// 请求转发前执行分组策略，支持 output_config.effort 和 reasoning.effort。
func applyAnthropicReasoningEffortPolicyForRequest(c *gin.Context, apiKey *service.APIKey, body []byte) ([]byte, bool, error) {
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	bindRequestedReasoningEffort(c, body, model)
	maxEffort, mappings, overLimit, ok := anthropicReasoningEffortPolicyForRequest(c, apiKey)
	if !ok {
		return body, false, nil
	}
	return service.ApplyOpenAIReasoningEffortPolicy(body, maxEffort, mappings, overLimit)
}

// respondOpenAIReasoningEffortPolicyError 将本地超限拒绝转换为 OpenAI 权限错误。
func respondOpenAIReasoningEffortPolicyError(c *gin.Context, err error, write func(*gin.Context, int, string, string)) {
	if c == nil || err == nil || write == nil {
		return
	}
	service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)
	write(c, http.StatusForbidden, "permission_error", err.Error())
}

// bindOpenAIReasoningEffortPolicyForMessagesRequest 为 Messages 请求绑定推理强度策略。
// 缺省 effort 时桥接器补出的默认强度不受分组上限约束（不被当作客户端请求的强度），
// 但显式关闭思考转换出的 none 仍按 from=none 映射，关闭思考时保留上限。
func bindOpenAIReasoningEffortPolicyForMessagesRequest(c *gin.Context, apiKey *service.APIKey, body []byte) {
	if c == nil || c.Request == nil {
		return
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	bindRequestedReasoningEffort(c, body, model)
	effort := gjson.GetBytes(body, "output_config.effort")
	explicitEffort := effort.Exists() && effort.Type == gjson.String && strings.TrimSpace(effort.String()) != ""
	// CN 平台仍只约束显式强度；其它平台缺省强度时由 Messages 专用策略决定（不加上限，只保留 none 映射）。
	isCNTarget := service.IsCNProvider(effectiveAPIKeyPlatform(c, apiKey))
	if isCNTarget && !explicitEffort {
		return
	}
	maxEffort, mappings, overLimit, ok := openAIReasoningEffortPolicyForRequest(c, apiKey)
	if !ok {
		return
	}
	var ctx context.Context
	if explicitEffort || isCNTarget {
		ctx = service.WithOpenAIReasoningEffortPolicyForModel(c.Request.Context(), maxEffort, mappings, overLimit, model)
	} else {
		ctx = service.WithOpenAIReasoningEffortPolicyForMessages(c.Request.Context(), body, maxEffort, mappings, overLimit, model)
	}
	c.Request = c.Request.WithContext(ctx)
}
