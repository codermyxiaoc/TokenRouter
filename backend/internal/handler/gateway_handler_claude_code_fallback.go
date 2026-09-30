package handler

import (
	middleware "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// allowsClaudeCodeCompatibilityFallback 只为现有 Anthropic 兼容桥开放有权限的降级链。
// 保留原 Key 的计费主体；智能路由的有序候选不能通过传统 fallback 扩大范围。
func (h *GatewayHandler) allowsClaudeCodeCompatibilityFallback(c *gin.Context, apiKey *service.APIKey, protocol service.GroupClientProtocol) bool {
	if apiKey == nil || apiKey.SmartRouting || apiKey.Group == nil || apiKey.User == nil || h.gatewayService == nil {
		return false
	}
	group := apiKey.Group
	visited := map[int64]bool{group.ID: true}
	subscription, _ := middleware.GetSubscriptionFromContext(c)
	for group.ClaudeCodeOnly {
		if group.FallbackGroupID == nil || *group.FallbackGroupID <= 0 || visited[*group.FallbackGroupID] {
			return false
		}
		visited[*group.FallbackGroupID] = true
		next, err := h.gatewayService.ResolveGroupByID(c.Request.Context(), *group.FallbackGroupID)
		if err != nil || next == nil || next.Status != service.StatusActive ||
			!apiKey.User.CanBindGroup(next.ID, next.IsExclusive) || !next.AllowsClientProtocol(protocol) {
			return false
		}
		// 本处理器只实现 Anthropic 与 Antigravity 转发，其他平台需要重新分派协议处理器。
		if next.Platform != service.PlatformAnthropic && next.Platform != service.PlatformAntigravity {
			return false
		}
		// 等待后的原有资金检查继续校验有效期和额度；此处额外锁定目标套餐范围。
		if service.APIKeyEffectiveBillingMode(apiKey) == service.APIKeyBillingModeSubscription &&
			!service.SubscriptionAllowsGroup(subscription, next.ID) {
			return false
		}
		group = next
	}
	return true
}
