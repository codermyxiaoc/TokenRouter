package admin

import (
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// requestDataCacheView 是管理端"数据缓存"设置页的数据：配置与当前缓存条数。
type requestDataCacheView struct {
	Settings *service.RequestDataCacheSettings `json:"settings"`
	Stats    *service.RequestDataCacheStats    `json:"stats"`
}

// requestDataCacheClearRequest 是手动清除请求，kind 取 normal / error / all。
type requestDataCacheClearRequest struct {
	Kind string `json:"kind"`
}

// loadRequestDataCacheView 读取配置与统计，供查询与保存后的回显复用。
func (h *OpsHandler) loadRequestDataCacheView(c *gin.Context, settings *service.RequestDataCacheSettings) (*requestDataCacheView, bool) {
	ctx := c.Request.Context()
	if settings == nil {
		cfg, err := h.opsService.GetRequestDataCacheSettings(ctx)
		if err != nil {
			response.Error(c, http.StatusInternalServerError, "Failed to get request data cache settings")
			return nil, false
		}
		settings = cfg
	}
	stats, err := h.opsService.GetRequestDataCacheStats(ctx)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to get request data cache stats")
		return nil, false
	}
	return &requestDataCacheView{Settings: settings, Stats: stats}, true
}

// GetRequestDataCache 返回请求数据缓存配置与统计。
// GET /api/v1/admin/ops/request-data-cache
func (h *OpsHandler) GetRequestDataCache(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	view, ok := h.loadRequestDataCacheView(c, nil)
	if !ok {
		return
	}
	response.Success(c, view)
}

// UpdateRequestDataCache 保存请求数据缓存配置。
// PUT /api/v1/admin/ops/request-data-cache
func (h *OpsHandler) UpdateRequestDataCache(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	var req service.RequestDataCacheSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	updated, err := h.opsService.UpdateRequestDataCacheSettings(c.Request.Context(), &req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	view, ok := h.loadRequestDataCacheView(c, updated)
	if !ok {
		return
	}
	response.Success(c, view)
}

// ClearRequestDataCache 手动清除缓存并返回删除条数与最新统计。
// POST /api/v1/admin/ops/request-data-cache/clear
func (h *OpsHandler) ClearRequestDataCache(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	var req requestDataCacheClearRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	deleted, err := h.opsService.ClearRequestDataCache(c.Request.Context(), req.Kind)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	view, ok := h.loadRequestDataCacheView(c, nil)
	if !ok {
		return
	}
	response.Success(c, gin.H{"deleted": deleted, "settings": view.Settings, "stats": view.Stats})
}
