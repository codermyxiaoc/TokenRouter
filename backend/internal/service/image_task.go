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
	// ErrImageTaskIdempotencyConflict 表示同一幂等键已用于内容不同的请求。
	ErrImageTaskIdempotencyConflict = infraerrors.New(http.StatusConflict, "IMAGE_TASK_IDEMPOTENCY_CONFLICT", "Idempotency-Key was already used with a different image request")
	// ErrImageTaskDuplicate 是仓储在幂等键已被占用时返回的内部错误，服务据此改为取回原任务。
	ErrImageTaskDuplicate = errors.New("image task idempotency key already exists")
)

// ImageTaskIdempotencyMaxKeyLength 是幂等键的最大长度，与数据库列宽一致。
const ImageTaskIdempotencyMaxKeyLength = 128

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
	// PendingResult 是已转存、等待扣费确认的紧凑结果，任务仍为 processing 且不对外返回；
	// BillingRequestID 是对应资金动作的请求 ID。执行者中断时维护器据账本决定交付结果或判为中断。
	PendingResult     json.RawMessage `json:"pending_result,omitempty"`
	PendingHTTPStatus int             `json:"pending_http_status,omitempty"`
	BillingRequestID  string          `json:"billing_request_id,omitempty"`
	// IdempotencyKey 与 PayloadHash 记录创建时的幂等键和请求指纹，只用于重放判定，不对外返回。
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	PayloadHash    string `json:"payload_hash,omitempty"`
}

// ImageTaskIdempotency 是一次带幂等键的创建：Key 来自 Idempotency-Key，PayloadHash 是接口与请求内容的指纹。
type ImageTaskIdempotency struct {
	Key         string
	PayloadHash string
}

// ImageTaskIdempotentStore 按 API Key 与幂等键查询未过期任务；没有时返回 ErrImageTaskNotFound。
type ImageTaskIdempotentStore interface {
	FindIdempotent(ctx context.Context, apiKeyID int64, key string) (*ImageTaskRecord, error)
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
	imageBilling     ImageBillingReservationRepository
	// objectRetention 独立清理已上传对象，任务记录到期不影响删除凭据。
	objectRetention *ImageObjectRetention
	// billingRecovery 仅重试已有安全证据的账务动作，不能调用生成。
	billingRecovery func(context.Context) error
	// deliveryRetryDelays 是转存临时失败的重试档位，生产装配时设置。
	deliveryRetryDelays []time.Duration
	// chargeRecorder 只读确认扣费是否已提交，用于扣费报错时区分“未扣费”与“已扣费但回执丢失”。
	chargeRecorder UsageBillingRequestRecorder
}

