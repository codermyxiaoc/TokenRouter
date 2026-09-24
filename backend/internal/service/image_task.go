package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	ImageTaskStatusProcessing = "processing"
	ImageTaskStatusCompleted  = "completed"
	ImageTaskStatusFailed     = "failed"

	defaultImageTaskTTL              = 24 * time.Hour
	defaultImageTaskExecutionTimeout = 30 * time.Minute
	maxImageTaskStoredResultBytes    = 1 << 20
)

type asyncImageExecutionContextKey struct{}

// WithAsyncImageExecutionContext 标记已脱离客户端的后台上下文，上游不可再移除它的执行超时。
func WithAsyncImageExecutionContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, asyncImageExecutionContextKey{}, true)
}

var (
	ErrImageTaskNotFound    = infraerrors.New(http.StatusNotFound, "IMAGE_TASK_NOT_FOUND", "image task not found")
	ErrImageTaskForbidden   = infraerrors.New(http.StatusForbidden, "IMAGE_TASK_FORBIDDEN", "image task does not belong to this API key")
	ErrImageTaskUnavailable = infraerrors.New(http.StatusServiceUnavailable, "IMAGE_TASK_UNAVAILABLE", "image task storage is unavailable")
)

type ImageTaskRecord struct {
	ID          string          `json:"id"`
	UserID      int64           `json:"user_id"`
	APIKeyID    int64           `json:"api_key_id"`
	Status      string          `json:"status"`
	HTTPStatus  int             `json:"http_status,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       json.RawMessage `json:"error,omitempty"`
	CreatedAt   int64           `json:"created_at"`
	CompletedAt *int64          `json:"completed_at,omitempty"`
	ExpiresAt   int64           `json:"expires_at"`
	// 执行租约与列表快照仅供内部持久化使用，不包含请求正文或 API Key。
	ExecutionID       string                `json:"execution_id,omitempty"`
	LeaseExpiresAt    int64                 `json:"lease_expires_at,omitempty"`
	ExecutionDeadline int64                 `json:"execution_deadline,omitempty"`
	Observation       *MediaTaskObservation `json:"observation,omitempty"`
}

type ImageTask struct {
	ID          string          `json:"id"`
	TaskID      string          `json:"task_id"`
	Object      string          `json:"object"`
	Status      string          `json:"status"`
	HTTPStatus  int             `json:"http_status,omitempty"`
	ImageURL    string          `json:"image_url,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       json.RawMessage `json:"error,omitempty"`
	CreatedAt   int64           `json:"created_at"`
	CompletedAt *int64          `json:"completed_at,omitempty"`
	ExpiresAt   int64           `json:"expires_at"`
}

type ImageTaskOwner struct {
	UserID   int64
	APIKeyID int64
}

type ImageTaskStore interface {
	Save(ctx context.Context, task *ImageTaskRecord, ttl time.Duration) error
	Get(ctx context.Context, id string) (*ImageTaskRecord, error)
}

// ImageTaskDurableStore 负责持久终态、租约恢复和列表投影，不重新调用上游或执行扣费。
// @project-doc docs/domains/media_tasks.md#async_image_lifecycle
type ImageTaskDurableStore interface {
	ImageTaskStore
	Heartbeat(ctx context.Context, id, executionID string, leaseExpiresAt int64) error
	Maintain(ctx context.Context, now time.Time) error
}

type ImageStorageResolver func() (uploader *ImageResultUploader, enabled bool)

type ImageTaskService struct {
	store            ImageTaskStore
	uploader         *ImageResultUploader
	enabled          bool
	resolve          ImageStorageResolver
	ttl              time.Duration
	executionTimeout time.Duration
	runtime          *imageTaskRuntime
}

func NewImageTaskService(store ImageTaskStore) *ImageTaskService {
	return NewImageTaskServiceWithOptions(store, defaultImageTaskTTL, defaultImageTaskExecutionTimeout)
}

func NewImageTaskServiceWithOptions(store ImageTaskStore, ttl, executionTimeout time.Duration) *ImageTaskService {
	if ttl <= 0 {
		ttl = defaultImageTaskTTL
	}
	if executionTimeout <= 0 {
		executionTimeout = defaultImageTaskExecutionTimeout
	}
	return &ImageTaskService{store: store, ttl: ttl, executionTimeout: executionTimeout, runtime: newImageTaskRuntime()}
}

// NewImageTaskServiceWithUploader 构造一个已启用的图片任务服务：结果会先经 uploader
// 转存到对象存储再写紧凑任务结果。uploader 为 nil 时不做转存（仅用于测试）。
func NewImageTaskServiceWithUploader(store ImageTaskStore, uploader *ImageResultUploader, ttl, executionTimeout time.Duration) *ImageTaskService {
	s := NewImageTaskServiceWithOptions(store, ttl, executionTimeout)
	s.uploader = uploader
	s.enabled = true
	return s
}

