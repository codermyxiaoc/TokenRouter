package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// 模型覆盖必须匹配最终上游模型；旧账号和显式保留旧协议都不能被新默认值迁移。
func TestVideoProtocolAdapterConfigurationPriority(t *testing.T) {
	for _, tc := range []struct {
		name, profile, endpointAdapter, overrideModel, overrideAdapter string
		wantAdapter, wantProfile                                       string
		wantVersion                                                    int
	}{
		{name: "old_default", wantVersion: 1},
		{name: "old_h3", profile: "metaso_h3", wantProfile: "metaso_h3", wantVersion: 1},
		{name: "endpoint_official", endpointAdapter: "openai_v1", wantAdapter: "openai_v1", wantVersion: 2},
		{name: "final_model_override", endpointAdapter: "openai_v1", overrideModel: "provider-model", overrideAdapter: "metaso_h3_v1", wantAdapter: "metaso_h3_v1", wantVersion: 2},
		{name: "requested_alias_cannot_override", endpointAdapter: "openai_v1", overrideModel: "public-model", overrideAdapter: "metaso_h3_v1", wantAdapter: "openai_v1", wantVersion: 2},
		{name: "explicit_legacy_overrides_old_h3", profile: "metaso_h3", endpointAdapter: "legacy_v1", wantAdapter: "legacy_v1", wantVersion: 2},
		{name: "model_override_without_endpoint_default", overrideModel: "provider-model", overrideAdapter: "openai_v1", wantAdapter: "openai_v1", wantVersion: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointOpenAIVideos)
			account.Credentials["model_mapping"] = map[string]any{"public-model": "provider-model"}
			account.Credentials["model_whitelist"] = []string{"provider-model"}
			account.Credentials["video_model_bindings"] = map[string]string{"provider-model": "openai_videos"}
			if tc.profile != "" {
				account.Credentials["video_openai_profile"] = tc.profile
			}
			if tc.endpointAdapter != "" {
				account.Credentials["video_endpoint_adapters"] = map[string]string{"openai_videos": tc.endpointAdapter}
			}
			if tc.overrideAdapter != "" {
				account.Credentials["video_model_adapters"] = map[string]any{tc.overrideModel: map[string]string{"openai_videos": tc.overrideAdapter}}
			}
			repo := &videoSelectionFixture{accounts: []Account{*account}}
			upstream := NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: repo, concurrencyService: NewConcurrencyService(nil)})
			key := &APIKey{GroupID: &account.GroupIDs[0], Group: &Group{ID: account.GroupIDs[0], Platform: PlatformVideo, Status: StatusActive}}
			selected, err := upstream.SelectAccount(context.Background(), key, VideoTaskSubmitRequest{
				InboundProtocol: "openai_videos", Body: []byte(`{"model":"public-model","prompt":"固定协议测试","seconds":"8","size":"1280x720"}`),
			})
			require.NoError(t, err)
			if selected.Release != nil {
				defer selected.Release()
			}
			require.Equal(t, "provider-model", selected.Target.Model)
			require.Equal(t, tc.wantVersion, selected.Target.Version)
			require.Equal(t, tc.wantAdapter, selected.Target.ProtocolAdapter)
			require.Equal(t, tc.wantProfile, selected.Target.OpenAIProfile)
			var payload map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(selected.Body, &payload))
			require.JSONEq(t, `"provider-model"`, string(payload["model"]))
		})
	}
}

// 只增加适配器配置不能绕过账号端点和最终模型白名单的独立授权。
func TestVideoProtocolAdapterDoesNotGrantEndpoint(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointSeedance)
	account.Credentials["video_endpoint_adapters"] = map[string]string{"openai_videos": "openai_v1"}
	account.Credentials["video_model_adapters"] = map[string]any{"video-model": map[string]string{"openai_videos": "openai_v1"}}
	cfg, err := account.VideoConfiguration()
	require.NoError(t, err)
	_, err = cfg.SelectEndpoint("video-model", "openai_videos", false)
	require.Error(t, err)
	endpoint, err := cfg.SelectEndpoint("video-model", "seedance", true)
	require.NoError(t, err)
	require.Equal(t, VideoEndpointSeedance, endpoint)
	id, explicit := cfg.ProtocolAdapter("video-model", endpoint)
	require.Equal(t, "legacy_v1", id)
	require.False(t, explicit)
}

