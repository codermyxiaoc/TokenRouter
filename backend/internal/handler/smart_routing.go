package handler

import (
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// SmartRoutingAuth 复用网关已有依赖，让两种协议认证共享相同候选检查规则。
func (h *GatewayHandler) SmartRoutingAuth(next gin.HandlerFunc, guards ...middleware.SmartRoutingRetryGuard) gin.HandlerFunc {
	if h == nil {
		return middleware.WithSmartRoutingResolver(nil, next, guards...)
	}
	return middleware.WithSmartRoutingResolver(service.NewSmartRoutingService(h.gatewayService, h.openAIGatewayService), next, guards...)
}
