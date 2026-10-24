package routes

import (
	"github.com/TokenFlux/TokenRouter/internal/handler"
	"github.com/gin-gonic/gin"
)

// 只挂载在已鉴权的管理员路由组；总开关关闭时仍允许维护配置及查阅历史。
func registerAdminChanshuiRoutes(parent *gin.RouterGroup, h *handler.Handlers) {
	if h.ChanshuiTests == nil {
		return
	}
	g := parent.Group("/chanshui-tests")
	g.GET("", h.ChanshuiTests.List)
	g.GET("/targets", h.ChanshuiTests.Targets)
	g.POST("", h.ChanshuiTests.Save)
	g.GET("/runs/:id", h.ChanshuiTests.Detail)
	g.PUT("/:id", h.ChanshuiTests.Save)
	g.DELETE("/:id", h.ChanshuiTests.Delete)
	g.POST("/:id/run", h.ChanshuiTests.Run)
	g.GET("/:id/runs", h.ChanshuiTests.History)
}
