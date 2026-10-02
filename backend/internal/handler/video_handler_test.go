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

func TestVideoHandlerPreservesNativeBodyResponseAndModelPath(t *testing.T) {
	body := `{"prompt":"x","future":{"enabled":false}}`
	response := `{"task_id":"native","future":null}`
	f := &videoLifecycleFixture{response: &service.VideoTaskResponse{StatusCode: 202, Header: http.Header{"X-Provider": []string{"test"}, "Cache-Control": []string{"public, max-age=600"}}, Body: []byte(response), LocalTaskID: "local"}}
	c, w := videoHandlerContext(http.MethodPost, "/omni-video/kling-model", body, service.PlatformVideo)
	c.Request.Header.Set("Idempotency-Key", "dedupe")
	NewVideoHandler(f).Handle(c)
	require.Equal(t, 202, w.Code)
	require.Equal(t, response, w.Body.String())
	require.Equal(t, "local", w.Header().Get("X-Video-Task-ID"))
	require.Equal(t, "test", w.Header().Get("X-Provider"))
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Equal(t, body, string(f.request.Body))
	require.Equal(t, "kling-model", f.request.ModelPath)
	require.Equal(t, "/omni-video/{model}", f.request.NativePath)
	require.Equal(t, "dedupe", f.request.IdempotencyKey)
	require.Equal(t, "kling", f.request.InboundProtocol)
	require.True(t, f.request.Native)
}

func TestVideoHandlerKlingSingleTaskBoundary(t *testing.T) {
	for _, query := range []string{"?task_ids=a,b", "?task_ids=a&task_ids=b", ""} {
		f := &videoLifecycleFixture{}
		c, w := videoHandlerContext(http.MethodGet, "/tasks"+query, "", service.PlatformVideo)
		NewVideoHandler(f).Handle(c)
		require.Equal(t, 400, w.Code)
		require.Zero(t, f.queries)
	}
	f := &videoLifecycleFixture{response: &service.VideoTaskResponse{StatusCode: 200, Body: []byte(`{"data":[]}`)}}
	c, w := videoHandlerContext(http.MethodGet, "/tasks?task_ids=owned", "", service.PlatformVideo)
	NewVideoHandler(f).Handle(c)
	require.Equal(t, 200, w.Code)
	require.Equal(t, "owned", f.id)
	require.Equal(t, "kling", f.protocol)
}

func TestVideoHandlerSeedanceLegacyDispatch(t *testing.T) {
	for _, platform := range []string{service.PlatformOpenAI, service.PlatformVideo, ""} {
		t.Run(platform, func(t *testing.T) {
			f := &videoLifecycleFixture{response: &service.VideoTaskResponse{StatusCode: 202, Body: []byte(`{"id":"video"}`)}}
			c, w := videoHandlerContext(http.MethodGet, "/api/v3/contents/generations/tasks/task", "", platform)
			legacy := 0
			NewVideoHandler(f).Seedance(c, func(c *gin.Context) { legacy++; c.String(200, "legacy") })
			if platform == service.PlatformOpenAI {
				require.Equal(t, 1, legacy)
				require.Zero(t, f.queries)
				require.Equal(t, "legacy", w.Body.String())
			} else {
				require.Zero(t, legacy)
				require.Equal(t, 1, f.queries)
			}
		})
	}
	f := &videoLifecycleFixture{err: infraerrors.NotFound("VIDEO_TASK_NOT_FOUND", "not found")}
	c, w := videoHandlerContext(http.MethodGet, "/api/v3/contents/generations/tasks/old", "", "")
	NewVideoHandler(f).Seedance(c, func(c *gin.Context) { c.String(200, "legacy-owner") })
	require.Equal(t, "legacy-owner", w.Body.String())
}

func TestVideoHandlerRejectsConflictingModelAndKeepsUnknownAcceptanceError(t *testing.T) {
	f := &videoLifecycleFixture{}
	c, w := videoHandlerContext(http.MethodPost, "/text-to-video/path-model", `{"model":"different"}`, service.PlatformVideo)
	NewVideoHandler(f).Handle(c)
	require.Equal(t, 400, w.Code)
	require.Zero(t, f.creates)
	f.response = &service.VideoTaskResponse{StatusCode: 502, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: []byte("upstream unavailable"), LocalTaskID: "local-unknown"}
	c, w = videoHandlerContext(http.MethodPost, "/v1/video/generations", `{"model":"m"}`, service.PlatformVideo)
	NewVideoHandler(f).Handle(c)
	require.Equal(t, 502, w.Code)
	require.Equal(t, "upstream unavailable", w.Body.String())
	require.Equal(t, "local-unknown", w.Header().Get("X-Video-Task-ID"))
}
