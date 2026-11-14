package handler

import (
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	middleware2 "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// GetRequestData 返回当前用户某条使用记录缓存的请求端点信息。
// GET /api/v1/usage/:id/request-data
func (h *UsageHandler) GetRequestData(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	usageID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || usageID <= 0 {
		response.BadRequest(c, "Invalid usage ID")
		return
	}
	if h.opsService == nil || h.usageService == nil {
		response.NotFound(c, "Request data cache not found")
		return
	}
	record, err := h.usageService.GetByID(c.Request.Context(), usageID)
	// 非本人记录与不存在统一返回 404，不泄露记录是否存在。
	if err != nil || record == nil || record.UserID != subject.UserID {
		response.NotFound(c, "Request data cache not found")
		return
	}
	entry, err := h.opsService.GetUserRequestDataCache(c.Request.Context(), subject.UserID, record.RequestID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, entry)
}

// GetErrorRequestData 返回当前用户某条错误请求缓存的请求端点信息。
// GET /api/v1/usage/errors/:id/request-data
func (h *UsageHandler) GetErrorRequestData(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	errorID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || errorID <= 0 {
		response.BadRequest(c, "Invalid error ID")
		return
	}
	if h.opsService == nil {
		response.NotFound(c, "Request data cache not found")
		return
	}
	entry, err := h.opsService.GetUserErrorRequestDataCache(c.Request.Context(), subject.UserID, errorID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, entry)
}