// 非法配置须在保存或调度前失败，不能默默回退成另一种会产生费用的请求协议。
func TestVideoProtocolAdapterRejectsMalformedConfiguration(t *testing.T) {
	for name, raw := range map[string]string{
		"endpoint_unknown":       `{"video_endpoint_adapters":{"unexpected":"legacy_v1"}}`,
		"adapter_unknown":        `{"video_endpoint_adapters":{"openai_videos":"openai_v999"}}`,
		"adapter_empty":          `{"video_endpoint_adapters":{"openai_videos":""}}`,
		"adapter_wrong_endpoint": `{"video_endpoint_adapters":{"minimax":"openai_v1"}}`,
		"adapter_number":         `{"video_endpoint_adapters":{"openai_videos":42}}`,
		"endpoint_list":          `{"video_endpoint_adapters":["openai_v1"]}`,
		"model_wildcard":         `{"video_model_adapters":{"gpt-*":{"openai_videos":"openai_v1"}}}`,
		"model_space":            `{"video_model_adapters":{" gpt-model":{"openai_videos":"openai_v1"}}}`,
		"model_empty":            `{"video_model_adapters":{"":{"openai_videos":"openai_v1"}}}`,
		"model_wrong_shape":      `{"video_model_adapters":{"model":"openai_v1"}}`,
		"model_unknown_adapter":  `{"video_model_adapters":{"model":{"openai_videos":"custom_script"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			a := videoFixtureAccount(VideoEndpointOpenAIVideos)
			var values map[string]any
			require.NoError(t, json.Unmarshal([]byte(raw), &values))
			for k, v := range values {
				a.Credentials[k] = v
			}
			_, err := a.VideoConfiguration()
			require.Error(t, err)
		})
	}
}

// 付费重试只能迁移到相同版本的协议；适配器不同即使端点和金额维度相同也不可重提。
func TestVideoProtocolAdapterRetryPreservesSingleHold(t *testing.T) {
	for _, compatible := range []bool{false, true} {
		t.Run(map[bool]string{false: "different_adapter_refunds", true: "same_adapter_settles"}[compatible], func(t *testing.T) {
			s, key, upstream, billing, logs, repo := newVideoRetryFixture()
			upstream.selections = upstream.selections[:2]
			for _, selected := range upstream.selections {
				selected.Target.Endpoint = VideoEndpointOpenAIVideos
				selected.Target.CreatePath = "/v1/videos"
				selected.Target.Version = 2
				selected.Target.ProtocolAdapter = "openai_v1"
			}
			if !compatible {
				upstream.selections[1].Target.ProtocolAdapter = "metaso_h3_v1"
			}
			upstream.responses = []*VideoUpstreamResponse{
				retryVideoResponse(http.StatusTooManyRequests, `{"error":{"code":"QuotaExceeded"}}`),
				retryVideoResponse(http.StatusOK, `{"id":"retry-adapter-task","status":"queued"}`),
			}
			response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model"}`), InboundProtocol: "openai_videos"})
			require.NoError(t, err)
			require.Equal(t, 1, billing.reserves)
			task, err := repo.Get(context.Background(), response.LocalTaskID)
			require.NoError(t, err)
			if compatible {
				require.Len(t, upstream.attempted, 2)
				require.Len(t, task.RejectedAttempts, 1)
				require.Equal(t, "openai_v1", task.Target.ProtocolAdapter)
				require.Equal(t, "openai_v1", task.RejectedAttempts[0].Target.ProtocolAdapter)
				upstream.poll = retryVideoResponse(http.StatusOK, `{"id":"retry-adapter-task","status":"completed","duration":8}`)
				s.advance(context.Background(), task)
				s.advance(context.Background(), task)
				require.Equal(t, 1, billing.captures)
				require.Zero(t, billing.releases)
				require.Len(t, logs.logs, 1)
			} else {
				require.Len(t, upstream.attempted, 1)
				require.Empty(t, task.RejectedAttempts)
				require.Equal(t, "released", task.BillingStatus)
				require.Equal(t, 1, billing.releases)
				require.Zero(t, billing.captures)
				require.Empty(t, logs.logs)
			}
		})
	}
}

