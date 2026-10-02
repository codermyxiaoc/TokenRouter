package handler

import (
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	pkghttputil "github.com/TokenFlux/TokenRouter/internal/pkg/httputil"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// VideoHandler 仅处理原生HTTP边界；任务服务负责原子归属和资金状态。
type VideoHandler struct {
	lifecycle service.VideoTaskLifecycle
	gateway   *OpenAIGatewayHandler
}

func NewVideoHandler(lifecycle service.VideoTaskLifecycle) *VideoHandler {
	return &VideoHandler{lifecycle: lifecycle}
}

func (h *VideoHandler) Handle(c *gin.Context) { h.handle(c, nil) }

// Seedance 保留旧OpenAI任务路径；复合Key只在新任务确实不存在时回到旧归属查询。
func (h *VideoHandler) Seedance(c *gin.Context, legacy gin.HandlerFunc) { h.handle(c, legacy) }

// OpenAIVideos 复用共享 URL；只有本站任务不存在时才交回旧 Grok 查询链路。
func (h *VideoHandler) OpenAIVideos(c *gin.Context, legacy gin.HandlerFunc) { h.handle(c, legacy) }

func (h *VideoHandler) handle(c *gin.Context, legacy gin.HandlerFunc) {
	key, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || key == nil {
		writeVideoError(c, infraerrors.Unauthorized("VIDEO_UNAUTHORIZED", "Invalid API key"))
		return
	}
	route, ok := service.MatchVideoGatewayRoute(c.Request.Method, c.Request.URL.Path)
	if !ok {
		writeVideoError(c, infraerrors.NotFound("VIDEO_ROUTE_NOT_FOUND", "Video route not found"))
		return
	}
	if legacy != nil && route.Protocol != "openai_videos" && key.Group != nil && key.Group.Platform != service.PlatformVideo {
		legacy(c)
		return
	}
	if h == nil || h.lifecycle == nil {
		if legacy != nil && (route.Protocol != "openai_videos" || key.Group == nil || key.Group.Platform != service.PlatformVideo) {
			legacy(c)
			return
		}
		writeVideoError(c, infraerrors.ServiceUnavailable("VIDEO_UNAVAILABLE", "Video gateway is unavailable"))
		return
	}
	c.Request = c.Request.WithContext(service.WithVideoUpstreamObserver(c.Request.Context(), func(observation service.VideoUpstreamObservation) {
		if c.GetString(opsModelKey) == "" {
			setOpsRequestContext(c, observation.Model, false)
		}
		setOpsSelectedAccount(c, observation.AccountID, service.PlatformVideo)
		service.SetOpsUpstreamModel(c, observation.Model)
		c.Set(service.OpsActualUpstreamEndpointKey, observation.Endpoint)
		if observation.SlotAcquired {
			service.MarkOpsAccountSlotAcquired(c)
		}
		if observation.StatusCode >= 400 {
			service.SetOpsUpstreamError(c, observation.StatusCode, observation.ErrorMessage, "")
		}
	}))
	var result *service.VideoTaskResponse
	var err error
	if route.Create {
		mediaType, _, mediaErr := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if mediaErr != nil || mediaType != "application/json" {
			writeVideoError(c, infraerrors.New(http.StatusUnsupportedMediaType, "VIDEO_CONTENT_TYPE", "Video requires application/json"))
			return
		}
		body, readErr := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
		if readErr != nil {
			if maxErr, ok := extractMaxBytesError(readErr); ok {
				writeVideoError(c, infraerrors.New(http.StatusRequestEntityTooLarge, "VIDEO_BODY_TOO_LARGE", buildBodyTooLargeMessage(maxErr.Limit)))
				return
			}
			writeVideoError(c, infraerrors.BadRequest("VIDEO_INVALID_REQUEST", "Unable to read video request"))
			return
		}
		release, allowed := h.admitVideo(c, key, body, route.Model)
		if !allowed {
			return
		}
		if release != nil {
			defer release()
		}
		result, err = h.lifecycle.Submit(c.Request.Context(), key, service.VideoTaskSubmitRequest{Body: body, ContentType: mediaType, InboundProtocol: route.Protocol, ModelPath: route.Model, NativePath: route.PathTemplate, Native: route.Native, IdempotencyKey: c.GetHeader("Idempotency-Key")})
	} else {
		id := route.TaskID
		if route.Protocol == "kling" {
			values := c.QueryArray("task_ids")
			if len(values) != 1 || strings.Contains(values[0], ",") {
				writeVideoError(c, infraerrors.BadRequest("VIDEO_TASK_ID_REQUIRED", "A single task_ids value is required"))
				return
			}
			id = strings.TrimSpace(values[0])
		}
		if id == "" {
			writeVideoError(c, infraerrors.BadRequest("VIDEO_TASK_ID_REQUIRED", "task ID is required"))
			return
		}
		if route.Content {
			err = h.writeVideoContent(c, key, id, route.Protocol)
			if err != nil {
				if legacy != nil && infraerrors.Code(err) == http.StatusNotFound && (key.Group == nil || key.Group.Platform != service.PlatformVideo) {
					legacy(c)
					return
				}
				writeVideoError(c, err)
			}
			return
		}
		if c.Request.Method == http.MethodDelete {
			result, err = h.lifecycle.Cancel(c.Request.Context(), key, id, route.Protocol)
		} else {
			result, err = h.lifecycle.Query(c.Request.Context(), key, id, route.Protocol)
		}
		if legacy != nil && infraerrors.Code(err) == http.StatusNotFound &&
			(route.Protocol != "openai_videos" || key.Group == nil || key.Group.Platform != service.PlatformVideo) {
			legacy(c)
			return
		}
	}
	if err != nil {
		writeVideoError(c, err)
		return
	}
	if result == nil {
		writeVideoError(c, infraerrors.ServiceUnavailable("VIDEO_UNAVAILABLE", "Video response is unavailable"))
		return
	}
	for name, values := range result.Header {
		for _, value := range values {
			c.Writer.Header().Add(name, value)
		}
	}
	// 原生任务响应可能含私有签名URL，不能被上游缓存头变成共享缓存。
	c.Header("Cache-Control", "no-store")
	if result.LocalTaskID != "" {
		c.Header("X-Video-Task-ID", result.LocalTaskID)
	}
	status := result.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	contentType := result.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(status, contentType, result.Body)
}

// 内容仅流式转发服务层校验后的媒体，禁止缓冲整段视频或透传上游认证及缓存头。
func (h *VideoHandler) writeVideoContent(c *gin.Context, key *service.APIKey, id, protocol string) error {
	content, ok := h.lifecycle.(service.VideoTaskContentLifecycle)
	if !ok {
		return infraerrors.ServiceUnavailable("VIDEO_CONTENT_UNAVAILABLE", "Video content is unavailable")
	}
	// 上游超时只限制读取；额外限制下游写入，避免慢客户端长期占用下载连接和并发槽。
	controller := http.NewResponseController(c.Writer)
	if err := controller.SetWriteDeadline(time.Now().Add(2 * time.Minute)); err == nil {
		defer controller.SetWriteDeadline(time.Time{})
	}
	result, err := content.OpenContent(c.Request.Context(), key, id, protocol, c.GetHeader("Range"))
	if err != nil {
		return err
	}
	if result == nil || result.Body == nil {
		return infraerrors.ServiceUnavailable("VIDEO_CONTENT_UNAVAILABLE", "Video content is unavailable")
	}
	defer result.Body.Close()
	c.Header("Cache-Control", "private, no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; sandbox")
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
		if value := result.Header.Get(name); value != "" {
			c.Header(name, value)
		}
	}
	c.Header("Content-Disposition", `inline; filename="`+mediaTaskPreviewFilename(result.Header.Get("Content-Type"))+`"`)
	if result.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		c.Header("Content-Length", "0")
		c.Status(result.StatusCode)
		return nil
	}
	c.Status(result.StatusCode)
	_, _ = io.Copy(c.Writer, result.Body)
	return nil
}

func writeVideoError(c *gin.Context, err error) {
	c.Header("Cache-Control", "no-store")
	status := infraerrors.Code(err)
	if status < 400 || status > 599 {
		status = http.StatusInternalServerError
	}
	c.JSON(status, gin.H{"error": gin.H{"type": "video_error", "code": infraerrors.Reason(err), "message": infraerrors.Message(err)}})
}
