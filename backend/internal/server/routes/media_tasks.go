package routes

import (
	"github.com/TokenFlux/TokenRouter/internal/handler"
	"github.com/gin-gonic/gin"
)

// registerMediaTaskRoutes 复用各自父路由的认证和限流，管理身份由处理器再次验证。
func registerMediaTaskRoutes(parent *gin.RouterGroup, h *handler.Handlers, admin bool) {
	if h.MediaTask == nil {
		return
	}
	g := parent.Group("/media-tasks")
	if admin {
		g.GET("", h.MediaTask.AdminList)
		g.GET("/:id", h.MediaTask.AdminGet)
	} else {
		g.GET("", h.MediaTask.List)
		g.GET("/:id", h.MediaTask.Get)
	}
}
