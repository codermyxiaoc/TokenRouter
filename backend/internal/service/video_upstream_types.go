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
	// ExcludedAccountIDs 仅用于创建前的原子容量拒绝后重选，不接收客户端JSON或参与幂等摘要。
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
}

// VideoRequestMetadata 只供定价与观测，绝不修改供应商原始请求。
type VideoRequestMetadata struct {
	Model             string  `json:"model"`
	Resolution        string  `json:"resolution"`
	DurationSeconds   float64 `json:"duration_seconds"`
	HasReferenceVideo bool    `json:"has_reference_video"`
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
