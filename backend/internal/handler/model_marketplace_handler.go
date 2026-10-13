package handler

import (
	"github.com/TokenFlux/TokenRouter/internal/handler/dto"
	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"strings"

	"github.com/gin-gonic/gin"
)

type ModelMarketplaceHandler struct {
	modelMarketplaceService *service.ModelMarketplaceService
	dashboardService        *service.DashboardService
}

func NewModelMarketplaceHandler(modelMarketplaceService *service.ModelMarketplaceService, dashboardService *service.DashboardService) *ModelMarketplaceHandler {
	return &ModelMarketplaceHandler{
		modelMarketplaceService: modelMarketplaceService,
		dashboardService:        dashboardService,
	}
}

// ListPublic 返回公开模型广场列表。
// GET /api/v1/marketplace/models
func (h *ModelMarketplaceHandler) ListPublic(c *gin.Context) {
	groups, err := h.modelMarketplaceService.ListPublic(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	varyMarketplaceLanguage(c)
	response.Success(c, dto.ModelMarketplaceGroupsFromService(groups, c.GetHeader("Accept-Language")))
}

// StatsPublic 返回首页公开统计，只暴露总 Token 和注册用户数。
// GET /api/v1/marketplace/stats
func (h *ModelMarketplaceHandler) StatsPublic(c *gin.Context) {
	stats, err := h.dashboardService.GetPublicDashboardStats(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.ModelMarketplaceStatsFromService(stats))
}

// varyMarketplaceLanguage 保留 CORS 等中间件已有的 Vary，防止不同语言共享缓存。
func varyMarketplaceLanguage(c *gin.Context) {
	for _, line := range c.Writer.Header().Values("Vary") {
		for _, token := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "Accept-Language") || strings.TrimSpace(token) == "*" {
				return
			}
		}
	}
	c.Writer.Header().Add("Vary", "Accept-Language")
}
