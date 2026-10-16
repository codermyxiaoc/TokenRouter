//go:build unit

package service

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// 已有可灵视频产物即使缺少任务 ID 也禁止重提，避免创建第二份收费视频。
func TestVideoKemeiKlingOutputsAcceptanceEvidence(t *testing.T) {
	for _, fragment := range []string{
		`"outputs":[{"type":"video","url":"https://cdn.example/video.mp4"}]`,
		`"task":{"outputs":[{"type":"video","url":"https://cdn.example/video.mp4"}]}`,
		`"data":[{"outputs":[{"type":"video","url":"https://cdn.example/video.mp4"}]}]`,
		`"data":{"task":{"outputs":[{"type":"video","url":"https://cdn.example/video.mp4"}]}}`,
		`"outputs":[],"outputs":[{"type":"video","url":"https://cdn.example/video.mp4"}]`,
	} {
		body := []byte(`{"error":{"code":"service_unavailable","message":"service unavailable"},` + fragment + `}`)
		for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			require.False(t, VideoCreationDefinitelyRejected(status, body), string(body))
			require.False(t, VideoTaskCanRetrySubmission(&VideoTaskRecord{Status: "failed", BillingStatus: "reserved", CreateResponseStatus: status, CreateResponse: body}), string(body))
		}
	}
	for _, fragment := range []string{
		`"outputs":[]`,
		`"outputs":[{"type":"video","url":" "}]`,
		`"outputs":[{"type":"cover","url":"https://input.example/image.png"}]`,
		`"input":{"outputs":[{"type":"video","url":"https://input.example/video.mp4"}]}`,
		`"prompt":{"task":{"outputs":[{"type":"video","url":"https://input.example/video.mp4"}]}}`,
	} {
		body := []byte(`{"error":{"code":"service_unavailable","message":"service unavailable"},` + fragment + `}`)
		require.True(t, VideoCreationDefinitelyRejected(http.StatusServiceUnavailable, body), string(body))
	}
}

// 禁止再次创建不改变无任务 ID 错误包的退款约定，重复推进只释放一次。
func TestVideoKemeiKlingOutputsKeepRefundPolicy(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		svc, key, upstream, billing, _, repo := newVideoRetryFixture()
		for _, selected := range upstream.selections {
			selected.Target.Version = 2
			selected.Target.Endpoint = VideoEndpointKling
			selected.Target.ProtocolAdapter = VideoProtocolAdapterKemei
			selected.Target.CreatePath = "/omni-video/" + url.PathEscape(selected.Target.Model)
		}
		body := []byte(`{"error":{"code":"service_unavailable"},"outputs":[{"type":"video","url":"https://cdn.example/video.mp4","duration":"5"}]}`)
		response, err := ExtractVideoProtocolResponse(upstream.selections[0].Target, status, body, "")
		require.NoError(t, err)
		upstream.responses = []*VideoUpstreamResponse{response}
		created, err := svc.Submit(context.Background(), key, VideoTaskSubmitRequest{Native: true, InboundProtocol: "kling", Body: []byte(`{"model":"video-model","prompt":"test"}`)})
		require.NoError(t, err)
		require.Equal(t, status, created.StatusCode)
		require.Equal(t, []int64{4}, upstream.attempted)
		require.Equal(t, 1, billing.reserves)
		require.Equal(t, 1, billing.releases)
		require.Zero(t, billing.captures)
		task, err := repo.Get(context.Background(), created.LocalTaskID)
		require.NoError(t, err)
		require.Equal(t, "released", task.BillingStatus)
		require.Empty(t, task.RejectedAttempts)
		svc.advance(context.Background(), task)
		require.Equal(t, 1, billing.releases)
		require.Equal(t, []int64{4}, upstream.attempted)
	}
}
