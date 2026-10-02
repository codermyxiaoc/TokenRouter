package service

import (
	"context"
	"net/http"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

var (
	ErrVideoTaskNotFound = infraerrors.NotFound("VIDEO_TASK_NOT_FOUND", "视频任务不存在")
	ErrVideoTaskConflict = infraerrors.New(http.StatusConflict, "VIDEO_TASK_CONFLICT", "视频任务状态或幂等参数冲突")
	ErrVideoTaskBusy     = infraerrors.New(http.StatusTooManyRequests, "VIDEO_TASK_LIMIT", "视频账号正在处理的任务已达到上限")
	ErrVideoTaskBudget   = infraerrors.BadRequest("VIDEO_TASK_BUDGET_REQUIRED", "请为视频账号配置有效的任务预留上限")
	ErrVideoTaskPricing  = infraerrors.BadRequest("VIDEO_PRICING_UNAVAILABLE", "未配置当前模型或分辨率的视频价格")
	ErrVideoTaskMode     = infraerrors.BadRequest("VIDEO_STANDARD_MODE_REQUIRED", "独立视频任务需要 standard 运行模式")
)

// VideoTaskRecord 不保存上游凭据和提交正文；未知受理任务不能据此重放生成。
// @project-doc docs/domains/video_tasks.md#video_task_lifecycle
type VideoTaskRecord struct {
	ID             string
	UserID         int64
	APIKeyID       int64
	GroupID        int64
	AccountID      int64
	IdempotencyKey string
	PayloadHash    string
	Status         string
	BillingStatus  string
	Target         VideoUpstreamTarget
	Metadata       VideoRequestMetadata
	RequestedModel string
	InternalModel  string
	Native         bool
	InboundPath    string
	UpstreamTaskID string
	// 响应以字节保存，非 JSON 的 502 页面也不能破坏状态持久化。
	RawResponse           []byte
	CreateResponse        []byte
	CreateResponseStatus  int
	PricingMismatch       bool
	ResponseStatus        int
	UpstreamStatus        string
	VideoURL              string
	ErrorMessage          string
	Quote                 *VideoPriceQuote
	Hold                  BatchImageBalanceHoldCommand
	BillingResult         *BatchImageBalanceHoldResult
	EffectsDone           bool
	AccountRateMultiplier float64
	CreatedAt             time.Time
	CompletedAt           *time.Time
	NextPollAt            time.Time
	PollAttempts          int
	LeaseToken            string
	LeaseUntil            time.Time
}

// VideoTaskRepository 的租约仅保护短期状态推进，资金事务另外使用任务行锁互斥。
type VideoTaskRepository interface {
	Create(context.Context, *VideoTaskRecord, int) (*VideoTaskRecord, bool, error)
	Get(context.Context, string) (*VideoTaskRecord, error)
	Find(context.Context, int64, int64, string, string) (*VideoTaskRecord, error)
	FindIdempotent(context.Context, int64, string) (*VideoTaskRecord, error)
	Claim(context.Context, string, time.Duration) (*VideoTaskRecord, error)
	ClaimReady(context.Context, int, time.Duration) ([]*VideoTaskRecord, error)
	Save(context.Context, *VideoTaskRecord, bool) error
}

// VideoBillingDetails 仅暴露用户需要核对的价格事实，不包含内部账号或资金凭据。
type VideoBillingDetails struct {
	Status            string  `json:"status"`
	Mode              string  `json:"mode"`
	Resolution        string  `json:"resolution"`
	HasReferenceVideo bool    `json:"has_reference_video"`
	UnitPrice         float64 `json:"unit_price"`
	Unit              string  `json:"unit"`
	Tokens            *int64  `json:"tokens,omitempty"`
	DurationSeconds   float64 `json:"duration_seconds"`
	ReservedAmount    float64 `json:"reserved_amount"`
	// 后结算模式的零预留不代表免费，实际费用需等待可靠用量及结算结果。
	DeferredBilling bool `json:"deferred_billing,omitempty"`
	// 预扣秒价和时长取创建快照，与最终 Token 单价及生成时长分开展示。
	TokenPrepay           bool     `json:"token_prepay,omitempty"`
	PrepayPricePerSecond  *float64 `json:"prepay_price_per_second,omitempty"`
	PrepayDurationSeconds float64  `json:"prepay_duration_seconds,omitempty"`
	ActualAmount          *float64 `json:"actual_amount,omitempty"`
	PricingSource         string   `json:"pricing_source"`
	// 指针保留显式零；历史未配置附加费的任务不显示这些字段。
	ReferenceImageCount         *int     `json:"reference_image_count,omitempty"`
	BillableReferenceImageCount *int     `json:"billable_reference_image_count,omitempty"`
	ReferenceImageFreeCount     *int     `json:"reference_image_free_count,omitempty"`
	ReferenceImageUnitPrice     *float64 `json:"reference_image_unit_price,omitempty"`
	ReferenceImageCost          *float64 `json:"reference_image_cost,omitempty"`
}

// VideoTaskTransport 允许离线协议样本测试完整状态机而不调用付费上游。
type VideoTaskTransport interface {
	SelectAccount(context.Context, *APIKey, VideoTaskSubmitRequest) (*VideoUpstreamSelection, error)
	Submit(context.Context, *VideoUpstreamSelection) (*VideoUpstreamResponse, error)
	Poll(context.Context, *Account, VideoUpstreamTarget, string) (*VideoUpstreamResponse, error)
	Cancel(context.Context, *Account, VideoUpstreamTarget, string) (*VideoUpstreamResponse, error)
}
