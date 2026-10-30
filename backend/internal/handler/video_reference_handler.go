package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

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
	// 上游图片读取不需要 Range：带范围的请求只按参考视频票据处理，图片票据不会因此被消耗读取次数。
	if rangeHeader := c.GetHeader("Range"); rangeHeader != "" {
		if !h.serveAsset(c, rangeHeader) {
			response.ErrorFrom(c, service.ErrVideoReferenceNotFound)
		}
		return
	}
	content, err := h.service.Open(c.Request.Context(), c.Param("ticket"), c.Request.Method)
	if err != nil {
		// 图片票据不存在时再按参考视频票据读取；两类票据互不重叠，未命中的查询不消耗任何读取次数。
		if h.serveAsset(c, "") {
			return
		}
		response.ErrorFrom(c, err)
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

// serveAsset 输出参考视频：从私有存储流式读取，只支持单段 Range（视频播放器与上游下载器常用），
// 多段 Range 会被拒绝；每次 GET/HEAD 都消耗一次读取次数。返回 false 表示不是视频票据。
func (h *VideoReferenceHandler) serveAsset(c *gin.Context, rangeHeader string) bool {
	if strings.Contains(rangeHeader, ",") {
		response.ErrorFrom(c, service.ErrVideoReferenceNotFound)
		return true
	}
	content, err := h.service.OpenAsset(c.Request.Context(), c.Param("ticket"), c.Request.Method)
	if err != nil {
		return false
	}
	defer content.Reader.Close()
	c.Header("Content-Type", content.MIMEType)
	c.Header("Content-Disposition", `inline; filename="reference-video"`)
	// ServeContent 负责单段 Range、416 与 HEAD；不设置修改时间，避免条件请求绕过读取计数语义。
	http.ServeContent(c.Writer, c.Request, "", time.Time{}, content.Reader)
	return true
}
