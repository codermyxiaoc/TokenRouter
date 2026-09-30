package handler

import (
	"io"
	"net/http"
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// MediaTaskHandler 只查询元数据和缓存媒体；播放只读文件，不查询模型状态或执行结算。
type MediaTaskHandler struct{ service *service.MediaTaskService }

func NewMediaTaskHandler(s *service.MediaTaskService) *MediaTaskHandler {
	return &MediaTaskHandler{service: s}
}

func mediaTaskActor(c *gin.Context, admin bool) (service.MediaTaskActor, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return service.MediaTaskActor{}, false
	}
	if admin {
		role, _ := middleware.GetUserRoleFromContext(c)
		if role != "admin" {
			response.Forbidden(c, "Administrator access required")
			return service.MediaTaskActor{}, false
		}
	}
	return service.MediaTaskActor{UserID: subject.UserID, IsAdmin: admin}, true
}

func (h *MediaTaskHandler) List(c *gin.Context)         { h.list(c, false) }
func (h *MediaTaskHandler) AdminList(c *gin.Context)    { h.list(c, true) }
func (h *MediaTaskHandler) Get(c *gin.Context)          { h.get(c, false) }
func (h *MediaTaskHandler) AdminGet(c *gin.Context)     { h.get(c, true) }
func (h *MediaTaskHandler) Models(c *gin.Context)       { h.models(c, false) }
func (h *MediaTaskHandler) AdminModels(c *gin.Context)  { h.models(c, true) }
func (h *MediaTaskHandler) Preview(c *gin.Context)      { h.preview(c, false) }
func (h *MediaTaskHandler) AdminPreview(c *gin.Context) { h.preview(c, true) }

// models 返回完整可见集合的选项，分页和模型自身不参与筛选。
func (h *MediaTaskHandler) models(c *gin.Context, admin bool) {
	actor, ok := mediaTaskActor(c, admin)
	if !ok {
		return
	}
	userID, err := strconv.ParseInt(c.DefaultQuery("user_id", "0"), 10, 64)
	if admin && err != nil {
		response.BadRequest(c, "Invalid user identifier")
		return
	}
	result, err := h.service.ListModels(c.Request.Context(), actor, service.MediaTaskFilter{
		UserID: userID, MediaType: c.Query("media_type"), Source: c.Query("source"), Status: c.Query("status"),
	})
	if !response.ErrorFrom(c, err) {
		c.Header("Cache-Control", "no-store")
		response.Success(c, result)
	}
}

// preview 使用面板身份读取结果，客户端无需提供生成任务的 API Key。
func (h *MediaTaskHandler) preview(c *gin.Context, admin bool) {
	actor, ok := mediaTaskActor(c, admin)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid media task identifier")
		return
	}
	result, err := h.service.Preview(c.Request.Context(), actor, id)
	if !response.ErrorFrom(c, err) {
		c.Header("Cache-Control", "no-store")
		response.Success(c, result)
	}
}

// PreviewContent 用短期任务专属票据支持原生 video/Range，不向浏览器或媒体站点发送任何账户凭据。
func (h *MediaTaskHandler) PreviewContent(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; sandbox")
	result, err := h.service.OpenPreviewContent(c.Request.Context(), c.Param("ticket"), c.Request.Method, c.GetHeader("Range"))
	if response.ErrorFrom(c, err) {
		return
	}
	defer result.Body.Close()
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
		if value := result.Header.Get(name); value != "" {
			c.Header(name, value)
		}
	}
	c.Header("Content-Disposition", `inline; filename="`+mediaTaskPreviewFilename(result.Header.Get("Content-Type"))+`"`)
	if result.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		// 不透传供应商错误正文，防止错误响应混入上游地址或其他敏感内容。
		c.Header("Content-Length", "0")
		c.Status(result.StatusCode)
		return
	}
	c.Status(result.StatusCode)
	if c.Request.Method != http.MethodHead {
		_, _ = io.Copy(c.Writer, result.Body)
	}
}

// 后缀只由服务层已经验证的媒体类型决定，不能让下载文件的扩展名与实际容器不符。
func mediaTaskPreviewFilename(contentType string) string {
	switch contentType {
	case "video/webm":
		return "preview.webm"
	case "video/ogg":
		return "preview.ogv"
	case "video/quicktime":
		return "preview.mov"
	default:
		return "preview.mp4"
	}
}

func (h *MediaTaskHandler) list(c *gin.Context, admin bool) {
	actor, ok := mediaTaskActor(c, admin)
	if !ok {
		return
	}
	page, errPage := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, errSize := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	userID, errUser := strconv.ParseInt(c.DefaultQuery("user_id", "0"), 10, 64)
	modelExact, errModelExact := strconv.ParseBool(c.DefaultQuery("model_exact", "false"))
	if errPage != nil || errSize != nil || errModelExact != nil || (admin && errUser != nil) {
		response.BadRequest(c, "Invalid pagination or user identifier")
		return
	}
	result, err := h.service.List(c.Request.Context(), actor, service.MediaTaskFilter{
		Page: page, PageSize: size, UserID: userID, MediaType: c.Query("media_type"),
		Source: c.Query("source"), Status: c.Query("status"), Model: c.Query("model"), ModelExact: modelExact,
	})
	if !response.ErrorFrom(c, err) {
		c.Header("Cache-Control", "no-store")
		response.Success(c, result)
	}
}

func (h *MediaTaskHandler) get(c *gin.Context, admin bool) {
	actor, ok := mediaTaskActor(c, admin)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid media task identifier")
		return
	}
	result, err := h.service.Get(c.Request.Context(), actor, id)
	if !response.ErrorFrom(c, err) {
		c.Header("Cache-Control", "no-store")
		response.Success(c, result)
	}
}