// SetBillingRecovery 在启动维护循环前装配已确认图片的结算补偿。
func (s *ImageTaskService) SetBillingRecovery(recover func(context.Context) error) {
	if s != nil {
		s.billingRecovery = recover
	}
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

// SupportsIdempotency 表示任务仓储能按幂等键查询；不支持时调用方必须拒绝带幂等键的请求，不能静默忽略。
func (s *ImageTaskService) SupportsIdempotency() bool {
	if s == nil || s.store == nil {
		return false
	}
	_, ok := s.store.(ImageTaskIdempotentStore)
	return ok
}

// ReplayIdempotent 只查询同一 Key 下的原任务，不创建、不生成、不扣费。找到且指纹一致返回原任务；
// 指纹或归属不一致返回冲突；没有记录时 found=false。
// @project-doc docs/domains/media_tasks.md#async_image_idempotency
func (s *ImageTaskService) ReplayIdempotent(ctx context.Context, owner ImageTaskOwner, idem ImageTaskIdempotency) (*ImageTask, bool, error) {
	if s == nil || s.store == nil {
		return nil, false, ErrImageTaskUnavailable
	}
	store, ok := s.store.(ImageTaskIdempotentStore)
	if !ok {
		return nil, false, ErrImageTaskUnavailable
	}
	if idem.Key == "" {
		return nil, false, nil
	}
	task, err := store.FindIdempotent(ctx, owner.APIKeyID, idem.Key)
	if errors.Is(err, ErrImageTaskNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, ErrImageTaskUnavailable.WithCause(err)
	}
	// 滚动升级期间旧版本重写记录会丢掉指纹；指纹为空时该幂等键仍只属于这一个任务，按重放处理而不误报冲突。
	if task.UserID != owner.UserID || task.APIKeyID != owner.APIKeyID || (task.PayloadHash != "" && task.PayloadHash != idem.PayloadHash) {
		return nil, false, ErrImageTaskIdempotencyConflict
	}
	return imageTaskToPublic(task), true, nil
}

// CreateObservedIdempotent 带幂等键创建任务；并发的同键请求由仓储唯一约束裁决，落败方取回胜出的任务（replayed=true）。
func (s *ImageTaskService) CreateObservedIdempotent(ctx context.Context, owner ImageTaskOwner, observation *MediaTaskObservation, idem ImageTaskIdempotency) (*ImageTask, bool, error) {
	task, err := s.createObserved(ctx, owner, observation, idem)
	if !errors.Is(err, ErrImageTaskDuplicate) {
		return task, false, err
	}
	replay, found, replayErr := s.ReplayIdempotent(ctx, owner, idem)
	if replayErr != nil {
		return nil, false, replayErr
	}
	if !found {
		return nil, false, ErrImageTaskUnavailable
	}
	return replay, true, nil
}

// CreateObserved 在受理前持久化任务及列表快照，保证二者共用同一执行标识。
func (s *ImageTaskService) CreateObserved(ctx context.Context, owner ImageTaskOwner, observation *MediaTaskObservation) (*ImageTask, error) {
	return s.createObserved(ctx, owner, observation, ImageTaskIdempotency{})
}

func (s *ImageTaskService) createObserved(ctx context.Context, owner ImageTaskOwner, observation *MediaTaskObservation, idem ImageTaskIdempotency) (*ImageTask, error) {
	if s == nil || s.store == nil {
		return nil, ErrImageTaskUnavailable
	}
	now := time.Now().UTC()
	task := &ImageTaskRecord{
		ID:             "imgtask_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		UserID:         owner.UserID,
		APIKeyID:       owner.APIKeyID,
		Status:         ImageTaskStatusProcessing,
		CreatedAt:      now.Unix(),
		ExpiresAt:      now.Add(s.ttl).Unix(),
		ExecutionID:    s.runtime.executionID,
		LeaseExpiresAt: now.Add(imageTaskLeaseDuration).Unix(),
		// 截止时间额外包含转存重试窗口，避免维护循环在“已生成、等待保存”期间误判中断。
		ExecutionDeadline: now.Add(s.executionTimeout + 3*time.Minute + s.deliveryRetryWindow()).Unix(),
		IdempotencyKey:    idem.Key,
		PayloadHash:       idem.PayloadHash,
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
		if errors.Is(err, ErrImageTaskDuplicate) {
			return nil, ErrImageTaskDuplicate
		}
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
	// 维护器较晚补交时，结果中的签名链接可能已先于对象过期；按当前存储重签，期限不超过任务记录有效期。
	if task.Status == ImageTaskStatusCompleted && len(task.Result) > 0 {
		if uploader, _ := s.current(); uploader != nil {
			task.Result = uploader.RefreshExpiredURLs(ctx, task.ID, task.Result, time.Unix(task.ExpiresAt, 0))
		}
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
	_, err := s.CompleteDelivered(ctx, id, statusCode, result, uploader, nil)
	return err
}

// ErrImageTaskBillingUnavailable 表示图片已转存但扣费无法完成；任务按未交付失败处理，不向用户返回结果。
var ErrImageTaskBillingUnavailable = errors.New("image task billing could not be settled")

// DefaultImageDeliveryRetryDelays 是转存临时失败后的等待档位；同一对象键幂等覆盖，不会重新生成。
var DefaultImageDeliveryRetryDelays = []time.Duration{15 * time.Second, time.Minute, 3 * time.Minute}

// SetDeliveryRetryDelays 配置转存临时失败的重试档位；为空时立即判定失败（单元测试默认）。
func (s *ImageTaskService) SetDeliveryRetryDelays(delays []time.Duration) {
	if s != nil {
		s.deliveryRetryDelays = append([]time.Duration(nil), delays...)
	}
}

// DeliveryBudget 返回转存与重试所需的总预算，供调用方设置独立于生成的存储截止时间。
func (s *ImageTaskService) DeliveryBudget() time.Duration {
	return 2*time.Minute + s.deliveryRetryWindow()
}

// deliveryRetryWindow 是重试档位带来的额外时间，每档等待后再给一次完整上传预算。
func (s *ImageTaskService) deliveryRetryWindow() time.Duration {
	var window time.Duration
	if s != nil {
		for _, delay := range s.deliveryRetryDelays {
			window += delay + 2*time.Minute
		}
		if len(s.deliveryRetryDelays) > 0 {
			// 生产装配还包含交付前扣费与扣费后结果落库的重试预算。
			window += 5*time.Minute + 11*time.Minute
		}
	}
	return window
}

// CompleteDelivered 与 CompleteWithUploader 相同，额外返回图片是否已真正交付（转存成功、扣费成功且终态已落库）。
// settle 在转存成功之后、标记完成之前执行（通常是捕获预占），保证“任务完成 ⇔ 已扣费”：
// settle 失败时任务按未交付失败处理，不返回结果链接，由调用方释放预占。
// 转存临时失败会在 ctx 预算内按档位重试，仍失败才判定未交付。
// @project-doc docs/domains/media_tasks.md#async_image_billing
func (s *ImageTaskService) CompleteDelivered(ctx context.Context, id string, statusCode int, result json.RawMessage, uploader *ImageResultUploader, settle func(context.Context) error) (bool, error) {
	return s.CompleteDeliveredWithBilling(ctx, id, statusCode, result, uploader, "", settle)
}

// CompleteDeliveredWithBilling 在扣费前先把待交付结果与扣费请求 ID 落库（仍为 processing、不对外返回），
// 保存失败则不扣费。这样扣费成功后进程退出或结果写入持续失败，维护器也能按账本补交结果；
// 扣费报错时先核对账本，已扣费仍交付结果，避免“钱扣了、图没给”或误报未扣费。
// billingRequestID 为空时退回只在内存中重试的旧流程。
// @project-doc docs/domains/media_tasks.md#async_image_billing
func (s *ImageTaskService) CompleteDeliveredWithBilling(ctx context.Context, id string, statusCode int, result json.RawMessage, uploader *ImageResultUploader, billingRequestID string, settle func(context.Context) error) (bool, error) {
	if !json.Valid(result) {
		return false, s.Fail(ctx, id, http.StatusBadGateway, imageTaskErrorJSON("api_error", "upstream returned a non-JSON image response"))
	}
	var response struct {
		Data []struct {
			URL string `json:"url"`
			B64 string `json:"b64_json"`
		} `json:"data"`
	}
	if json.Unmarshal(result, &response) != nil || len(response.Data) == 0 {
		return false, s.Fail(ctx, id, http.StatusBadGateway, imageTaskErrorJSON("api_error", "upstream returned no generated images"))
	}
	for _, item := range response.Data {
		if strings.TrimSpace(item.URL) == "" && strings.TrimSpace(item.B64) == "" {
			return false, s.Fail(ctx, id, http.StatusBadGateway, imageTaskErrorJSON("api_error", "upstream returned an invalid image item"))
		}
	}
	if uploader != nil {
		rewritten, err := s.rewriteWithRetry(ctx, id, result, uploader)
		if err != nil {
			// 转存失败不回退存 base64，避免大 blob 撑爆 Redis：直接把任务标记为失败。
			stage, code, message := ImageStorageFailureDetails(err)
			logger.L().Error("image_task.offload_failed", zap.String("task_id", id), zap.String("stage", stage), zap.String("code", code))
			// 对象存储超时后仍给终态写入独立预算，避免任务永远停在处理中。
			failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			taskErr, _ := json.Marshal(map[string]string{"type": "api_error", "code": code, "message": message})
			return false, s.Fail(failureCtx, id, http.StatusBadGateway, taskErr)
		}
		result = rewritten
	}
	// 转存后只允许紧凑 JSON，未知扩展字段也不能把大 Base64 间接写进 Redis。
	if len(result) > maxImageTaskStoredResultBytes {
		return false, s.Fail(ctx, id, http.StatusBadGateway, imageTaskErrorJSON("api_error", "stored image result exceeds metadata limit"))
	}
	billingRequestID = strings.TrimSpace(billingRequestID)
	if settle != nil && billingRequestID != "" {
		// 先落库待交付结果再扣费：保存失败说明没有可恢复的记录，此时不扣费，按未交付失败处理。
		if err := s.stagePendingDelivery(ctx, id, statusCode, result, billingRequestID); err != nil {
			logger.L().Error("image_task.pending_delivery_store_failed", zap.String("task_id", id), zap.Error(err))
			failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			taskErr, _ := json.Marshal(map[string]string{"type": "api_error", "code": "image_billing_unavailable",
				"message": "image was generated but the result could not be saved; the result was not delivered and no charge was applied"})
			return false, errors.Join(ErrImageTaskBillingUnavailable, s.Fail(failureCtx, id, http.StatusServiceUnavailable, taskErr))
		}
	}
	if settle != nil {
		// 扣费使用独立预算：转存可能已耗尽 ctx，不能因此把已产出的图片当作扣费失败。
		settleCtx, settleCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		settleErr := settle(settleCtx)
		settleCancel()
		if settleErr != nil {
			logger.L().Error("image_task.billing_settle_failed", zap.String("task_id", id), zap.Error(settleErr))
			// 扣费可能已提交但回执丢失：账本确认已扣费时照常交付，不能把已收费的图片判为失败。
			charged, checkErr := s.chargeRecorded(ctx, id, billingRequestID)
			if charged {
				logger.L().Warn("image_task.billing_settle_error_but_captured", zap.String("task_id", id))
				return s.persistDeliveredResult(ctx, id, statusCode, result)
			}
			if checkErr != nil && billingRequestID != "" {
				// 账本暂时无法确认且待交付结果已落库：不判失败，任务保持 processing，由租约到期后的维护器
				// 按账本决定交付或中断。若此处判失败会清掉待交付结果，扣费已提交时用户将再也拿不到图片。
				logger.L().Error("image_task.billing_settle_unconfirmed", zap.String("task_id", id), zap.Error(checkErr))
				return false, ErrImageTaskBillingUnavailable
			}
			message := "image was generated but billing could not be completed; the result was not delivered and no charge was applied"
			if checkErr != nil {
				// 无法确认账本时不能断言未扣费；已扣费的预占不会再被释放，可凭任务 ID 核对。
				message = "image was generated but billing could not be confirmed; the result was not delivered. If a charge appears, contact support with the task ID"
			}
			failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			taskErr, _ := json.Marshal(map[string]string{"type": "api_error", "code": "image_billing_unavailable", "message": message})
			return false, errors.Join(ErrImageTaskBillingUnavailable, s.Fail(failureCtx, id, http.StatusServiceUnavailable, taskErr))
		}
	}
	// 上传可能耗尽自己的预算，结果入库使用独立预算，避免图片已保存却丢失终态。
	if settle != nil {
		// 已扣费：结果必须交到用户手里。扣费不可撤销，因此按递增间隔持续重试落库（最长约 10 分钟），
		// 期间任务保持 processing 且租约由执行者续期，用户轮询最终能取得结果。
		return s.persistDeliveredResult(ctx, id, statusCode, result)
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := s.finish(finishCtx, id, ImageTaskStatusCompleted, statusCode, result, nil); err != nil {
		return false, err
	}
	return true, nil
}

// stagePendingDelivery 把待交付结果写入仍处于 processing 的任务，并读回确认写入生效；
// 任务已被维护器判为终态或执行者不符时返回错误，调用方不得继续扣费。
func (s *ImageTaskService) stagePendingDelivery(ctx context.Context, id string, statusCode int, result json.RawMessage, billingRequestID string) error {
	if s == nil || s.store == nil {
		return ErrImageTaskUnavailable
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
		}
		stageCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		lastErr = s.stagePendingDeliveryOnce(stageCtx, id, statusCode, result, billingRequestID)
		cancel()
		if lastErr == nil || errors.Is(lastErr, ErrImageTaskNotFound) || errors.Is(lastErr, errImageTaskNotPending) {
			return lastErr
		}
	}
	return lastErr
}

// errImageTaskNotPending 表示任务已不在执行中（例如已被判为中断），不能再登记待交付结果。
var errImageTaskNotPending = errors.New("image task is no longer processing")

func (s *ImageTaskService) stagePendingDeliveryOnce(ctx context.Context, id string, statusCode int, result json.RawMessage, billingRequestID string) error {
	task, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if task.Status != ImageTaskStatusProcessing {
		return errImageTaskNotPending
	}
	task.PendingResult, task.PendingHTTPStatus, task.BillingRequestID = result, statusCode, billingRequestID
	if err := s.store.Save(ctx, task, s.ttl); err != nil {
		return err
	}
	stored, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if stored.Status != ImageTaskStatusProcessing || stored.BillingRequestID != billingRequestID || len(stored.PendingResult) == 0 {
		return errImageTaskNotPending
	}
	return nil
}

// chargeRecorded 按扣费请求 ID 与任务所属 Key 只读核对账本；没有请求 ID 或查询能力时返回未扣费且无错误。
func (s *ImageTaskService) chargeRecorded(ctx context.Context, id, billingRequestID string) (bool, error) {
	if s == nil || s.chargeRecorder == nil || billingRequestID == "" {
		return false, nil
	}
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	task, err := s.store.Get(checkCtx, id)
	if err != nil {
		return false, err
	}
	return s.chargeRecorder.UsageBillingRequestRecorded(checkCtx, billingRequestID, task.APIKeyID)
}

// deliveredResultPersistDelays 是扣费成功后结果落库的重试间隔；数据库或 Redis 的短暂故障通常在此窗口内恢复。
var deliveredResultPersistDelays = []time.Duration{0, time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 2 * time.Minute, 2 * time.Minute, 2 * time.Minute}

// persistDeliveredResult 在已扣费后反复保存完成结果；全部失败时记录可人工补交的错误日志（含任务 ID），
// 对象仍在存储桶中，不撤销已发生的扣费，也不改写为失败（改写同样需要可用的存储）。
func (s *ImageTaskService) persistDeliveredResult(ctx context.Context, id string, statusCode int, result json.RawMessage) (bool, error) {
	var lastErr error
	for attempt, delay := range deliveredResultPersistDelays {
		if delay > 0 {
			time.Sleep(delay)
		}
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		lastErr = s.finish(finishCtx, id, ImageTaskStatusCompleted, statusCode, result, nil)
		cancel()
		if lastErr == nil {
			return true, nil
		}
		if errors.Is(lastErr, ErrImageTaskNotFound) {
			break
		}
		logger.L().Warn("image_task.delivered_result_persist_retry", zap.String("task_id", id), zap.Int("attempt", attempt+1), zap.Error(lastErr))
	}
	// 结果 JSON 含签名链接，日志只记任务 ID；对象键以任务 ID 为前缀，可据此在存储中找回并人工补交。
	logger.L().Error("image_task.delivered_result_persist_failed_after_capture", zap.String("task_id", id), zap.Error(lastErr))
	return false, lastErr
}

// rewriteWithRetry 只对上传、超时等临时故障按档位重试；图片内容非法或过大立即失败。
func (s *ImageTaskService) rewriteWithRetry(ctx context.Context, id string, result json.RawMessage, uploader *ImageResultUploader) (json.RawMessage, error) {
	for attempt := 0; ; attempt++ {
		rewritten, err := uploader.Rewrite(ctx, id, result)
		if err == nil {
			return rewritten, nil
		}
		if attempt >= len(s.deliveryRetryDelays) || !imageDeliveryRetryable(err) {
			return nil, err
		}
		_, code, _ := ImageStorageFailureDetails(err)
		logger.L().Warn("image_task.offload_retry_scheduled", zap.String("task_id", id), zap.String("code", code), zap.Int("attempt", attempt+1))
		timer := time.NewTimer(s.deliveryRetryDelays[attempt])
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, err
		case <-timer.C:
		}
	}
}

// imageDeliveryRetryable 只放行存储侧临时故障；解码失败、内容非法、过大等固定输入错误不重试。
func imageDeliveryRetryable(err error) bool {
	stage, code, _ := ImageStorageFailureDetails(err)
	switch code {
	case imageResultInvalid, imageResultTooLarge, imageStorageAccessDenied:
		return false
	}
	return stage == "upload" || stage == "download" || stage == "result" || code == imageStorageTimeout || code == imageStorageUploadFailed
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
	// 终态不再保留待交付副本，避免记录翻倍；扣费请求 ID 保留供核对。
	task.PendingResult, task.PendingHTTPStatus = nil, 0
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
