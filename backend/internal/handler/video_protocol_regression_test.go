package handler

import (
	"net/http"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 视频平台 Key 只保留统一创建入口：旧统一与原生创建别名一律返回 410 且不创建任务；
// /v1/videos 原样保留请求体与客户端幂等键，并以规范请求交给任务服务。
func TestVideoHandlerRegressionCreateAliases(t *testing.T) {
	for _, path := range []string{
		"/v1/video/generations", "/video/generations",
		"/api/v3/contents/generations/tasks", "/v3/contents/generations/tasks", "/v1/contents/generations/tasks", "/contents/generations/tasks",
		"/text-to-video/model", "/image-to-video/model", "/omni-video/model", "/v1/videos/text2video", "/v1/videos/omni-video",
		"/api/v1/services/aigc/video-generation/video-synthesis", "/v2/video_generation",
	} {
		t.Run(path, func(t *testing.T) {
			fixture := &videoLifecycleFixture{response: &service.VideoTaskResponse{StatusCode: http.StatusAccepted, Body: []byte(`{"task_id":"provider"}`), LocalTaskID: "local"}}
			c, w := videoHandlerContext(http.MethodPost, path, `{"model":"model","resolution":"540P","duration":5}`, service.PlatformVideo)
			NewVideoHandler(fixture).Handle(c)
			require.Equal(t, http.StatusGone, w.Code)
			require.Contains(t, w.Body.String(), "VIDEO_ENDPOINT_RETIRED")
			require.Zero(t, fixture.creates)
			require.Empty(t, w.Header().Get("X-Video-Task-ID"))
		})
	}
	body := `{"model":"model","resolution":"540P","duration":5,"extra":{"explicit_false":false,"empty":null}}`
	fixture := &videoLifecycleFixture{response: &service.VideoTaskResponse{StatusCode: http.StatusAccepted, Header: http.Header{"Cache-Control": []string{"public, max-age=600"}}, Body: []byte(`{"task_id":"provider","future":null}`), LocalTaskID: "local"}}
	c, w := videoHandlerContext(http.MethodPost, "/v1/videos", body, service.PlatformVideo)
	c.Request.Header.Set("Content-Type", "application/json; charset=utf-8")
	c.Request.Header.Set("Idempotency-Key", "stable-client-key")
	NewVideoHandler(fixture).Handle(c)
	require.Equal(t, http.StatusAccepted, w.Code)
	require.Equal(t, 1, fixture.creates)
	require.Equal(t, "openai_videos", fixture.request.InboundProtocol)
	require.False(t, fixture.request.Native)
	require.Empty(t, fixture.request.ModelPath)
	require.NotNil(t, fixture.request.Canonical)
	require.Equal(t, body, string(fixture.request.Body))
	require.Equal(t, "stable-client-key", fixture.request.IdempotencyKey)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Equal(t, "local", w.Header().Get("X-Video-Task-ID"))
}

// 旧查询与取消别名对视频平台 Key 已下线：返回 410 且不查询、不取消；统一入口查询仍按 openai_videos 归属。
func TestVideoHandlerRegressionQueryCancelAliases(t *testing.T) {
	for _, tc := range []struct {
		path   string
		cancel bool
	}{
		{"/v1/video/generations/owned", true}, {"/video/generations/owned", true}, {"/v1/tasks/owned", true},
		{"/api/v3/contents/generations/tasks/owned", true}, {"/v3/contents/generations/tasks/owned", true},
		{"/v1/contents/generations/tasks/owned", true}, {"/contents/generations/tasks/owned", true},
		{"/tasks?task_ids=owned", false}, {"/api/v1/tasks/owned", false}, {"/v2/query/video_generation/owned", false},
	} {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			t.Run(method+tc.path, func(t *testing.T) {
				fixture := &videoLifecycleFixture{response: &service.VideoTaskResponse{StatusCode: 200, Header: http.Header{"Cache-Control": []string{"public"}}, Body: []byte(`{"status":"processing"}`)}}
				c, w := videoHandlerContext(method, tc.path, "", service.PlatformVideo)
				NewVideoHandler(fixture).Handle(c)
				if method == http.MethodDelete && !tc.cancel {
					require.Equal(t, http.StatusNotFound, w.Code)
				} else {
					require.Equal(t, http.StatusGone, w.Code)
				}
				require.Zero(t, fixture.queries)
				require.Zero(t, fixture.cancels)
				require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			})
		}
	}
	fixture := &videoLifecycleFixture{response: &service.VideoTaskResponse{StatusCode: 200, Header: http.Header{"Cache-Control": []string{"public"}}, Body: []byte(`{"status":"processing"}`)}}
	c, w := videoHandlerContext(http.MethodGet, "/v1/videos/owned", "", service.PlatformVideo)
	NewVideoHandler(fixture).Handle(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "owned", fixture.id)
	require.Equal(t, "openai_videos", fixture.protocol)
	require.Equal(t, 1, fixture.queries)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

// 无模型、歧义 JSON、未知参数和非 JSON 正文在统一入口进入生命周期前失败，不产生有费用的提交。
func TestVideoHandlerRegressionInvalidInputNeverSubmits(t *testing.T) {
	for _, tc := range []struct {
		body, contentType string
		status            int
	}{
		{`{}`, "application/json", 400}, {`{"model":" "}`, "application/json", 400}, {`{"model":123}`, "application/json", 400},
		{`{"model":"a","model":"b"}`, "application/json", 400}, {`{"model":"a","parameters":{"duration":1,"duration":5}}`, "application/json", 400},
		{`{"model":"a","Duration":5}`, "application/json", 400}, {`{"model":"a"}`, "text/plain", 415},
	} {
		fixture := &videoLifecycleFixture{}
		c, w := videoHandlerContext(http.MethodPost, "/v1/videos", tc.body, service.PlatformVideo)
		c.Request.Header.Set("Content-Type", tc.contentType)
		NewVideoHandler(fixture).Handle(c)
		require.Equal(t, tc.status, w.Code, tc.body)
		require.Zero(t, fixture.creates)
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	}
	fixture := &videoLifecycleFixture{err: infraerrors.NotFound("VIDEO_TASK_NOT_FOUND", "not found")}
	c, w := videoHandlerContext(http.MethodGet, "/v1/videos/unknown", "", service.PlatformVideo)
	NewVideoHandler(fixture).Handle(c)
	require.Equal(t, 404, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

// 生命周期缺价错误必须向调用方返回 400，不能被包装成成功任务或免费生成。
func TestVideoHandlerRegressionPricingRejectionIsNotSuccess(t *testing.T) {
	fixture := &videoLifecycleFixture{err: service.ErrVideoTaskPricing}
	c, w := videoHandlerContext(http.MethodPost, "/v1/videos", `{"model":"unpriced","resolution":"540p","duration":5}`, service.PlatformVideo)
	NewVideoHandler(fixture).Handle(c)
	require.Equal(t, 400, w.Code)
	require.Equal(t, 1, fixture.creates)
	require.Contains(t, w.Body.String(), "VIDEO_PRICING_UNAVAILABLE")
	require.NotContains(t, w.Body.String(), `"status":"queued"`)
	require.Empty(t, w.Header().Get("X-Video-Task-ID"))
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}
