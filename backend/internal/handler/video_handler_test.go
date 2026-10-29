package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 生命周期替身只记录HTTP边界输入，不连接数据库或供应商。
type videoLifecycleFixture struct {
	request                   service.VideoTaskSubmitRequest
	response                  *service.VideoTaskResponse
	err                       error
	creates, queries, cancels int
	id, protocol              string
}

func (f *videoLifecycleFixture) Submit(_ context.Context, _ *service.APIKey, request service.VideoTaskSubmitRequest) (*service.VideoTaskResponse, error) {
	f.creates++
	f.request = request
	return f.response, f.err
}
func (f *videoLifecycleFixture) Query(_ context.Context, _ *service.APIKey, id, protocol string) (*service.VideoTaskResponse, error) {
	f.queries++
	f.id, f.protocol = id, protocol
	return f.response, f.err
}
func (f *videoLifecycleFixture) Cancel(_ context.Context, _ *service.APIKey, id, protocol string) (*service.VideoTaskResponse, error) {
	f.cancels++
	f.id, f.protocol = id, protocol
	return f.response, f.err
}

func videoHandlerContext(method, path, body, platform string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	key := &service.APIKey{ID: 3, UserID: 1}
	if platform != "" {
		id := int64(5)
		key.GroupID = &id
		key.Group = &service.Group{ID: id, Platform: platform, Status: service.StatusActive}
	}
	c.Set(string(middleware.ContextKeyAPIKey), key)
	return c, w
}

// 统一入口原样保留请求体、响应体与上游头，并把严格解析后的规范请求交给任务服务；入口不再携带原生路径或模型路径。
func TestVideoHandlerPreservesNativeBodyResponseAndModelPath(t *testing.T) {
	body := `{"model":"kling-model","prompt":"x","extra":{"future":{"enabled":false}}}`
	response := `{"task_id":"native","future":null}`
	f := &videoLifecycleFixture{response: &service.VideoTaskResponse{StatusCode: 202, Header: http.Header{"X-Provider": []string{"test"}, "Cache-Control": []string{"public, max-age=600"}}, Body: []byte(response), LocalTaskID: "local"}}
	c, w := videoHandlerContext(http.MethodPost, "/v1/videos", body, service.PlatformVideo)
	c.Request.Header.Set("Idempotency-Key", "dedupe")
	NewVideoHandler(f).Handle(c)
	require.Equal(t, 202, w.Code)
	require.Equal(t, response, w.Body.String())
	require.Equal(t, "local", w.Header().Get("X-Video-Task-ID"))
	require.Equal(t, "test", w.Header().Get("X-Provider"))
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Equal(t, body, string(f.request.Body))
	require.NotNil(t, f.request.Canonical)
	require.Equal(t, "kling-model", f.request.Canonical.Model)
	require.Empty(t, f.request.ModelPath)
	require.Empty(t, f.request.NativePath)
	require.Equal(t, "dedupe", f.request.IdempotencyKey)
	require.Equal(t, "openai_videos", f.request.InboundProtocol)
	require.False(t, f.request.Native)
	// 原生 Kling 创建入口对视频平台 Key 已下线，必须在创建任务前返回 410。
	f = &videoLifecycleFixture{}
	c, w = videoHandlerContext(http.MethodPost, "/omni-video/kling-model", `{"prompt":"x"}`, service.PlatformVideo)
	NewVideoHandler(f).Handle(c)
	require.Equal(t, http.StatusGone, w.Code)
	require.Contains(t, w.Body.String(), "VIDEO_ENDPOINT_RETIRED")
	require.Zero(t, f.creates)
}

// Kling 原生查询入口已下线：任何 task_ids 形式都返回 410，且不查询任务。
func TestVideoHandlerKlingSingleTaskBoundary(t *testing.T) {
	for _, query := range []string{"?task_ids=a,b", "?task_ids=a&task_ids=b", "", "?task_ids=owned"} {
		f := &videoLifecycleFixture{response: &service.VideoTaskResponse{StatusCode: 200, Body: []byte(`{"data":[]}`)}}
		c, w := videoHandlerContext(http.MethodGet, "/tasks"+query, "", service.PlatformVideo)
		NewVideoHandler(f).Handle(c)
		require.Equal(t, http.StatusGone, w.Code)
		require.Zero(t, f.queries)
	}
}

