package service

import (
	"context"
	"net/http"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// 同一模型经不同入站协议选中上游后，取消端点勾选不能影响已受理任务的恢复与结算。
func TestVideoTaskAdaptiveEndpointsFreezeSelectionThroughSettlement(t *testing.T) {
	s, key, _, billing, logs := newVideoLifecycleFixture(BillingModeVideoToken)
	key.Group.Status = StatusActive
	account := videoFixtureAccount(VideoEndpointCompat)
	account.GroupIDs = []int64{key.Group.ID}
	account.Credentials["video_endpoints"] = []string{"compat", "seedance", "openai_videos"}
	account.Credentials["video_model_bindings"] = map[string]any{"video-model": []string{"compat", "seedance", "openai_videos"}}
	account.Credentials["video_base_urls"] = map[string]string{"compat": "https://compat.example", "seedance": "https://ark.example", "openai_videos": "https://videos.example"}
	account.Credentials["video_max_output_tokens"] = 1_000_000
	accounts := &videoSelectionFixture{accounts: []Account{*account}}
	transport := &videoHTTPFixture{status: http.StatusAccepted}
	s.upstream = NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: accounts, concurrencyService: NewConcurrencyService(nil), httpUpstream: transport})
	s.accounts = videoLifecycleAccounts{account: account}
	type taskExpectation struct {
		id, host, pollPath string
		endpoint           VideoEndpoint
	}
	var created []taskExpectation
	requests := []struct {
		req               VideoTaskSubmitRequest
		endpoint          VideoEndpoint
		host, path, rawID string
	}{
		{VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","prompt":"compat prompt","resolution":"720p","duration":8}`), InboundProtocol: "unified", NativePath: "/v1/video/generations"}, VideoEndpointCompat, "compat.example", "/v1/video/generations", "compat-task"},
		{VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","prompt":"videos prompt","resolution":"720p","duration":8}`), InboundProtocol: "openai_videos", NativePath: "/v1/videos"}, VideoEndpointOpenAIVideos, "videos.example", "/v1/videos", "videos-task"},
		{VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","content":[{"type":"text","text":"native prompt"}],"resolution":"720p","duration":8}`), InboundProtocol: "seedance", NativePath: "/api/v3/contents/generations/tasks", Native: true}, VideoEndpointSeedance, "ark.example", "/api/v3/contents/generations/tasks", "ark-task"},
	}
	for _, tc := range requests {
		transport.response = `{"id":"` + tc.rawID + `","status":"queued"}`
		response, err := s.Submit(context.Background(), key, tc.req)
		require.NoError(t, err)
		require.Equal(t, tc.host, transport.request.URL.Host)
		require.Equal(t, tc.path, transport.request.URL.Path)
		require.JSONEq(t, string(tc.req.Body), string(transport.requestBody))
		task, err := s.repo.Get(context.Background(), response.LocalTaskID)
		require.NoError(t, err)
		require.Equal(t, tc.endpoint, task.Target.Endpoint)
		created = append(created, taskExpectation{task.ID, tc.host, tc.path + "/" + tc.rawID, tc.endpoint})
	}
	require.Equal(t, len(requests), billing.reserves)
	// 管理员改了支持端点和地址，不得改变已受理任务的轮询地址或重新发送生成请求。
	account.Credentials["video_endpoints"] = []string{"minimax"}
	account.Credentials["base_url"] = "https://changed.example"
	account.Credentials["video_base_urls"] = map[string]string{"minimax": "https://changed.example"}
	transport.status = http.StatusOK
	transport.response = `{"status":"completed","duration":8,"resolution":"720p","usage":{"completion_tokens":100},"data":[{"url":"https://cdn.example/video.mp4"}]}`
	for _, expected := range created {
		task, err := s.repo.Get(context.Background(), expected.id)
		require.NoError(t, err)
		s.advance(context.Background(), task)
		require.Equal(t, http.MethodGet, transport.request.Method)
		require.Equal(t, expected.host, transport.request.URL.Host)
		require.Equal(t, expected.pollPath, transport.request.URL.Path)
		require.Equal(t, "settled", task.BillingStatus)
		require.True(t, task.EffectsDone)
		require.Equal(t, expected.endpoint, task.Target.Endpoint)
		require.Equal(t, 100, logs.logs["video_capture:"+task.ID].OutputTokens)
		require.InDelta(t, 0.0002, task.BillingResult.ActualAmountUSD, 1e-10)
	}
	require.Equal(t, len(requests), billing.captures)
	require.Equal(t, len(requests)*2, transport.calls)
	require.Len(t, logs.logs, len(requests))
}

// 未明确允许的统一入口必须在建任务和预扣之前拒绝，不能产生付费 POST 或冻结余额。
func TestVideoTaskDisabledUnifiedEndpointHasNoBillingEffects(t *testing.T) {
	for _, tc := range []struct {
		name, protocol string
		endpoints      []string
		bindings       map[string]any
	}{
		{"native_only_compat", "unified", []string{"seedance"}, nil},
		{"native_only_videos", "openai_videos", []string{"seedance"}, nil},
		{"compat_does_not_grant_videos", "openai_videos", []string{"compat"}, nil},
		{"videos_does_not_grant_compat", "unified", []string{"openai_videos"}, nil},
		{"binding_excludes_compat", "unified", []string{"compat", "seedance"}, map[string]any{"video-model": "seedance"}},
		{"binding_excludes_videos", "openai_videos", []string{"openai_videos", "seedance"}, map[string]any{"video-model": "seedance"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, key, _, billing, logs := newVideoLifecycleFixture(BillingModeVideo)
			key.Group.Status = StatusActive
			account := videoFixtureAccount(VideoEndpointSeedance)
			account.GroupIDs = []int64{key.Group.ID}
			account.Credentials["video_endpoints"] = tc.endpoints
			if tc.bindings != nil {
				account.Credentials["video_model_bindings"] = tc.bindings
			}
			accounts := &videoSelectionFixture{accounts: []Account{*account}}
			transport := &videoHTTPFixture{status: http.StatusAccepted, response: `{"id":"must-not-submit","status":"queued"}`}
			s.upstream = NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: accounts, concurrencyService: NewConcurrencyService(nil), httpUpstream: transport})
			_, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","duration":8,"resolution":"720p"}`), InboundProtocol: tc.protocol})
			require.Error(t, err)
			require.Equal(t, "VIDEO_ENDPOINT_MODEL_MISMATCH", infraerrors.Reason(err))
			require.Empty(t, s.repo.(*videoLifecycleRepo).tasks)
			require.Zero(t, transport.calls)
			require.Zero(t, billing.reserves)
			require.Zero(t, billing.captures)
			require.Empty(t, logs.logs)
		})
	}
}
