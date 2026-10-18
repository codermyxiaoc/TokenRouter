package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

// 云画布保存用户文档和七天私有素材，任务仍交给现有图片与视频账本。
var (
	ErrCanvasNotFound = infraerrors.New(http.StatusNotFound, "CANVAS_NOT_FOUND", "画布或素材不存在")
	ErrCanvasConflict = infraerrors.New(http.StatusConflict, "CANVAS_REVISION_CONFLICT", "画布已被其他窗口修改，请重新载入")
	ErrCanvasInvalid  = infraerrors.New(http.StatusBadRequest, "CANVAS_INVALID_REQUEST", "画布请求参数不合法")
	ErrCanvasExpired  = infraerrors.New(http.StatusGone, "CANVAS_ASSET_EXPIRED", "素材已超过七天保留期限")
	ErrCanvasQuota    = infraerrors.New(http.StatusRequestEntityTooLarge, "CANVAS_STORAGE_LIMIT", "画布素材超过大小或个人存储限制")
	// 在途任务预留导致的临时准入不足与实际素材占满必须分开，避免误导用户删除或压缩素材。
	ErrCanvasExecutionLimit = infraerrors.New(http.StatusTooManyRequests, "CANVAS_EXECUTION_LIMIT", "当前生成任务较多，请等待已有任务完成后重试；本次未提交生成或预扣费用")
	ErrCanvasUnavailable    = infraerrors.New(http.StatusServiceUnavailable, "CANVAS_UNAVAILABLE", "画布存储暂时不可用")
)

const (
	CreativeCanvasAssetTTL            = 7 * 24 * time.Hour
	CreativeCanvasUploadLimit   int64 = 64 << 20
	CreativeCanvasOutputLimit   int64 = 512 << 20
	CreativeCanvasUserQuota     int64 = 2 << 30
	CreativeCanvasDocumentLimit       = 4 << 20
)