// Vidu 原生创建、查询与取消入口对视频平台 Key 均已下线，不触达任务服务。
func TestVideoHandlerViduNativeOperationAndQuery(t *testing.T) {
	f := &videoLifecycleFixture{response: &service.VideoTaskResponse{StatusCode: 200, Body: []byte(`{"task_id":"owned","state":"created"}`)}}
	c, w := videoHandlerContext(http.MethodPost, "/ent/v2/reference2video", `{"model":"viduq3-pro","images":["a","b"]}`, service.PlatformVideo)
	NewVideoHandler(f).Handle(c)
	require.Equal(t, http.StatusGone, w.Code)
	c, w = videoHandlerContext(http.MethodGet, "/ent/v2/tasks/owned/creations", "", service.PlatformVideo)
	NewVideoHandler(f).Handle(c)
	require.Equal(t, http.StatusGone, w.Code)
	c, w = videoHandlerContext(http.MethodDelete, "/ent/v2/tasks/owned/creations", "", service.PlatformVideo)
	NewVideoHandler(f).Handle(c)
	require.Equal(t, 404, w.Code)
	require.Zero(t, f.creates)
	require.Zero(t, f.queries)
	require.Zero(t, f.cancels)
}

// OpenAI 平台 Seedance 仍走旧链路；视频平台 Key 返回 410；未分组（复合）Key 查询先查本站任务再回落旧归属，创建交回旧链路。
func TestVideoHandlerSeedanceLegacyDispatch(t *testing.T) {
	for _, platform := range []string{service.PlatformOpenAI, service.PlatformVideo, ""} {
		t.Run(platform, func(t *testing.T) {
			f := &videoLifecycleFixture{response: &service.VideoTaskResponse{StatusCode: 202, Body: []byte(`{"id":"video"}`)}}
			c, w := videoHandlerContext(http.MethodGet, "/api/v3/contents/generations/tasks/task", "", platform)
			legacy := 0
			NewVideoHandler(f).Seedance(c, func(c *gin.Context) { legacy++; c.String(200, "legacy") })
			switch platform {
			case service.PlatformOpenAI:
				require.Equal(t, 1, legacy)
				require.Zero(t, f.queries)
				require.Equal(t, "legacy", w.Body.String())
			case service.PlatformVideo:
				require.Zero(t, legacy)
				require.Zero(t, f.queries)
				require.Equal(t, http.StatusGone, w.Code)
				require.Contains(t, w.Body.String(), "VIDEO_ENDPOINT_RETIRED")
			default:
				require.Zero(t, legacy)
				require.Equal(t, 1, f.queries)
			}
		})
	}
	f := &videoLifecycleFixture{err: infraerrors.NotFound("VIDEO_TASK_NOT_FOUND", "not found")}
	c, w := videoHandlerContext(http.MethodGet, "/api/v3/contents/generations/tasks/old", "", "")
	NewVideoHandler(f).Seedance(c, func(c *gin.Context) { c.String(200, "legacy-owner") })
	require.Equal(t, "legacy-owner", w.Body.String())
	f = &videoLifecycleFixture{}
	c, w = videoHandlerContext(http.MethodPost, "/api/v3/contents/generations/tasks", `{"model":"m","content":[]}`, "")
	NewVideoHandler(f).Seedance(c, func(c *gin.Context) { c.String(200, "legacy-create") })
	require.Equal(t, "legacy-create", w.Body.String())
	require.Zero(t, f.creates)
}

// 统一入口拒绝无法解析为规范请求的参数且不创建任务；受理不明的上游错误原样返回并保留本地任务 ID。
func TestVideoHandlerRejectsConflictingModelAndKeepsUnknownAcceptanceError(t *testing.T) {
	f := &videoLifecycleFixture{}
	for _, body := range []string{
		`{"model":"m","images":["https://a/x.png"],"input_reference":{"image_url":"https://a/y.png"}}`,
		`{"model":"m","vendor_field":true}`,
		`{"prompt":"missing model"}`,
	} {
		c, w := videoHandlerContext(http.MethodPost, "/v1/videos", body, service.PlatformVideo)
		NewVideoHandler(f).Handle(c)
		require.Equal(t, 400, w.Code, body)
	}
	require.Zero(t, f.creates)
	c, w := videoHandlerContext(http.MethodPost, "/v1/video/generations", `{"model":"m"}`, service.PlatformVideo)
	NewVideoHandler(f).Handle(c)
	require.Equal(t, http.StatusGone, w.Code)
	require.Zero(t, f.creates)
	f.response = &service.VideoTaskResponse{StatusCode: 502, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: []byte("upstream unavailable"), LocalTaskID: "local-unknown"}
	c, w = videoHandlerContext(http.MethodPost, "/v1/videos", `{"model":"m"}`, service.PlatformVideo)
	NewVideoHandler(f).Handle(c)
	require.Equal(t, 502, w.Code)
	require.Equal(t, "upstream unavailable", w.Body.String())
	require.Equal(t, "local-unknown", w.Header().Get("X-Video-Task-ID"))
}