// NewImageTaskServiceWithResolver 构造一个由 resolver 决定启用状态的服务：
// 开关与凭证来自后台设置，保存后立即生效，无需重启。
func NewImageTaskServiceWithResolver(store ImageTaskStore, resolve ImageStorageResolver, ttl, executionTimeout time.Duration) *ImageTaskService {
	s := NewImageTaskServiceWithOptions(store, ttl, executionTimeout)
	s.resolve = resolve
	return s
}

// current 返回当前生效的 uploader 与启用状态。
// 注入了 resolver 时以 resolver 为准（后台设置可热切换），否则回落到构造时固定的值。
func (s *ImageTaskService) current() (*ImageResultUploader, bool) {
	if s == nil {
		return nil, false
	}
	if s.resolve != nil {
		return s.resolve()
	}
	return s.uploader, s.enabled
}

// Enabled 表示异步图片任务功能是否可用（总开关 + 凭证齐全）。
// 关闭时 handler 直接返回 404，不创建任务、不写存储。
func (s *ImageTaskService) Enabled() bool {
	if s == nil || s.store == nil {
		return false
	}
	_, enabled := s.current()
	return enabled
}

// Pollable 表示已创建的任务能否被查询。
// 比 Enabled 弱：只要 store 可用即可，从而在功能被关掉后仍能取回进行中的任务结果。
func (s *ImageTaskService) Pollable() bool {
	return s != nil && s.store != nil
}

// PersistsMediaTasks 表示仓储已在同一事务内维护列表，接口层不再另写可能滞后的投影。
func (s *ImageTaskService) PersistsMediaTasks() bool {
	if s == nil {
		return false
	}
	_, ok := s.store.(ImageTaskDurableStore)
	return ok
}

func (s *ImageTaskService) ExecutionTimeout() time.Duration {
	if s == nil || s.executionTimeout <= 0 {
		return defaultImageTaskExecutionTimeout
	}
	return s.executionTimeout
}

func (s *ImageTaskService) Create(ctx context.Context, owner ImageTaskOwner) (*ImageTask, error) {
	return s.CreateObserved(ctx, owner, nil)
}

// CreateObserved 在受理前持久化任务及列表快照，保证二者共用同一执行标识。
func (s *ImageTaskService) CreateObserved(ctx context.Context, owner ImageTaskOwner, observation *MediaTaskObservation) (*ImageTask, error) {
	if s == nil || s.store == nil {
		return nil, ErrImageTaskUnavailable
	}
	now := time.Now().UTC()
	task := &ImageTaskRecord{
		ID:                "imgtask_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		UserID:            owner.UserID,
		APIKeyID:          owner.APIKeyID,
		Status:            ImageTaskStatusProcessing,
		CreatedAt:         now.Unix(),
		ExpiresAt:         now.Add(s.ttl).Unix(),
		ExecutionID:       s.runtime.executionID,
		LeaseExpiresAt:    now.Add(imageTaskLeaseDuration).Unix(),
		ExecutionDeadline: now.Add(s.executionTimeout + 3*time.Minute).Unix(),
	}
	if observation != nil {
		copy := *observation
		copy.Source, copy.TaskID, copy.MediaType = "async_image", task.ID, "image"
		copy.UserID, copy.APIKeyID, copy.Status = owner.UserID, owner.APIKeyID, task.Status
		copy.CreatedAt = now
		expires := time.Unix(task.ExpiresAt, 0)
		copy.ExpiresAt = &expires
		task.Observation = &copy
	}
	if err := s.store.Save(ctx, task, s.ttl); err != nil {
		return nil, ErrImageTaskUnavailable.WithCause(err)
	}
	return imageTaskToPublic(task), nil
}

func (s *ImageTaskService) Get(ctx context.Context, owner ImageTaskOwner, id string) (*ImageTask, error) {
	if s == nil || s.store == nil {
		return nil, ErrImageTaskUnavailable
	}
	task, err := s.store.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		if errors.Is(err, ErrImageTaskNotFound) {
			return nil, ErrImageTaskNotFound
		}
		return nil, ErrImageTaskUnavailable.WithCause(err)
	}
	if task.UserID != owner.UserID || task.APIKeyID != owner.APIKeyID {
		return nil, ErrImageTaskNotFound
	}
	return imageTaskToPublic(task), nil
}

// ExecutionStorage 固定本任务接受时的存储配置，后台关闭开关不会让大图片退回 Redis。
func (s *ImageTaskService) ExecutionStorage() (*ImageResultUploader, bool) { return s.current() }
func (s *ImageTaskService) Complete(ctx context.Context, id string, statusCode int, result json.RawMessage) error {
	uploader, _ := s.current()
	return s.CompleteWithUploader(ctx, id, statusCode, result, uploader)
}

