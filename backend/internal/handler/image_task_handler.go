package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	pkghttputil "github.com/TokenFlux/TokenRouter/internal/pkg/httputil"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	middleware2 "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type AsyncImageHandler struct {
	tasks       *service.ImageTaskService
	openAI      *OpenAIGatewayHandler
	execute     func(platform string, c *gin.Context)
	observer    service.MediaTaskObserver
	pendingOnce sync.Once
	pending     chan struct{}
}

const asyncImageMaxPendingTasks = 64

// acquirePending 限制单进程已受理任务数，额度覆盖排队、生成和结果转存整个生命周期。
func (h *AsyncImageHandler) acquirePending() (func(), bool) {
	h.pendingOnce.Do(func() {
		if h.pending == nil {
			h.pending = make(chan struct{}, asyncImageMaxPendingTasks)
		}
	})
	select {
	case h.pending <- struct{}{}:
		return func() { <-h.pending }, true
	default:
		return nil, false
	}
}

func NewAsyncImageHandler(tasks *service.ImageTaskService, openAI *OpenAIGatewayHandler) *AsyncImageHandler {
	h := &AsyncImageHandler{tasks: tasks, openAI: openAI}
	h.execute = h.executeWithGateway
	return h
}

// SetMediaTaskObserver 注入统一任务投影，任务正文和图片只保留在原执行链路。
func (h *AsyncImageHandler) SetMediaTaskObserver(observer service.MediaTaskObserver) {
	h.observer = observer
}

// SetGatewayExecutor 在路由装配后复用整个同步网关，保留既有路由、审核和错误日志。
func (h *AsyncImageHandler) SetGatewayExecutor(execute func(string, *gin.Context)) {
	h.execute = execute
}

const asyncImageOriginalRequestKey = "async_image_original_request"

// asyncImageCapturedBody 只记录正常鉴权和处理实际读取的字节，不提前读取未认证的大请求。
type asyncImageCapturedBody struct {
	io.ReadCloser
	buffer bytes.Buffer
}

func (b *asyncImageCapturedBody) Read(data []byte) (int, error) {
	n, err := b.ReadCloser.Read(data)
	if n > 0 {
		_, _ = b.buffer.Write(data[:n])
	}
	return n, err
}

// CaptureRequest 在模型重写前安装惰性快照，后台重新走认证时仍使用客户端原模型。
func (h *AsyncImageHandler) CaptureRequest(c *gin.Context) {
	if c.Request.Method != http.MethodPost || !(strings.HasSuffix(c.Request.URL.Path, "/images/generations/async") || strings.HasSuffix(c.Request.URL.Path, "/images/edits/async")) {
		c.Next()
		return
	}
	original := c.Request.Clone(c.Request.Context())
	captured := &asyncImageCapturedBody{ReadCloser: c.Request.Body}
	original.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(captured.buffer.Bytes())), nil }
	c.Request.Body = captured
	c.Set(asyncImageOriginalRequestKey, original)
	c.Next()
}
func (h *AsyncImageHandler) enabled() bool {
	return h != nil && h.tasks != nil && h.tasks.Enabled()
}

func (h *AsyncImageHandler) pollable() bool {
	return h != nil && h.tasks != nil && h.tasks.Pollable()
}

