package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 官方响应中的异常用量必须显式进入待核对，不允许把非法字段当成缺省请求值扣费。
func TestVideoOfficialResponseRejectsMalformedUsage(t *testing.T) {
	for _, tc := range []struct {
		name, adapter, path, body string
		endpoint                  VideoEndpoint
	}{
		{"minimax duration", "minimax_v2", "/v2/video_generation", `{"task":{"id":"task","status":"completed","duration":5,"usage":{"output_seconds":"bad"}}}`, VideoEndpointMiniMax},
		{"minimax resolution", "minimax_v2", "/v2/video_generation", `{"task":{"id":"task","status":"completed","resolution":false,"usage":{"output_seconds":5}}}`, VideoEndpointMiniMax},
		{"seedance tokens", "seedance_v1", "/api/v3/contents/generations/tasks", `{"id":"task","status":"succeeded","usage":{"completion_tokens":-1}}`, VideoEndpointSeedance},
		{"seedance conflicting tokens", "seedance_v1", "/api/v3/contents/generations/tasks", `{"id":"task","status":"succeeded","usage":{"completion_tokens":10,"output_tokens":20}}`, VideoEndpointSeedance},
		{"openai usage", "openai_v1", "/v1/videos", `{"id":"task","status":"completed","seconds":"4","size":"1280x720","usage":[]}`, VideoEndpointOpenAIVideos},
		{"wan duration", "wan_v1", "/api/v1/services/aigc/video-generation/video-synthesis", `{"output":{"task_id":"task","task_status":"SUCCEEDED"},"usage":{"output_video_duration":0}}`, VideoEndpointWan},
		{"kling missing output", "kling_v1", "/text-to-video/m", `{"data":{"id":"task","status":"completed","outputs":[]}}`, VideoEndpointKling},
		{"kling multi output", "kling_v1", "/text-to-video/m", `{"data":{"id":"task","status":"completed","outputs":[{"type":"video","url":"https://cdn.example/1","duration":"5"},{"type":"video","url":"https://cdn.example/2","duration":"5"}]}}`, VideoEndpointKling},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := VideoUpstreamTarget{Version: 2, ProtocolAdapter: tc.adapter, Endpoint: tc.endpoint, Model: "m", CreatePath: tc.path}
			result, err := ExtractVideoProtocolResponse(target, 200, []byte(tc.body), "task")
			require.NoError(t, err)
			require.Equal(t, "completed", result.Status)
			require.NotEmpty(t, result.MetadataError)
		})
	}
}

// 明确的创建业务拒绝即使携带空 data 也要释放一次预扣，不能进入无限核对。
func TestVideoOfficialKlingBusinessRejectionReleasesHold(t *testing.T) {
	for _, suffix := range []string{`,"data":{}`, `,"data":null`, `,"data":[]`, ``} {
		t.Run(suffix, func(t *testing.T) {
			s, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideo)
			target := &upstream.selection.Target
			target.Version, target.Endpoint, target.ProtocolAdapter = 2, VideoEndpointKling, VideoProtocolAdapterKling
			target.CreatePath = "/text-to-video/" + target.Model
			response, err := ExtractVideoProtocolResponse(*target, 200, []byte(`{"code":1001,"message":"authentication failed"`+suffix+`}`), "")
			require.NoError(t, err)
			require.Equal(t, "failed", response.Status)
			upstream.submit = response
			created, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
			require.NoError(t, err)
			task, err := s.repo.Get(context.Background(), created.LocalTaskID)
			require.NoError(t, err)
			require.Equal(t, "released", task.BillingStatus)
			s.advance(context.Background(), task)
			require.Equal(t, 1, billing.releases)
			require.Zero(t, billing.captures)
			require.Equal(t, 1, upstream.submitted)
			require.Empty(t, logs.logs)
		})
	}
}

// 创建错误包可能仍含任务数组或 accepted，存在受理证据就不能按空任务释放。
func TestVideoOfficialKlingBusinessErrorRetainsAcceptance(t *testing.T) {
	for _, body := range []string{
		`{"code":1001,"data":[{"id":"upstream-task","status":"submitted"}]}`,
		`{"code":1001,"data":[{"id":"upstream-task"},{"id":"upstream-task"}]}`,
		`{"code":1001,"accepted":true,"data":{"settings":[]}}`,
		`{"code":1001,"accepted":true}`,
	} {
		t.Run(body, func(t *testing.T) {
			s, key, upstream, billing, _ := newVideoLifecycleFixture(BillingModeVideo)
			target := &upstream.selection.Target
			target.Version, target.Endpoint, target.ProtocolAdapter = 2, VideoEndpointKling, VideoProtocolAdapterKling
			target.CreatePath = "/text-to-video/" + target.Model
			response, err := ExtractVideoProtocolResponse(*target, 200, []byte(body), "")
			require.NoError(t, err)
			require.NotEqual(t, "failed", response.Status)
			upstream.submit = response
			created, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
			require.NoError(t, err)
			task, err := s.repo.Get(context.Background(), created.LocalTaskID)
			require.NoError(t, err)
			require.NotEqual(t, "released", task.BillingStatus)
			require.Zero(t, billing.releases)
			require.Zero(t, billing.captures)
			require.Equal(t, 1, upstream.submitted)
			if response.TaskID != "" {
				require.Equal(t, response.TaskID, task.UpstreamTaskID)
			}
		})
	}
}

