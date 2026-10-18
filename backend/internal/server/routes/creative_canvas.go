package routes

import (
	"github.com/TokenFlux/TokenRouter/internal/handler"
	"github.com/gin-gonic/gin"
)

// @project-doc docs/domains/cloud_canvas.md#persistence
// 所有路径继承用户 JWT 与面板限流，生成和上传另外使用重请求限流。
func registerCreativeCanvasRoutes(group *gin.RouterGroup, h *handler.CreativeCanvasHandler, heavy gin.HandlerFunc) {
	if h == nil {
		return
	}
	group.GET("/canvases", h.List)
	group.POST("/canvases", heavy, h.Create)
	group.GET("/canvases/:id", h.Get)
	group.PUT("/canvases/:id", h.Update)
	group.DELETE("/canvases/:id", h.Delete)
	group.GET("/assets", h.ListAssets)
	group.POST("/assets", heavy, h.UploadAsset)
	group.GET("/assets/:id", h.GetAsset)
	group.DELETE("/assets/:id", h.DeleteAsset)
	group.GET("/assets/:id/content", h.AssetContent)
	group.HEAD("/assets/:id/content", h.AssetContent)
	group.POST("/executions", heavy, h.CreateExecution)
	group.POST("/execution-batches", heavy, h.CreateExecutionBatch)
	group.GET("/executions", h.ListExecutions)
	group.GET("/executions/:id", h.GetExecution)
}
