package routes

import (
	"github.com/TokenFlux/TokenRouter/internal/handler"
	"github.com/gin-gonic/gin"
)

// registerTicketRoutes 注册个人工单，附件下载与详情使用相同权限。
func registerTicketRoutes(parent *gin.RouterGroup, h *handler.Handlers) {
	if h.Ticket == nil {
		return
	}
	g := parent.Group("/tickets")
	g.GET("/config", h.Ticket.Config)
	g.GET("", h.Ticket.List)
	g.POST("", h.Ticket.Create)
	g.GET("/:id", h.Ticket.Get)
	g.POST("/:id/replies", h.Ticket.Reply)
	g.POST("/:id/cancel", h.Ticket.Cancel)
	g.POST("/:id/complete", h.Ticket.Complete)
	g.GET("/:id/attachments/:attachmentId", h.Ticket.Download)
	g.GET("/:id/attachments/:attachmentId/preview", h.Ticket.Preview)
}

// registerAdminTicketRoutes 设置静态路径先于详情注册，避免与工单编号混淆。
func registerAdminTicketRoutes(parent *gin.RouterGroup, h *handler.Handlers) {
	if h.Ticket == nil {
		return
	}
	g := parent.Group("/tickets")
	g.GET("/settings", h.Ticket.AdminConfig)
	g.PUT("/settings", h.Ticket.UpdateConfig)
	g.GET("", h.Ticket.AdminList)
	g.GET("/:id", h.Ticket.AdminGet)
	g.PATCH("/:id", h.Ticket.Update)
	g.POST("/:id/replies", h.Ticket.AdminReply)
	g.POST("/:id/complete", h.Ticket.AdminComplete)
	g.POST("/:id/cancel", h.Ticket.AdminCancel)
	g.GET("/:id/attachments/:attachmentId", h.Ticket.AdminDownload)
	g.GET("/:id/attachments/:attachmentId/preview", h.Ticket.AdminPreview)
}