// CompleteWithUploader 使用提交时的对象存储快照，设置变更只影响新任务。
func (s *ImageTaskService) CompleteWithUploader(ctx context.Context, id string, statusCode int, result json.RawMessage, uploader *ImageResultUploader) error {
	if !json.Valid(result) {
		return s.Fail(ctx, id, http.StatusBadGateway, imageTaskErrorJSON("api_error", "upstream returned a non-JSON image response"))
	}
	var response struct {
		Data []struct {
			URL string `json:"url"`
			B64 string `json:"b64_json"`
		} `json:"data"`
	}
	if json.Unmarshal(result, &response) != nil || len(response.Data) == 0 {
		return s.Fail(ctx, id, http.StatusBadGateway, imageTaskErrorJSON("api_error", "upstream returned no generated images"))
	}
	for _, item := range response.Data {
		if strings.TrimSpace(item.URL) == "" && strings.TrimSpace(item.B64) == "" {
			return s.Fail(ctx, id, http.StatusBadGateway, imageTaskErrorJSON("api_error", "upstream returned an invalid image item"))
		}
	}
	if uploader != nil {
		rewritten, err := uploader.Rewrite(ctx, id, result)
		if err != nil {
			// 转存失败不回退存 base64，避免大 blob 撑爆 Redis：直接把任务标记为失败。
			logger.L().Error("image_task.offload_failed", zap.String("task_id", id), zap.Error(err))
			// 对象存储超时后仍给终态写入独立预算，避免任务永远停在处理中。
			failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			return s.Fail(failureCtx, id, http.StatusBadGateway, imageTaskErrorJSON("api_error", "image was generated but object storage failed; generation usage may already have been billed"))
		}
		result = rewritten
	}
	// 转存后只允许紧凑 JSON，未知扩展字段也不能把大 Base64 间接写进 Redis。
	if len(result) > maxImageTaskStoredResultBytes {
		return s.Fail(ctx, id, http.StatusBadGateway, imageTaskErrorJSON("api_error", "stored image result exceeds metadata limit"))
	}
	// 上传可能耗尽自己的预算，结果入库使用独立预算，避免图片已保存却丢失终态。
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	return s.finish(finishCtx, id, ImageTaskStatusCompleted, statusCode, result, nil)
}

func (s *ImageTaskService) Fail(ctx context.Context, id string, statusCode int, taskErr json.RawMessage) error {
	if !json.Valid(taskErr) || len(taskErr) > maxImageTaskStoredResultBytes {
		taskErr = imageTaskErrorJSON("api_error", "image generation failed")
	}
	return s.finish(ctx, id, ImageTaskStatusFailed, statusCode, nil, taskErr)
}

func (s *ImageTaskService) finish(ctx context.Context, id, status string, statusCode int, result, taskErr json.RawMessage) error {
	if s == nil || s.store == nil {
		return ErrImageTaskUnavailable
	}
	// 只重试保存现有结果，绝不重新进入生成或结算链路。
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * 200 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ErrImageTaskUnavailable.WithCause(ctx.Err())
			case <-timer.C:
			}
		}
		lastErr = s.finishOnce(ctx, id, status, statusCode, result, taskErr)
		if lastErr == nil || errors.Is(lastErr, ErrImageTaskNotFound) {
			return lastErr
		}
	}
	return lastErr
}

// finishOnce 的权威状态由仓储原子更新，迟到的旧执行不能覆盖新的终态。
func (s *ImageTaskService) finishOnce(ctx context.Context, id, status string, statusCode int, result, taskErr json.RawMessage) error {
	task, err := s.store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrImageTaskNotFound) {
			return ErrImageTaskNotFound
		}
		return ErrImageTaskUnavailable.WithCause(err)
	}
	now := time.Now().UTC()
	completedAt := now.Unix()
	task.Status = status
	task.HTTPStatus = statusCode
	task.Result = result
	task.Error = taskErr
	task.CompletedAt = &completedAt
	task.ExpiresAt = now.Add(s.ttl).Unix()
	if err := s.store.Save(ctx, task, s.ttl); err != nil {
		return ErrImageTaskUnavailable.WithCause(err)
	}
	return nil
}

func imageTaskToPublic(task *ImageTaskRecord) *ImageTask {
	if task == nil {
		return nil
	}
	return &ImageTask{
		ID:          task.ID,
		TaskID:      task.ID,
		Object:      "image.generation.task",
		Status:      task.Status,
		HTTPStatus:  task.HTTPStatus,
		ImageURL:    firstImageTaskURL(task.Result),
		Result:      task.Result,
		Error:       task.Error,
		CreatedAt:   task.CreatedAt,
		CompletedAt: task.CompletedAt,
		ExpiresAt:   task.ExpiresAt,
	}
}

func firstImageTaskURL(result json.RawMessage) string {
	if len(result) == 0 || !json.Valid(result) {
		return ""
	}
	var response struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if json.Unmarshal(result, &response) != nil || len(response.Data) == 0 {
		return ""
	}
	return strings.TrimSpace(response.Data[0].URL)
}

func imageTaskErrorJSON(errorType, message string) json.RawMessage {
	data, _ := json.Marshal(map[string]string{"type": errorType, "message": message})
	return data
}