func (h *AsyncImageHandler) Submit(c *gin.Context) {
	if !h.enabled() {
		imageTaskJSONError(c, http.StatusNotFound, "not_found_error", "async image tasks are not enabled")
		return
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.UserID <= 0 || apiKey.ID <= 0 {
		imageTaskError(c, service.ErrImageTaskForbidden)
		return
	}
	platform := ""
	if apiKey.Group != nil {
		platform = apiKey.Group.Platform
	}
	if platform != service.PlatformOpenAI && platform != service.PlatformGrok {
		imageTaskJSONError(c, http.StatusNotFound, "not_found_error", "Images API is not supported for this platform")
		return
	}
	if !service.GroupAllowsImageGeneration(apiKey.Group) {
		imageTaskJSONError(c, http.StatusForbidden, "permission_error", service.ImageGenerationPermissionMessage())
		return
	}
	if h == nil || h.tasks == nil || h.execute == nil {
		imageTaskError(c, service.ErrImageTaskUnavailable)
		return
	}
	release, acquired := h.acquirePending()
	if !acquired {
		c.Header("Retry-After", "3")
		imageTaskJSONError(c, http.StatusServiceUnavailable, "image_task_busy", "too many pending image tasks; retry later")
		return
	}
	started := false
	defer func() {
		if !started {
			release()
		}
	}()

	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			imageTaskJSONError(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		imageTaskJSONError(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		imageTaskJSONError(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}
	if asyncImageRequestStreams(c.GetHeader("Content-Type"), body) {
		imageTaskJSONError(c, http.StatusBadRequest, "invalid_request_error", "streaming image requests cannot be submitted as asynchronous tasks")
		return
	}
	if err := h.validateRequest(c, platform, body); err != nil {
		imageTaskJSONError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	uploader, enabled := h.tasks.ExecutionStorage()
	if !enabled {
		imageTaskJSONError(c, http.StatusNotFound, "not_found_error", "async image tasks are not enabled")
		return
	}
	taskCtx, recorder, cancel := newAsyncImageContext(c, body, h.tasks.ExecutionTimeout())
	observation := service.MediaTaskObservation{Source: "async_image", MediaType: "image", Platform: platform, Status: "processing", UserID: apiKey.UserID, APIKeyID: apiKey.ID, GroupID: apiKey.GroupID}
	observation.RequestID, _ = taskCtx.Request.Context().Value(ctxkey.ClientRequestID).(string)
	if observation.RequestID != "" {
		// 与普通网关 resolveUsageBillingRequestID 的幂等标识保持一致，列表只读关联实际扣费。
		observation.RequestID = "client:" + observation.RequestID
	}
	if platform == service.PlatformGrok {
		observation.Model = service.ParseGrokMediaRequest(c.GetHeader("Content-Type"), body).Model
	} else if h.openAI != nil && h.openAI.gatewayService != nil {
		if parsed, err := h.openAI.gatewayService.ParseOpenAIImagesRequestForRouting(c, body); err == nil {
			observation.Model = parsed.Model
		}
	}
	task, err := h.tasks.CreateObserved(c.Request.Context(), service.ImageTaskOwner{UserID: apiKey.UserID, APIKeyID: apiKey.ID}, &observation)
	if err != nil {
		cancel()
		imageTaskError(c, err)
		return
	}
	expiresAt := time.Unix(task.ExpiresAt, 0)
	observation.TaskID, observation.CreatedAt, observation.ExpiresAt = task.ID, time.Unix(task.CreatedAt, 0), &expiresAt
	if h.observer != nil && !h.tasks.PersistsMediaTasks() {
		if err := h.observer.ObserveMediaTask(c.Request.Context(), observation); err != nil {
			cancel()
			h.failTask(task.ID, http.StatusServiceUnavailable, imageTaskErrorPayload("api_error", "task index unavailable"))
			imageTaskError(c, service.ErrImageTaskUnavailable)
			return
		}
	}
	pollURL := imageTaskPollURL(c.Request.URL.Path, task.ID)
	c.Header("Cache-Control", "no-store")
	c.Header("Location", pollURL)
	c.Header("Retry-After", "3")
	c.JSON(http.StatusAccepted, gin.H{
		"id":         task.ID,
		"task_id":    task.TaskID,
		"object":     task.Object,
		"status":     task.Status,
		"created_at": task.CreatedAt,
		"expires_at": task.ExpiresAt,
		"poll_url":   pollURL,
	})

	started = true
	go h.run(task.ID, platform, taskCtx, recorder, cancel, uploader, observation, release)
}

func (h *AsyncImageHandler) Get(c *gin.Context) {
	if !h.pollable() {
		imageTaskJSONError(c, http.StatusNotFound, "not_found_error", "async image tasks are not enabled")
		return
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.UserID <= 0 || apiKey.ID <= 0 {
		imageTaskError(c, service.ErrImageTaskForbidden)
		return
	}
	task, err := h.tasks.Get(c.Request.Context(), service.ImageTaskOwner{UserID: apiKey.UserID, APIKeyID: apiKey.ID}, c.Param("task_id"))
	if err != nil {
		imageTaskError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	if task.Status == service.ImageTaskStatusProcessing {
		c.Header("Retry-After", "3")
	}
	c.JSON(http.StatusOK, task)
}

func (h *AsyncImageHandler) validateRequest(c *gin.Context, platform string, body []byte) error {
	if err := validateAsyncImagePrompt(c.GetHeader("Content-Type"), body); err != nil {
		return err
	}
	if h.openAI == nil || h.openAI.gatewayService == nil {
		return nil
	}
	if platform == service.PlatformGrok {
		parsed := service.ParseGrokMediaRequest(c.GetHeader("Content-Type"), body)
		if strings.TrimSpace(parsed.Model) == "" {
			return errors.New("model is required")
		}
		return nil
	}
	parsed, err := h.openAI.gatewayService.ParseOpenAIImagesRequestForRouting(c, body)
	if err != nil {
		return err
	}
	if parsed.Stream {
		return errors.New("streaming image requests cannot be submitted as asynchronous tasks")
	}
	return nil
}

func (h *AsyncImageHandler) executeWithGateway(platform string, c *gin.Context) {
	if h.openAI == nil {
		imageTaskJSONError(c, http.StatusServiceUnavailable, "api_error", "image gateway is unavailable")
		return
	}
	if platform == service.PlatformGrok {
		h.openAI.GrokImages(c)
		return
	}
	h.openAI.Images(c)
}

func (h *AsyncImageHandler) run(taskID, platform string, taskCtx *gin.Context, recorder *httptest.ResponseRecorder, cancel context.CancelFunc, uploader *service.ImageResultUploader, observation service.MediaTaskObservation, release func()) {
	stopHeartbeat := h.tasks.KeepAlive(taskID)
	defer stopHeartbeat()
	if release != nil {
		defer release()
	}
	defer cancel()
	// 终态从实际任务记录读取，包含对象转存失败，避免将已扣费但未存图的任务误报成功。
	defer h.observeFinishedTask(observation)
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.L().Error("image_task.execution_panicked", zap.String("task_id", taskID), zap.Any("panic", recovered))
			h.failTask(taskID, http.StatusInternalServerError, imageTaskErrorPayload("api_error", "image generation task panicked"))
		}
	}()

	h.execute(platform, taskCtx)
	body := bytes.TrimSpace(recorder.Body.Bytes())
	if err := taskCtx.Request.Context().Err(); err != nil && len(body) == 0 {
		h.failTask(taskID, http.StatusGatewayTimeout, imageTaskErrorPayload("timeout_error", "image generation task timed out"))
		return
	}
	statusCode := recorder.Code
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	if statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices {
		var envelope struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil && len(envelope.Error) > 0 && string(envelope.Error) != "null" {
			h.failTask(taskID, http.StatusBadGateway, envelope.Error)
			return
		}
		if len(body) == 0 || !json.Valid(body) {
			h.failTask(taskID, http.StatusBadGateway, imageTaskErrorPayload("api_error", "upstream returned an invalid image response"))
			return
		}
		storageCtx, storageCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer storageCancel()
		if err := h.tasks.CompleteWithUploader(storageCtx, taskID, statusCode, json.RawMessage(body), uploader); err != nil {
			logger.L().Error("image_task.complete_store_failed", zap.String("task_id", taskID), zap.Error(err))
		}
		return
	}
	h.failTask(taskID, statusCode, extractImageTaskError(body))
}

func (h *AsyncImageHandler) failTask(taskID string, statusCode int, taskErr json.RawMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := h.tasks.Fail(ctx, taskID, statusCode, taskErr); err != nil {
		logger.L().Error("image_task.failure_store_failed", zap.String("task_id", taskID), zap.Error(err))
	}
}

func newAsyncImageContext(c *gin.Context, body []byte, timeoutDuration time.Duration) (*gin.Context, *httptest.ResponseRecorder, context.CancelFunc) {
	source := c.Request
	if value, ok := c.Get(asyncImageOriginalRequestKey); ok {
		if original, ok := value.(*http.Request); ok {
			source = original
			if original.GetBody != nil {
				reader, err := original.GetBody()
				if err == nil {
					body, _ = io.ReadAll(reader)
					_ = reader.Close()
				}
			}
		}
	}
	base := context.WithoutCancel(source.Context())
	executionCtx, cancel := context.WithTimeout(service.WithAsyncImageExecutionContext(base), timeoutDuration)
	request := source.Clone(executionCtx)
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	request.ContentLength = int64(len(body))
	request.URL.Path = strings.TrimSuffix(request.URL.Path, "/async")

	taskCtx := c.Copy()
	recorder := httptest.NewRecorder()
	recorderCtx, _ := gin.CreateTestContext(recorder)
	taskCtx.Writer = recorderCtx.Writer
	taskCtx.Request = request
	return taskCtx, recorder, cancel
}

func asyncImageRequestStreams(contentType string, body []byte) bool {
	if isMultipartImagesContentType(contentType) {
		// Grok 与 OpenAI 共用提交门禁，表单请求同样不允许流式生成。
		_, params, err := mime.ParseMediaType(contentType)
		if err != nil || params["boundary"] == "" {
			return false
		}
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, err := reader.NextPart()
			if err != nil {
				break
			}
			if part.FormName() == "stream" && part.FileName() == "" {
				value, _ := io.ReadAll(io.LimitReader(part, 16))
				flag := strings.TrimSpace(string(value))
				if strings.EqualFold(flag, "true") || flag == "1" {
					_ = part.Close()
					return true
				}
			}
			_ = part.Close()
		}
		return false
	}
	var envelope struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(body, &envelope) == nil && envelope.Stream
}

func imageTaskPollURL(submitPath, taskID string) string {
	if strings.HasPrefix(submitPath, "/v1/") {
		return "/v1/images/tasks/" + taskID
	}
	return "/images/tasks/" + taskID
}

func extractImageTaskError(body []byte) json.RawMessage {
	if json.Valid(body) {
		var envelope struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil && len(envelope.Error) > 0 && json.Valid(envelope.Error) {
			return envelope.Error
		}
		return json.RawMessage(body)
	}
	return imageTaskErrorPayload("api_error", "image generation failed")
}

func imageTaskErrorPayload(errorType, message string) json.RawMessage {
	data, _ := json.Marshal(gin.H{"type": errorType, "message": message})
	return data
}

func imageTaskError(c *gin.Context, err error) {
	status := infraerrors.Code(err)
	code := infraerrors.Reason(err)
	message := infraerrors.Message(err)
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if strings.TrimSpace(code) == "" {
		code = "IMAGE_TASK_ERROR"
	}
	imageTaskJSONError(c, status, code, message)
}

func imageTaskJSONError(c *gin.Context, status int, code, message string) {
	c.Header("Cache-Control", "no-store")
	c.JSON(status, gin.H{"error": gin.H{"type": code, "code": code, "message": message}})
}

// observeFinishedTask 仅同步脱敏的状态，列表失败不回滚已经完成的真实计费。
func (h *AsyncImageHandler) observeFinishedTask(observation service.MediaTaskObservation) {
	if h.observer == nil || h.tasks.PersistsMediaTasks() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	task, err := h.tasks.Get(ctx, service.ImageTaskOwner{UserID: observation.UserID, APIKeyID: observation.APIKeyID}, observation.TaskID)
	if err != nil {
		logger.L().Warn("image_task.observation_read_failed", zap.String("task_id", observation.TaskID), zap.Error(err))
		return
	}
	observation.Status = task.Status
	observation.HTTPStatus = task.HTTPStatus
	if task.CompletedAt != nil {
		completed := time.Unix(*task.CompletedAt, 0)
		observation.CompletedAt = &completed
	}
	expires := time.Unix(task.ExpiresAt, 0)
	observation.ExpiresAt = &expires
	if len(task.Error) > 0 {
		var envelope struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		}
		if json.Unmarshal(task.Error, &envelope) == nil {
			// 原始上游错误可能回显提示词，只由短期任务接口返回；长期任务列表保存固定分类。
			observation.ErrorMessage = "图片生成失败，请根据请求 ID 查看错误日志"
			if strings.Contains(envelope.Message, "image was generated but object storage failed") {
				observation.ErrorMessage = "图片生成完成但结果存储失败，生成用量可能已经计费"
			} else if envelope.Type == "timeout_error" {
				observation.ErrorMessage = "图片生成任务超时"
			}
		}
	}
	if err := h.observer.ObserveMediaTask(ctx, observation); err != nil {
		logger.L().Warn("image_task.observation_write_failed", zap.String("task_id", observation.TaskID), zap.Error(err))
	}
}
