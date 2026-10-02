package handler

import (
	"net/http"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 创建别名只改变协议描述，生命周期收到的供应商参数和客户端幂等键必须原样保留。
func TestVideoHandlerRegressionCreateAliases(t *testing.T) {
	for _, tc := range []struct {
		path, protocol, model string
		native                bool
	}{
		{"/v1/video/generations", "unified", "", false}, {"/video/generations", "unified", "", false},
		{"/api/v3/contents/generations/tasks", "seedance", "", true}, {"/v3/contents/generations/tasks", "seedance", "", true},
		{"/v1/contents/generations/tasks", "seedance", "", true}, {"/contents/generations/tasks", "seedance", "", true},
		{"/text-to-video/model", "kling", "model", true}, {"/image-to-video/model", "kling", "model", true}, {"/omni-video/model", "kling", "model", true},
		{"/v1/videos/text2video", "kling", "", true}, {"/v1/videos/omni-video", "kling", "", true},
		{"/api/v1/services/aigc/video-generation/video-synthesis", "wan", "", true}, {"/v2/video_generation", "minimax", "", true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			body := `{"model":"model","resolution":"540P","duration":5,"future":{"explicit_false":false,"empty":null}}`
			fixture := &videoLifecycleFixture{response: &service.VideoTaskResponse{StatusCode: http.StatusAccepted, Header: http.Header{"Cache-Control": []string{"public, max-age=600"}}, Body: []byte(`{"task_id":"provider","future":null}`), LocalTaskID: "local"}}
			c, w := videoHandlerContext(http.MethodPost, tc.path, body, service.PlatformVideo)
			c.Request.Header.Set("Content-Type", "application/json; charset=utf-8")
			c.Request.Header.Set("Idempotency-Key", "stable-client-key")
			NewVideoHandler(fixture).Handle(c)
			require.Equal(t, http.StatusAccepted, w.Code)
			require.Equal(t, 1, fixture.creates)
			require.Equal(t, tc.protocol, fixture.request.InboundProtocol)
			require.Equal(t, tc.native, fixture.request.Native)
			require.Equal(t, tc.model, fixture.request.ModelPath)
			require.Equal(t, body, string(fixture.request.Body))
			require.Equal(t, "stable-client-key", fixture.request.IdempotencyKey)
			require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			require.Equal(t, "local", w.Header().Get("X-Video-Task-ID"))
		})
	}
}

// 查询与取消沿用原端点归属，统一两个别名不会变成供应商协议查询。
func TestVideoHandlerRegressionQueryCancelAliases(t *testing.T) {
	for _, tc := range []struct {
		path, protocol string
		cancel         bool
	}{
		{"/v1/video/generations/owned", "unified", true}, {"/video/generations/owned", "unified", true}, {"/v1/tasks/owned", "unified", true},
		{"/api/v3/contents/generations/tasks/owned", "seedance", true}, {"/v3/contents/generations/tasks/owned", "seedance", true},
		{"/v1/contents/generations/tasks/owned", "seedance", true}, {"/contents/generations/tasks/owned", "seedance", true},
		{"/tasks?task_ids=owned", "kling", false}, {"/api/v1/tasks/owned", "wan", false}, {"/v2/query/video_generation/owned", "minimax", false},
	} {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			t.Run(method+tc.path, func(t *testing.T) {
				fixture := &videoLifecycleFixture{response: &service.VideoTaskResponse{StatusCode: 200, Header: http.Header{"Cache-Control": []string{"public"}}, Body: []byte(`{"status":"processing"}`)}}
				c, w := videoHandlerContext(method, tc.path, "", service.PlatformVideo)
				NewVideoHandler(fixture).Handle(c)
				if method == http.MethodDelete && !tc.cancel {
					require.Equal(t, http.StatusNotFound, w.Code)
					require.Zero(t, fixture.cancels)
				} else {
					require.Equal(t, http.StatusOK, w.Code)
					require.Equal(t, "owned", fixture.id)
					require.Equal(t, tc.protocol, fixture.protocol)
					if method == http.MethodDelete {
						require.Equal(t, 1, fixture.cancels)
					} else {
						require.Equal(t, 1, fixture.queries)
					}
				}
				require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			})
		}
	}
}

// 无模型、歧义 JSON 和非 JSON 正文在进入生命周期前失败，不产生有费用的提交。
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
		c, w := videoHandlerContext(http.MethodPost, "/v1/video/generations", tc.body, service.PlatformVideo)
		c.Request.Header.Set("Content-Type", tc.contentType)
		NewVideoHandler(fixture).Handle(c)
		require.Equal(t, tc.status, w.Code, tc.body)
		require.Zero(t, fixture.creates)
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	}
	fixture := &videoLifecycleFixture{err: infraerrors.NotFound("VIDEO_TASK_NOT_FOUND", "not found")}
	c, w := videoHandlerContext(http.MethodGet, "/v1/tasks/unknown", "", service.PlatformVideo)
	NewVideoHandler(fixture).Handle(c)
	require.Equal(t, 404, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

// 生命周期缺价错误必须向调用方返回 400，不能被包装成成功任务或免费生成。
func TestVideoHandlerRegressionPricingRejectionIsNotSuccess(t *testing.T) {
	fixture := &videoLifecycleFixture{err: service.ErrVideoTaskPricing}
	c, w := videoHandlerContext(http.MethodPost, "/v1/video/generations", `{"model":"unpriced","resolution":"540p","duration":5}`, service.PlatformVideo)
	NewVideoHandler(fixture).Handle(c)
	require.Equal(t, 400, w.Code)
	require.Equal(t, 1, fixture.creates)
	require.Contains(t, w.Body.String(), "VIDEO_PRICING_UNAVAILABLE")
	require.NotContains(t, w.Body.String(), `"status":"queued"`)
	require.Empty(t, w.Header().Get("X-Video-Task-ID"))
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}
