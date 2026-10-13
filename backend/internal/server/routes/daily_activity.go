package routes

import (
	"github.com/TokenFlux/TokenRouter/internal/handler"
	"github.com/gin-gonic/gin"
)

// registerDailyActivityRoutes 沿用登录身份、面板限流和审计，资格与金额均由服务端计算。
func registerDailyActivityRoutes(parent *gin.RouterGroup, h *handler.Handlers, limiter gin.HandlerFunc) {
	if h.DailyActivities == nil {
		return
	}
	g := parent.Group("/daily-activities")
	if limiter != nil {
		g.Use(limiter)
	}
	g.GET("", h.DailyActivities.Status)
	g.GET("/history", h.DailyActivities.History)
	g.POST("/claim", h.DailyActivities.Claim)
}

// 管理配置独立保存，避免通用设置覆盖累计起点；关闭活动后仍允许管理员调整规则。
func registerAdminDailyActivityRoutes(parent *gin.RouterGroup, h *handler.Handlers) {
	if h.DailyActivities == nil {
		return
	}
	parent.GET("/settings/daily-activity", h.DailyActivities.GetConfig)
	parent.PUT("/settings/daily-activity", h.DailyActivities.SaveConfig)
}