// 复用真正选号与传输层，模拟落盘重启后编辑账号和价格，验证各版本合同不会漂移。
func TestVideoProtocolAdapterFrozenLifecycleAndBilling(t *testing.T) {
	for _, adapter := range []string{"", "legacy_v1", "openai_v1", "metaso_h3_v1"} {
		for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoToken, BillingModeVideoPerRequest} {
			for _, status := range []string{"completed", "failed"} {
				t.Run(adapter+"/"+string(mode)+"/"+status, func(t *testing.T) {
					s, key, _, billing, logs := newVideoLifecycleFixture(mode)
					key.Group.Status = StatusActive
					price, prepay := .3, .3
					wantHold, wantCharge := 4.8, 2.4
					if mode == BillingModeVideoToken {
						price, wantHold, wantCharge = 3, 2.4, .6
					} else if mode == BillingModeVideoPerRequest {
						price, wantHold, wantCharge = 1.25, 2.5, 2.5
					}
					tier := "1280x720"
					if adapter == "metaso_h3_v1" {
						tier = "768p"
					}
					pricing := ChannelModelPricing{Platform: PlatformVideo, Models: []string{"video-model"}, BillingMode: mode,
						VideoPrices: []VideoPriceTier{{Resolution: tier, Price: &price}}}
					if mode == BillingModeVideoToken {
						pricing.VideoTokenPrepay = &VideoTokenPrepayConfig{PricePerSecond: &prepay}
					}
					key.Group.ModelPricing = []ChannelModelPricing{pricing}
					a := videoFixtureAccount(VideoEndpointOpenAIVideos)
					a.GroupIDs = []int64{key.Group.ID}
					a.Credentials["video_base_urls"] = map[string]string{"openai_videos": "https://frozen.example"}
					if adapter != "" {
						a.Credentials["video_endpoint_adapters"] = map[string]string{"openai_videos": adapter}
					}
					httpFixture := &videoHTTPFixture{status: http.StatusAccepted, response: `{"id":"frozen-adapter-task","status":"queued"}`}
					s.upstream = NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: &videoSelectionFixture{accounts: []Account{*a}},
						concurrencyService: NewConcurrencyService(nil), httpUpstream: httpFixture})
					s.accounts = videoLifecycleAccounts{account: a}
					req := VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","prompt":"冻结合同回归","seconds":"8","size":"1280x720"}`),
						InboundProtocol: "openai_videos", IdempotencyKey: "frozen-adapter-request"}
					created, err := s.Submit(context.Background(), key, req)
					require.NoError(t, err)
					task, err := s.repo.Get(context.Background(), created.LocalTaskID)
					require.NoError(t, err)
					require.Equal(t, adapter, task.Target.ProtocolAdapter)
					require.InDelta(t, wantHold, task.Hold.HoldAmount, 1e-9)
					frozen := task.Target
					quote, err := json.Marshal(task.Quote)
					require.NoError(t, err)
					// 管理员更换端点、地址、适配器和单价不改变原任务的账务与路由快照。
					a.Credentials["video_endpoints"] = []string{"minimax"}
					a.Credentials["video_base_urls"] = map[string]string{"minimax": "https://changed.example"}
					a.Credentials["video_endpoint_adapters"] = map[string]string{"minimax": "minimax_v2"}
					*key.Group.ModelPricing[0].VideoPrices[0].Price = 99
					httpFixture.status = http.StatusOK
					httpFixture.response = `{"id":"frozen-adapter-task","status":"` + status + `","seconds":"4","size":"1280x720","usage":{"completion_tokens":100000}}`
					restarted := *s
					restarted.advance(context.Background(), task)
					fresh, err := s.repo.Get(context.Background(), task.ID)
					require.NoError(t, err)
					require.Equal(t, frozen, fresh.Target)
					afterQuote, err := json.Marshal(fresh.Quote)
					require.NoError(t, err)
					require.Equal(t, quote, afterQuote)
					require.Equal(t, http.MethodGet, httpFixture.request.Method)
					require.Equal(t, "frozen.example", httpFixture.request.URL.Host)
					require.Equal(t, "/v1/videos/frozen-adapter-task", httpFixture.request.URL.Path)
					require.True(t, fresh.EffectsDone)
					if status == "completed" {
						require.Equal(t, "settled", fresh.BillingStatus)
						require.InDelta(t, wantCharge, fresh.BillingResult.ActualAmountUSD, 1e-9)
						require.Equal(t, 1, billing.captures)
						require.Zero(t, billing.releases)
						require.Len(t, logs.logs, 1)
					} else {
						require.Equal(t, "released", fresh.BillingStatus)
						require.Equal(t, 1, billing.releases)
						require.Zero(t, billing.captures)
						require.Empty(t, logs.logs)
					}
					_, err = s.Submit(context.Background(), key, req)
					require.NoError(t, err)
					restarted.advance(context.Background(), fresh)
					require.Equal(t, 1, billing.reserves)
					require.Equal(t, 2, httpFixture.calls, "幂等重放和完成恢复不得再发生成或轮询")
				})
			}
		}
	}
}

