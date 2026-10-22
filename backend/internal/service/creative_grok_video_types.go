package service

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/tidwall/gjson"
)

// Grok 视频沿用原异步任务账务，画布仓储只持久化发送栅栏和原任务归属。
type CreativeGrokVideoExecutionState struct {
	Phase       string
	APIKeyID    int64
	GroupID     int64
	TaskID      string
	AccountID   int64
	HTTPStatus  int
	RequestSent bool
	ErrorCode   string
}

type CreativeGrokVideoRepository interface {
	BeginGrokVideoExecution(context.Context, *CreativeCanvasExecution, int64) (bool, error)
	GetGrokVideoExecutionState(context.Context, int64, string) (*CreativeGrokVideoExecutionState, error)
	SaveGrokVideoResponse(context.Context, int64, string, int, bool, string) error
	BindGrokVideoTask(context.Context, int64, string, *LegacyVideoTask) error
}

// 私有桥在进程内进入正式网关，服务层不依赖路由，也不向本站端口发送回环请求。
type CreativeGrokVideoGateway interface {
	Generate(context.Context, *APIKey, []byte) (CreativeTextGatewayResponse, error)
	Task(context.Context, int64, int64, string) (*LegacyVideoTask, error)
	OpenContent(context.Context, *APIKey, string) (*http.Response, error)
}

type creativeGrokVideoContextKey struct{}
type creativeGrokVideoContext struct {
	executionID    string
	userID         int64
	keyID          int64
	groupID        int64
	taskID         string
	sent           atomic.Bool
	rejectedStatus atomic.Int32
	contentError   func()
}

// 可信标记仅由画布执行器创建，公网 Header、参数及模型名称均不能开启它。
func creativeGrokVideoContextFor(ctx context.Context, e *CreativeCanvasExecution, key *APIKey, taskID string) (context.Context, *creativeGrokVideoContext) {
	sink := &creativeGrokVideoContext{executionID: e.ID, userID: e.UserID, keyID: key.ID, groupID: e.Params.GroupID, taskID: taskID}
	return context.WithValue(ctx, creativeGrokVideoContextKey{}, sink), sink
}

// MarkCreativeGrokVideoRequestSent 只在真正发送视频创建前调用，用于区分本地拒绝和受理未知。
func MarkCreativeGrokVideoRequestSent(ctx context.Context) {
	if sink, _ := ctx.Value(creativeGrokVideoContextKey{}).(*creativeGrokVideoContext); sink != nil && sink.taskID == "" {
		sink.sent.Store(true)
		// 下一账号已经开始新尝试，不能沿用上一账号拒绝来解释本次未知受理。
		sink.rejectedStatus.Store(0)
	}
}

// 原始完整响应明确证明没有受理时才登记拒绝；最终错误透传状态不具备这种证明力。
func MarkCreativeGrokVideoRejected(ctx context.Context, status int) {
	if sink, _ := ctx.Value(creativeGrokVideoContextKey{}).(*creativeGrokVideoContext); sink != nil && sink.taskID == "" {
		sink.rejectedStatus.Store(int32(status))
	}
}

// 参数与审核拒绝可以结束画布执行，但不能借此给原网关增加自动换号重发资格。
func creativeGrokVideoDefinitelyRejected(status int, body []byte) bool {
	if _, present := asyncVideoTaskID(body); present || videoCreationHasAcceptanceEvidence(body) {
		return false
	}
	if VideoCreationDefinitelyRejected(status, body) {
		return true
	}
	if !gjson.ValidBytes(body) {
		return false
	}
	if isGrokContentPolicyRejection(status, body) {
		return true
	}
	if status != http.StatusBadRequest && status != http.StatusUnprocessableEntity {
		return false
	}
	for _, path := range []string{"error.code", "error.type", "code"} {
		switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, path).String())) {
		case "invalid_request_error", "invalid_argument", "invalid_arguments", "validation_error", "bad_request", "invalid_parameter", "invalid_parameters":
			return true
		}
	}
	message := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	for _, marker := range []string{"invalid duration", "invalid resolution", "invalid aspect", "invalid image", "image is required", "image is missing", "unsupported resolution", "unsupported aspect", "duration must", "resolution must", "aspect_ratio must"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// CreativeGrokVideoExecutionID 只为与私有执行身份完全匹配的原任务附加恢复关联。
func CreativeGrokVideoExecutionID(ctx context.Context, userID, keyID, groupID int64) string {
	sink, _ := ctx.Value(creativeGrokVideoContextKey{}).(*creativeGrokVideoContext)
	if sink == nil || sink.taskID != "" || sink.userID != userID || sink.keyID != keyID || sink.groupID != groupID {
		return ""
	}
	return sink.executionID
}

// IsCreativeGrokVideoRead 只豁免原任务读取的消费准入；真实 Key、用户和原账号归属校验仍执行。
func IsCreativeGrokVideoRead(ctx context.Context, key *APIKey, taskID string) bool {
	sink, _ := ctx.Value(creativeGrokVideoContextKey{}).(*creativeGrokVideoContext)
	return sink != nil && sink.taskID != "" && (taskID == "" || sink.taskID == taskID) &&
		creativeVideoKeyMatches(key, sink.userID, sink.groupID) && key.ID == sink.keyID
}

// 下载响应已开始后若上游断流，显式关闭私有管道为错误，避免未知长度的截断视频被当成成功。
func BindCreativeGrokVideoContentError(ctx context.Context, onError func()) {
	if sink, _ := ctx.Value(creativeGrokVideoContextKey{}).(*creativeGrokVideoContext); sink != nil && sink.taskID != "" {
		sink.contentError = onError
	}
}
func MarkCreativeGrokVideoContentError(ctx context.Context) {
	if sink, _ := ctx.Value(creativeGrokVideoContextKey{}).(*creativeGrokVideoContext); sink != nil && sink.taskID != "" && sink.contentError != nil {
		sink.contentError()
	}
}
