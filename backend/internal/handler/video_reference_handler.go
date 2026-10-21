package handler

import (
	"net/http"
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// VideoReferenceHandler 只有票据读取入口；签发只能由已鉴权的生成服务在校验原素材后调用。
type VideoReferenceHandler struct {
	service *service.VideoReferenceService
}

func NewVideoReferenceHandler(s *service.VideoReferenceService) *VideoReferenceHandler {
	return &VideoReferenceHandler{service: s}
}

func (h *VideoReferenceHandler) Content(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; sandbox")
	// 上游图片读取不需要 Range；拒绝非预期范围，避免无限切片消耗访问配额。
	if c.GetHeader("Range") != "" {
		response.ErrorFrom(c, service.ErrVideoReferenceNotFound)
		return
	}
	content, err := h.service.Open(c.Request.Context(), c.Param("ticket"), c.Request.Method)
	if response.ErrorFrom(c, err) {
		return
	}
	c.Header("Content-Type", content.MIMEType)
	c.Header("Content-Length", strconv.FormatInt(content.Size, 10))
	c.Header("Content-Disposition", `inline; filename="reference-image"`)
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		return
	}
	c.Data(http.StatusOK, content.MIMEType, content.Body)
}