// Kling 查询可不回显分辨率，产物补齐可以恢复，但真实档位冲突必须单独消除。
func TestVideoOfficialKlingOutputRecoveryPreservesPricingGuard(t *testing.T) {
	for _, conflict := range []string{"none", "previous response", "same response"} {
		t.Run(conflict, func(t *testing.T) {
			s, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideo)
			target := &upstream.selection.Target
			target.Version, target.Endpoint, target.ProtocolAdapter = 2, VideoEndpointKling, VideoProtocolAdapterKling
			target.CreatePath = "/text-to-video/" + target.Model
			created, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
			require.NoError(t, err)
			task, err := s.repo.Get(context.Background(), created.LocalTaskID)
			require.NoError(t, err)
			poll := func(body string) {
				upstream.poll, err = ExtractVideoProtocolResponse(*target, 200, []byte(body), "upstream-task")
				require.NoError(t, err)
				s.advance(context.Background(), task)
			}
			if conflict == "previous response" {
				poll(`{"data":{"id":"upstream-task","status":"completed","settings":{"resolution":"1080p"},"outputs":[{"type":"video","duration":"4.5","url":"https://cdn.example/video.mp4"}]}}`)
				require.True(t, task.PricingMismatch)
			}
			if conflict == "same response" {
				poll(`{"data":{"id":"upstream-task","status":"completed","settings":{"resolution":"1080p"},"outputs":[{"type":"video","duration":"bad","url":"https://cdn.example/video.mp4"}]}}`)
			} else {
				poll(`{"data":{"id":"upstream-task","status":"completed","outputs":[]}}`)
			}
			require.Equal(t, "reconciliation", task.BillingStatus)
			require.Zero(t, billing.captures)
			poll(`{"data":{"id":"upstream-task","status":"completed","outputs":[{"type":"video","duration":"4.5","url":"https://cdn.example/video.mp4"}]}}`)
			if conflict != "none" {
				require.True(t, task.PricingMismatch, "缺少 settings 不能清除之前确认的分辨率冲突")
				require.Zero(t, billing.captures)
				poll(`{"data":{"id":"upstream-task","status":"completed","settings":{"resolution":"720p"},"outputs":[{"type":"video","duration":"4.5","url":"https://cdn.example/video.mp4"}]}}`)
			}
			require.Equal(t, "settled", task.BillingStatus)
			require.Empty(t, task.ResponseMetadataError)
			require.False(t, task.PricingMismatch)
			require.InDelta(t, 9, task.BillingResult.ActualAmountUSD, 1e-9)
			s.advance(context.Background(), task)
			require.Equal(t, 1, billing.captures)
			require.Zero(t, billing.releases)
			require.Len(t, logs.logs, 1)
		})
	}
}

// 同一包的冲突终态不能取首值退款，改账号配置也无法覆盖任务已有协议。
func TestVideoOfficialAmbiguousStatusNeverConfirmsRefund(t *testing.T) {
	target := VideoUpstreamTarget{Version: 2, ProtocolAdapter: "minimax_v2", Endpoint: VideoEndpointMiniMax, Model: "m", CreatePath: "/v2/video_generation"}
	for _, body := range []string{
		`{"task":{"id":"task","status":"failed","status":"completed"}}`,
		`{"task":{"id":"task","status":"failed","Status":"completed"}}`,
	} {
		result, err := ExtractVideoProtocolResponse(target, 200, []byte(body), "task")
		require.NoError(t, err)
		require.Equal(t, "task", result.TaskID)
		require.Empty(t, result.Status)
		require.NotEmpty(t, result.MetadataError)
	}
}

// 上游产物的 ID 不是任务 ID；只取唯一视频产物的真实时长，并识别返回的实际分辨率。
func TestVideoOfficialKlingOutputMetadata(t *testing.T) {
	target := VideoUpstreamTarget{Version: 2, ProtocolAdapter: "kling_v1", Endpoint: VideoEndpointKling, Model: "m", CreatePath: "/text-to-video/m"}
	result, err := ExtractVideoProtocolResponse(target, 200, []byte(`{"data":[{"id":"task","status":"completed","settings":{"resolution":"1080p"},"outputs":[{"id":"poster","type":"image","url":"https://cdn.example/poster"},{"id":"artifact","type":"video","url":"https://cdn.example/video","duration":"4.5"}]}]}`), "task")
	require.NoError(t, err)
	require.Empty(t, result.MetadataError)
	require.Equal(t, "completed", result.Status)
	require.Equal(t, "https://cdn.example/video", result.VideoURL)
	require.Equal(t, 4.5, result.Metadata.DurationSeconds)
	require.Equal(t, "1080p", result.Metadata.Resolution)
	require.Nil(t, result.Metadata.Tokens)
	result, err = ExtractVideoProtocolResponse(target, 200, []byte(`{"id":"task","status":"completed","usage":{"completion_tokens":0},"outputs":[{"type":"video","url":"https://cdn.example/video","duration":"5"}]}`), "task")
	require.NoError(t, err)
	require.Nil(t, result.Metadata.Tokens, "官方未定义的零 Token 扩展不能触发免费结算")
}
