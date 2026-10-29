package service

import (
	"context"
	"net/http"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// 统一入口按提交时的模型绑定选中上游；之后改绑协议、端点或地址都不能影响已受理任务的恢复与结算。
func TestVideoTaskAdaptiveEndpointsFreezeSelectionThroughSettlement(t *testing.T) {
	s, key, _, billing, logs := newVideoLifecycleFixture(BillingModeVideoToken)
	key.Group.Status = StatusActive
	account := videoFixtureAccount(VideoEndpointCompat)
	account.GroupIDs = []int64{key.Group.ID}
	delete(account.Credentials, "video_endpoints")
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
		protocol, base    string
		endpoint          VideoEndpoint
		host, path, rawID string
	}{
		{VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","prompt":"compat prompt","resolution":"720p","duration":8}`), InboundProtocol: "openai_videos"}, "compat.legacy_v1", "https://compat.example", VideoEndpointCompat, "compat.example", "/v1/video/generations", "compat-task"},
		{VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","prompt":"videos prompt","resolution":"720p","duration":8}`), InboundProtocol: "openai_videos"}, "openai_videos.legacy_v1", "https://videos.example", VideoEndpointOpenAIVideos, "videos.example", "/v1/videos", "videos-task"},
		{VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","content":[{"type":"text","text":"native prompt"}],"resolution":"720p","duration":8}`), InboundProtocol: "openai_videos"}, "seedance.legacy_v1", "https://ark.example", VideoEndpointSeedance, "ark.example", "/api/v3/contents/generations/tasks", "ark-task"},
	}
	for _, tc := range requests {
		// 每次提交前改绑模型协议；入站入口始终是 /v1/videos，上游由绑定决定。
		account.Credentials["video_model_protocols"] = map[string]any{"video-model": map[string]any{"protocol": tc.protocol, "base_url": tc.base}}
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
	account.Credentials["video_model_protocols"] = map[string]any{"*": map[string]any{"protocol": "minimax.minimax_v2", "base_url": "https://changed.example"}}
	account.Credentials["base_url"] = "https://changed.example"
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

// 模型在账号上没有可用协议绑定时，统一入口必须在建任务和预扣之前拒绝，不能产生付费 POST 或冻结余额。
func TestVideoTaskDisabledUnifiedEndpointHasNoBillingEffects(t *testing.T) {
	for _, tc := range []struct {
		name      string
		endpoints []string
		protocols map[string]any
	}{
		{"legacy_kling_without_model_path", []string{"kling"}, nil},
		{"legacy_vidu_without_model_path", []string{"vidu"}, nil},
		{"binding_for_other_model", nil, map[string]any{"other-model": map[string]any{"protocol": "seedance.seedance_v1"}}},
		{"invalid_binding", nil, map[string]any{"video-model": map[string]any{"protocol": "seedance.openai_v1"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, key, _, billing, logs := newVideoLifecycleFixture(BillingModeVideo)
			key.Group.Status = StatusActive
			account := videoFixtureAccount(VideoEndpointSeedance)
			account.GroupIDs = []int64{key.Group.ID}
			delete(account.Credentials, "video_endpoints")
			if tc.endpoints != nil {
				account.Credentials["video_endpoints"] = tc.endpoints
			}
			if tc.protocols != nil {
				account.Credentials["video_model_protocols"] = tc.protocols
			}
			accounts := &videoSelectionFixture{accounts: []Account{*account}}
			transport := &videoHTTPFixture{status: http.StatusAccepted, response: `{"id":"must-not-submit","status":"queued"}`}
			s.upstream = NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: accounts, concurrencyService: NewConcurrencyService(nil), httpUpstream: transport})
			_, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","duration":8,"resolution":"720p"}`), InboundProtocol: "openai_videos"})
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