// CreativeCanvas 的 revision 仅保护文档，不参与任务或资金状态。
type CreativeCanvas struct {
	ID        string          `json:"id"`
	UserID    int64           `json:"-"`
	Title     string          `json:"title"`
	Document  json.RawMessage `json:"document"`
	Revision  int64           `json:"revision"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type CreativeCanvasAsset struct {
	ID          string    `json:"id"`
	UserID      int64     `json:"-"`
	CanvasID    string    `json:"canvas_id"`
	ExecutionID string    `json:"execution_id,omitempty"`
	OutputIndex int       `json:"output_index,omitempty"`
	MediaType   string    `json:"media_type"`
	MIMEType    string    `json:"mime_type"`
	ByteSize    int64     `json:"byte_size"`
	SHA256      string    `json:"-"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type CreativeCanvasReference struct {
	AssetID string `json:"asset_id"`
	Role    string `json:"role,omitempty"`
}

// 参数只含受控模型能力，禁止浏览器传供应商地址、密钥或可执行脚本。
type CreativeCanvasExecutionParams struct {
	// 文本模型仅开放通用输出预算，费用由真实 Token 用量产生。
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
	// TTS 采用官方音色、可播放容器与语速；不接收任意上游脚本。
	Voice       string  `json:"voice,omitempty"`
	AudioFormat string  `json:"audio_format,omitempty"`
	Speed       float64 `json:"speed,omitempty"`
	// 批次字段仅由服务端写入，用于按原单次合同恢复各子任务；客户端不得指定。
	BatchID          string                    `json:"batch_id,omitempty"`
	BatchIndex       int                       `json:"batch_index,omitempty"`
	BatchSize        int                       `json:"batch_size,omitempty"`
	BatchOutputCount int                       `json:"batch_output_count,omitempty"`
	GroupID          int64                     `json:"group_id"`
	Model            string                    `json:"model"`
	Operation        string                    `json:"operation"`
	Prompt           string                    `json:"prompt"`
	ImageSize        string                    `json:"image_size,omitempty"`
	AspectRatio      string                    `json:"aspect_ratio,omitempty"`
	Quality          string                    `json:"quality,omitempty"`
	Background       string                    `json:"background,omitempty"`
	ThinkingLevel    string                    `json:"thinking_level,omitempty"`
	Endpoint         string                    `json:"endpoint,omitempty"`
	Duration         float64                   `json:"duration,omitempty"`
	Resolution       string                    `json:"resolution,omitempty"`
	References       []CreativeCanvasReference `json:"references,omitempty"`
	MaskAssetID      string                    `json:"mask_asset_id,omitempty"`
	GenerateAudio    *bool                     `json:"generate_audio,omitempty"`
	Watermark        *bool                     `json:"watermark,omitempty"`
}

// CreativeCanvasExecutionBatch 原子受理多个独立结算任务，不合并或重复执行其资金动作。
type CreativeCanvasExecutionBatch struct {
	BatchID    string                     `json:"batch_id"`
	Executions []*CreativeCanvasExecution `json:"executions"`
}

type CreativeCanvasExecution struct {
	ID             string                        `json:"id"`
	UserID         int64                         `json:"-"`
	CanvasID       string                        `json:"canvas_id"`
	NodeID         string                        `json:"node_id"`
	Kind           string                        `json:"kind"`
	Params         CreativeCanvasExecutionParams `json:"params"`
	IdempotencyKey string                        `json:"-"`
	Fingerprint    string                        `json:"-"`
	TaskID         string                        `json:"task_id,omitempty"`
	Status         string                        `json:"status"`
	OutputText     string                        `json:"output_text,omitempty"`
	// running 时可携带核对阶段提示；是否终态仍只由 Status 决定。
	ErrorCode    string                 `json:"error_code,omitempty"`
	ErrorMessage string                 `json:"error_message,omitempty"`
	Assets       []*CreativeCanvasAsset `json:"assets"`
	LeaseToken   string                 `json:"-"`
	CreatedAt    time.Time              `json:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at"`
}

type CreativeCanvasInputAsset struct {
	Asset   *CreativeCanvasAsset
	Role    string
	Content []byte
}

type CreativeCanvasOutput struct {
	Index    int
	MIMEType string
	Reader   io.ReadCloser
	Size     int64
}

type CreativeCanvasExecutionResult struct {
	Status       string
	OutputText   string
	Outputs      []CreativeCanvasOutput
	ErrorCode    string
	ErrorMessage string
}

// 执行器只能复用原任务服务；提交幂等键由 execution ID 固定生成。
type CreativeCanvasExecutor interface {
	Submit(context.Context, *CreativeCanvasExecution, []CreativeCanvasInputAsset) (string, error)
	Poll(context.Context, *CreativeCanvasExecution) (*CreativeCanvasExecutionResult, error)
}

type CreativeCanvasExecutorAcknowledger interface {
	Ack(context.Context, *CreativeCanvasExecution, int) error
}

// 恢复已提交但尚未绑定的任务必须先于重新读取可能已过期的输入素材。
type CreativeCanvasExecutorRecoverer interface {
	Recover(context.Context, *CreativeCanvasExecution) (string, error)
}

// 文件存储实现须流式写入并原子发布，不允许返回公共素材地址。
type CreativeCanvasAssetStore interface {
	Put(context.Context, string, io.Reader, int64) (int64, string, error)
	Open(context.Context, string) (io.ReadSeekCloser, error)
	Delete(context.Context, string) error
}

type CreativeCanvasRepository interface {
	Create(context.Context, *CreativeCanvas) error
	Get(context.Context, int64, string) (*CreativeCanvas, error)
	List(context.Context, int64, int, int) ([]*CreativeCanvas, int64, error)
	Update(context.Context, *CreativeCanvas, int64) error
	Delete(context.Context, int64, string, int64) error
	ReserveAsset(context.Context, *CreativeCanvasAsset, int64) (*CreativeCanvasAsset, error)
	ReadyAsset(context.Context, int64, string, int64, string) error
	GetAsset(context.Context, int64, string) (*CreativeCanvasAsset, error)
	ListAssets(context.Context, int64, string, string, int, int) ([]*CreativeCanvasAsset, int64, error)
	DeleteAsset(context.Context, int64, string) error
	ExpiredAssets(context.Context, time.Time, int) ([]*CreativeCanvasAsset, error)
	MarkAssetPurged(context.Context, string) error
	CreateExecution(context.Context, *CreativeCanvasExecution) (*CreativeCanvasExecution, error)
	CreateExecutionBatch(context.Context, []*CreativeCanvasExecution) ([]*CreativeCanvasExecution, error)
	GetExecutionBatch(context.Context, int64, string) ([]*CreativeCanvasExecution, error)
	GetExecution(context.Context, int64, string) (*CreativeCanvasExecution, error)
	ListExecutions(context.Context, int64, string, int, int) ([]*CreativeCanvasExecution, int64, error)
	ClaimExecutions(context.Context, int, time.Duration) ([]*CreativeCanvasExecution, error)
	SaveExecution(context.Context, *CreativeCanvasExecution, time.Time) error
	FindExecutionByTaskID(context.Context, string) (*CreativeCanvasExecution, error)
}
