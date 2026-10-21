package service

import (
	"context"
	"net/http"
)

// VideoEndpoint 是独立视频平台的上游协议，不由模型名称或地址主机猜测。
type VideoEndpoint string

const (
	VideoEndpointCompat       VideoEndpoint = "compat"
	VideoEndpointOpenAIVideos VideoEndpoint = "openai_videos"
	VideoEndpointSeedance     VideoEndpoint = "seedance"
	VideoEndpointKling        VideoEndpoint = "kling"
	VideoEndpointWan          VideoEndpoint = "wan"
	VideoEndpointMiniMax      VideoEndpoint = "minimax"
	VideoEndpointVidu         VideoEndpoint = "vidu"
)

// VideoTaskSubmitRequest 保留入站原始参数；协议差异只影响路由和模型位置。
type VideoTaskSubmitRequest struct {
	Body            []byte
	ContentType     string
	InboundProtocol string
	ModelPath       string
	NativePath      string
	Native          bool
	IdempotencyKey  string
	// 仅画布桥接在校验私有参考素材后设置；不接收客户端字段，也不写入上游正文。
	// 自动比例在真实账号协议确定后才解释，普通 API 和其它协议保持原行为。
	canvasAdaptiveRatio string
	// 仅云画布从已鉴权、已校验的原图字节计算；在实际账号协议确定后检查发送上限。
	// 不读取客户端同名 JSON，不保存到任务快照，也不改变普通 API 请求合同。
	canvasMaxReferenceImageBytes int64
	// 仅内部画布提供，实际协议选定后才发布已鉴权参考图；不接受 JSON，也不参与原始请求幂等摘要。
	canvasPrepareReferences func(context.Context, VideoUpstreamTarget, []byte) ([]byte, error)
	// ExcludedAccountIDs 用于容量拒绝及明确未受理后的重选，不接收客户端 JSON 或参与幂等摘要。
	ExcludedAccountIDs map[int64]struct{} `json:"-"`
}

// VideoTaskResponse 交由处理器写回；任务服务可先完成归属持久化再提交响应。
type VideoTaskResponse struct {
	StatusCode  int
	Header      http.Header
	Body        []byte
	LocalTaskID string
}

// VideoUpstreamTarget 冻结非敏感路由配置；任务轮询不重新选择账号或协议。
type VideoUpstreamTarget struct {
	Version    int           `json:"version"`
	Endpoint   VideoEndpoint `json:"endpoint"`
	BaseURL    string        `json:"base_url"`
	CreatePath string        `json:"create_path"`
	Model      string        `json:"model"`
	AccountID  int64         `json:"account_id"`
	// 仅 OpenAI Videos 使用显式适配配置；冻结后不受账号后续编辑影响。
	OpenAIProfile string `json:"openai_profile,omitempty"`
	// 版本二冻结内置协议及其版本号；旧版本一继续使用原端点和旧适配字段。
	ProtocolAdapter string `json:"protocol_adapter,omitempty"`
}

// VideoRequestMetadata 只供定价与观测，显式协议适配时必须与真正发送的载荷一致。
type VideoRequestMetadata struct {
	Model           string  `json:"model"`
	Resolution      string  `json:"resolution"`
	DurationSeconds float64 `json:"duration_seconds"`
	// Size 保留像素尺寸；Resolution 仍表示实际报价所用的精确尺寸或供应商档位。
	Size              string `json:"size,omitempty"`
	HasReferenceVideo bool   `json:"has_reference_video"`
	// nil 表示输入图片数量存在歧义；明确无图片使用指向零的指针，不读取输出图片数量。
	ReferenceImageCount *int   `json:"reference_image_count,omitempty"`
	Tokens              *int64 `json:"tokens,omitempty"`
}

// VideoUpstreamSelection 持有创建请求的账号并发槽；调用方必须释放。
type VideoUpstreamSelection struct {
	Account        *Account
	Target         VideoUpstreamTarget
	Body           []byte
	Metadata       VideoRequestMetadata
	RequestedModel string
	InternalModel  string
	BillingModel   string
	Release        func()
}

// VideoUpstreamObservation 把同步请求的调度与错误归属交给HTTP边界，后台轮询无需持有Gin上下文。
type VideoUpstreamObservation struct {
	AccountID    int64
	AccountName  string
	Model        string
	Endpoint     string
	SlotAcquired bool
	StatusCode   int
	ErrorMessage string
}

type videoObserverContextKey struct{}

// WithVideoUpstreamObserver 只在当前同步请求中调用观测函数，不跨后台任务保留。
func WithVideoUpstreamObserver(ctx context.Context, observer func(VideoUpstreamObservation)) context.Context {
	return context.WithValue(ctx, videoObserverContextKey{}, observer)
}

func observeVideoUpstream(ctx context.Context, observation VideoUpstreamObservation) {
	if observer, ok := ctx.Value(videoObserverContextKey{}).(func(VideoUpstreamObservation)); ok && observer != nil {
		observer(observation)
	}
}

// VideoUpstreamResponse 同时携带原生响应及最小安全观测，HTTP错误不转换正文。
type VideoUpstreamResponse struct {
	StatusCode     int
	Header         http.Header
	Body           []byte
	TaskID         string
	Status         string
	UpstreamStatus string
	Metadata       VideoRequestMetadata
	VideoURL       string
	// 元数据存在歧义时保留任务归属与状态，但终态不得沿用请求预估值扣费。
	MetadataError string
}

// VideoTaskLifecycle 隔离HTTP协议处理与持久任务、预留和结算状态机。
type VideoTaskLifecycle interface {
	Submit(context.Context, *APIKey, VideoTaskSubmitRequest) (*VideoTaskResponse, error)
	Query(context.Context, *APIKey, string, string) (*VideoTaskResponse, error)
	Cancel(context.Context, *APIKey, string, string) (*VideoTaskResponse, error)
}

// VideoTaskContentLifecycle 为内容流提供可选能力，不要求旧任务处理器实现下载。
type VideoTaskContentLifecycle interface {
	OpenContent(context.Context, *APIKey, string, string, string) (*http.Response, error)
}
