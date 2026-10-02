package routes

import (
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/handler"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// 共享入口按平台分派，已有本站任务允许原 Key 改组后继续取回，归属仍由任务服务复核。
func shouldHandleVideoPlatformRequest(c *gin.Context) bool {
	route, ok := service.MatchVideoGatewayRoute(c.Request.Method, c.Request.URL.Path)
	return ok && route.Protocol == "openai_videos" &&
		(getGroupPlatform(c) == service.PlatformVideo || (!route.Create && strings.HasPrefix(route.TaskID, "vid_")))
}

// registerVideoRoutes 只注册独立平台入口；旧Grok复数入口与Ark别名仍由原分派点拥有。
func registerVideoRoutes(r *gin.Engine, h *handler.Handlers, middlewares ...gin.HandlerFunc) {
	routes := []struct{ method, path string }{
		{http.MethodPost, "/v1/video/generations"}, {http.MethodGet, "/v1/video/generations/:task_id"}, {http.MethodDelete, "/v1/video/generations/:task_id"},
		{http.MethodPost, "/video/generations"}, {http.MethodGet, "/video/generations/:task_id"}, {http.MethodDelete, "/video/generations/:task_id"},
		{http.MethodGet, "/v1/tasks/:task_id"}, {http.MethodDelete, "/v1/tasks/:task_id"},
		{http.MethodPost, "/text-to-video/*model_id"}, {http.MethodPost, "/image-to-video/*model_id"}, {http.MethodPost, "/omni-video/*model_id"},
		{http.MethodPost, "/v1/videos/text2video"}, {http.MethodPost, "/v1/videos/omni-video"}, {http.MethodGet, "/tasks"},
		{http.MethodPost, "/api/v1/services/aigc/video-generation/video-synthesis"}, {http.MethodGet, "/api/v1/tasks/:task_id"},
		{http.MethodPost, "/v2/video_generation"}, {http.MethodGet, "/v2/query/video_generation/:task_id"},
	}
	for _, route := range routes {
		// 鉴权或请求体校验提前返回时，也禁止缓存任务入口响应。
		chain := []gin.HandlerFunc{func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Next() }}
		chain = append(chain, middlewares...)
		chain = append(chain, func(c *gin.Context) { h.Video.Handle(c) })
		r.Handle(route.method, route.path, chain...)
	}
}