// 不认识的快照不能调用上游，也不能用旧响应宽松解析后完成扣款或退款。
func TestVideoProtocolAdapterInvalidSnapshotFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, adapter string
		version       int
	}{
		{name: "future_version", version: 99, adapter: "openai_v1"},
		{name: "future_adapter", version: 2, adapter: "openai_v999"},
		{name: "missing_v2_adapter", version: 2},
		{name: "wrong_endpoint_adapter", version: 2, adapter: "minimax_v2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := videoFixtureAccount(VideoEndpointOpenAIVideos)
			httpFixture := &videoHTTPFixture{status: http.StatusOK, response: `{"id":"must-not-send","status":"completed","seconds":"4","size":"1280x720"}`}
			u := NewVideoUpstreamService(&OpenAIGatewayService{concurrencyService: NewConcurrencyService(nil), httpUpstream: httpFixture})
			target := VideoUpstreamTarget{Version: tc.version, ProtocolAdapter: tc.adapter, Endpoint: VideoEndpointOpenAIVideos,
				AccountID: a.ID, BaseURL: "https://frozen.example", CreatePath: "/v1/videos", Model: "video-model"}
			_, err := u.Submit(context.Background(), &VideoUpstreamSelection{Account: a, Target: target, Body: []byte(`{"model":"video-model"}`)})
			require.Error(t, err)
			_, err = u.Poll(context.Background(), a, target, "frozen-task")
			require.Error(t, err)
			require.Zero(t, httpFixture.calls)
			// 未知快照的旧错误包也不能被恢复器当成明确未受理后退钱。
			s, key, oldUpstream, billing, logs := newVideoLifecycleFixture(BillingModeVideo)
			created, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model"}`)})
			require.NoError(t, err)
			task, err := s.repo.Get(context.Background(), created.LocalTaskID)
			require.NoError(t, err)
			target.AccountID = task.AccountID
			task.Target, task.Status, task.UpstreamTaskID = target, "submission_unknown", ""
			task.CreateResponseStatus = http.StatusBadRequest
			task.CreateResponse = []byte(`{"error":{"code":"invalid_request"}}`)
			task.RawResponse = task.CreateResponse
			require.NoError(t, s.repo.Save(context.Background(), task, false))
			s.advance(context.Background(), task)
			require.Zero(t, billing.captures)
			require.Zero(t, billing.releases)
			require.Zero(t, oldUpstream.polls)
			require.False(t, task.EffectsDone)
			require.Empty(t, logs.logs)
		})
	}
}
