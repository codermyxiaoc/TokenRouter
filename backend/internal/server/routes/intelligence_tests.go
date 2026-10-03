package routes

import (
	"github.com/TokenFlux/TokenRouter/internal/handler"
	"github.com/gin-gonic/gin"
)

// registerIntelligenceRoutes 只开放登录用户的可见结果，不允许用户发起付费检测。
// @project-doc docs/domains/intelligence_tests.md#intelligence_access
func registerIntelligenceRoutes(parent *gin.RouterGroup, h *handler.Handlers) {
	if h.IntelligenceTests == nil {
		return
	}
	g := parent.Group("/intelligence-tests")
	g.GET("", h.IntelligenceTests.UserList)
	g.GET("/runs/:id", h.IntelligenceTests.UserDetail)
	g.GET("/runs/:id/preview", h.IntelligenceTests.UserPreview)
}

// registerAdminIntelligenceRoutes 总开关关闭后仍保留配置访问，由服务层阻止新检测。
func registerAdminIntelligenceRoutes(parent *gin.RouterGroup, h *handler.Handlers) {
	if h.IntelligenceTests == nil {
		return
	}
	g := parent.Group("/intelligence-tests")
	g.GET("", h.IntelligenceTests.AdminList)
	g.POST("", h.IntelligenceTests.AdminSave)
	g.GET("/runs/:id", h.IntelligenceTests.AdminDetail)
	g.GET("/runs/:id/preview", h.IntelligenceTests.AdminPreview)
	g.PUT("/:id", h.IntelligenceTests.AdminSave)
	g.DELETE("/:id", h.IntelligenceTests.AdminDelete)
	g.POST("/:id/run", h.IntelligenceTests.AdminRun)
	g.GET("/:id/runs", h.IntelligenceTests.AdminRuns)
}
