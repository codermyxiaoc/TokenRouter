//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 同一账号后来切换方案时，旧快照的鉴权和查询地址仍不改变，也不能通过测试取消重复生成。
func TestVideoProtocolOptionViduFrozenAuthentication(t *testing.T) {
	for _, adapter := range []string{VideoProtocolAdapterKemei, VideoProtocolAdapterLegacy, "vidu_v2"} {
		t.Run(adapter, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointVidu)
			target := viduProtocolFixture("/ent/v2/img2video")
			target.AccountID, target.ProtocolAdapter = account.ID, adapter
			raw, err := json.Marshal(target)
			require.NoError(t, err)
			var restored VideoUpstreamTarget
			require.NoError(t, json.Unmarshal(raw, &restored))
			fixture := &videoAccountTestHTTP{handle: func(_ *http.Request, _ []byte) (*http.Response, error) {
				return videoAccountTestJSON(http.StatusOK, `{"task_id":"task-owned","state":"created"}`), nil
			}}
			upstream := NewVideoUpstreamService(&OpenAIGatewayService{httpUpstream: fixture})
			_, err = upstream.Submit(context.Background(), &VideoUpstreamSelection{Account: account, Target: restored, Body: []byte(`{"model":"viduq3-pro","images":["reference"]}`)})
			require.NoError(t, err)
			next := "vidu_v2"
			if adapter == next {
				next = VideoProtocolAdapterKemei
			}
			account.Credentials["video_endpoint_adapters"] = map[string]string{"vidu": next}
			account.Credentials["base_url"] = "https://changed.example"
			account.Credentials["video_vidu_model_paths"] = map[string]string{target.Model: "/ent/v2/text2video"}
			_, err = upstream.Poll(context.Background(), account, restored, "task-owned")
			require.NoError(t, err)
			require.Len(t, fixture.requests, 2)
			wantAuth := "Bearer fixture-secret"
			if adapter == "vidu_v2" {
				wantAuth = "Token fixture-secret"
			}
			for _, request := range fixture.requests {
				require.Equal(t, wantAuth, request.Header.Get("Authorization"))
				require.Equal(t, "vidu.example", request.URL.Host)
			}
			require.Equal(t, "/ent/v2/img2video", fixture.requests[0].URL.Path)
			require.Equal(t, "/ent/v2/tasks/task-owned/creations", fixture.requests[1].URL.Path)
			_, err = upstream.Cancel(context.Background(), account, restored, "task-owned")
			require.Error(t, err)
			require.Len(t, fixture.requests, 2)
		})
	}
}

// 每个原生端点都有明确的官方和原样方案；方案不能隐式授权另一种端点。
func TestVideoProtocolOptionEndpointAvailability(t *testing.T) {
	official := map[VideoEndpoint]string{
		VideoEndpointCompat:       "openai_v1",
		VideoEndpointOpenAIVideos: "openai_v1", VideoEndpointSeedance: "seedance_v1",
		VideoEndpointMiniMax: "minimax_v2", VideoEndpointWan: "wan_v1",
		VideoEndpointKling: "kling_v1", VideoEndpointVidu: "vidu_v2",
	}
	for endpoint, adapter := range official {
		t.Run(string(endpoint), func(t *testing.T) {
			require.True(t, ValidVideoProtocolAdapter(endpoint, VideoProtocolAdapterLegacy))
			require.True(t, ValidVideoProtocolAdapter(endpoint, adapter))
			for other, otherAdapter := range official {
				if other != endpoint && otherAdapter != adapter {
					require.False(t, ValidVideoProtocolAdapter(endpoint, otherAdapter), otherAdapter)
				}
			}
		})
	}
	require.True(t, ValidVideoProtocolAdapter(VideoEndpointCompat, VideoProtocolAdapterLegacy))
	require.False(t, ValidVideoProtocolAdapter(VideoEndpointVidu, "vidu_v999"))
	_, err := VideoTargetProtocolAdapter(VideoUpstreamTarget{Version: 1, Endpoint: VideoEndpointVidu})
	require.Error(t, err)
}

// OpenAI 字段合同与端点资格分开：兼容入口仍走原路径，旧兼容请求也不被默认转换。
func TestVideoProtocolOptionCompatOpenAIFieldsKeepEndpoint(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointCompat)
	account.Credentials["video_endpoint_adapters"] = map[string]string{"compat": VideoProtocolAdapterOpenAI}
	cfg, err := account.VideoConfiguration()
	require.NoError(t, err)
	_ = cfg
	upstream := NewVideoUpstreamService(&OpenAIGatewayService{})
	target, err := upstream.resolveTarget(context.Background(), account, "model")
	require.NoError(t, err)
	require.Equal(t, VideoEndpointCompat, target.Endpoint)
	require.Equal(t, VideoProtocolAdapterOpenAI, target.ProtocolAdapter)
	require.Equal(t, "/v1/video/generations", target.CreatePath)
	original := []byte(`{ "model":"model", "prompt":"p", "duration":8, "resolution":"720p", "ratio":"16:9", "images":["https://cdn.example/reference.png"], "extension":9007199254740993 }`)
	body, metadata, err := PrepareVideoProtocolRequest(original, "", target)
	require.NoError(t, err)
	require.Equal(t, "8", gjson.GetBytes(body, "seconds").String())
	require.Equal(t, "1280x720", gjson.GetBytes(body, "size").String())
	require.Equal(t, "https://cdn.example/reference.png", gjson.GetBytes(body, "input_reference.image_url").String())
	require.Equal(t, float64(8), metadata.DurationSeconds)
	require.Equal(t, 1, *metadata.ReferenceImageCount)
	completed, err := ExtractVideoProtocolResponse(target, 200, []byte(`{"id":"owned","status":"completed","seconds":"4","size":"1280x720","usage":{"completion_tokens":123}}`), "owned")
	require.NoError(t, err)
	require.Empty(t, completed.MetadataError)
	require.Equal(t, float64(4), completed.Metadata.DurationSeconds)
	require.Equal(t, "1280x720", completed.Metadata.Resolution)
	require.Equal(t, int64(123), *completed.Metadata.Tokens)
	account.Credentials["video_endpoint_adapters"] = map[string]string{"compat": VideoProtocolAdapterKemei}
	path, err := VideoProtocolTaskPath(target, "owned")
	require.NoError(t, err)
	require.Equal(t, "/v1/video/generations/owned", path)
	legacy := target
	legacy.ProtocolAdapter = VideoProtocolAdapterLegacy
	body, metadata, err = PrepareVideoProtocolRequest(original, "", legacy)
	require.NoError(t, err)
	require.Equal(t, original, body)
	require.Equal(t, "720p", metadata.Resolution)
	require.Equal(t, float64(8), metadata.DurationSeconds)
	for _, invalid := range []string{
		`{"model":"model","seconds":"8","duration":4}`, `{"model":"model","seconds":"5"}`,
		`{"model":"model","size":"1280x720","ratio":"9:16"}`, `{"model":"model","images":["a","b"]}`,
	} {
		_, _, err = PrepareVideoProtocolRequest([]byte(invalid), "", target)
		require.Error(t, err, invalid)
	}
}

// 显式原样方案保留供应商扩展、数字精度和未知参数，不注入官方默认值或媒体角色。
func TestVideoProtocolOptionViduLegacyPreservesPayloadAndDimensions(t *testing.T) {
	target := viduProtocolFixture("/ent/v2/img2video")
	target.ProtocolAdapter = VideoProtocolAdapterLegacy
	body := []byte(`{ "model":"viduq3-pro", "images":["first","last"], "sounds":["sound"], "duration":9.25, "resolution":"720P", "aspect_ratio":"9:16", "extension":{"counter":9007199254740993} }`)
	actual, metadata, err := PrepareVideoProtocolRequest(body, "", target)
	require.NoError(t, err)
	require.Equal(t, body, actual)
	require.Equal(t, 9.25, metadata.DurationSeconds)
	require.Equal(t, "720P", metadata.Resolution)
	require.NotNil(t, metadata.ReferenceImageCount)
	require.Equal(t, 2, *metadata.ReferenceImageCount)
	require.False(t, metadata.HasReferenceVideo)
	minimal := []byte(`{"model":"viduq3-pro","extension":true}`)
	actual, metadata, err = PrepareVideoProtocolRequest(minimal, "", target)
	require.NoError(t, err)
	require.Equal(t, minimal, actual)
	require.Zero(t, metadata.DurationSeconds)
	require.Empty(t, metadata.Resolution)
}

// 主体库不能被当成零张参考图；原样方案与官方方案都必须触发图片附加费的未知数量门禁。
func TestVideoProtocolOptionViduSubjectImageBillingGuard(t *testing.T) {
	for _, adapter := range []string{VideoProtocolAdapterLegacy, "vidu_v2"} {
		for _, media := range []string{
			`"subjects":{"name":"hero","images":["first","last"]}`,
			`"subjects":[{"name":"saved-subject"}]`,
			`"images":["first"],"auto_subjects":true`,
		} {
			t.Run(adapter+"/"+media, func(t *testing.T) {
				target := viduProtocolFixture("/ent/v2/reference2video")
				target.ProtocolAdapter = adapter
				body := []byte(`{ "model":"viduq3-pro", "prompt":"p", "duration":5, "resolution":"720p", ` + media + ` }`)
				actual, metadata, err := PrepareVideoProtocolRequest(body, "", target)
				require.NoError(t, err)
				if adapter == VideoProtocolAdapterLegacy {
					require.Equal(t, body, actual)
				}
				require.Nil(t, metadata.ReferenceImageCount)
				price, imagePrice := 1.0, .15
				group := &Group{ID: 3, Platform: PlatformVideo, ModelPricing: []ChannelModelPricing{{
					Platform: PlatformVideo, Models: []string{target.Model}, BillingMode: BillingModeVideo,
					VideoPrices:            []VideoPriceTier{{Resolution: "720p", Price: &price}},
					VideoImageInputPricing: &VideoImageInputPricing{Price: &imagePrice},
				}}}
				input := VideoPriceInput{Group: group, Model: metadata.Model, Resolution: metadata.Resolution,
					ReferenceImageCount: metadata.ReferenceImageCount, RateMultiplier: 1}
				_, err = NewModelPricingResolver(nil, nil).QuoteVideo(context.Background(), input)
				require.ErrorIs(t, err, ErrVideoUsageUnavailable)
				// 没有配置图片附加费时，未知图片数不破坏原有的视频秒价报价。
				group.ModelPricing[0].VideoImageInputPricing = nil
				_, err = NewModelPricingResolver(nil, nil).QuoteVideo(context.Background(), input)
				require.NoError(t, err)
			})
		}
	}
}

// 旧可美快照、显式原样和新官方都使用自己的冻结方案；未知取消状态不能猜测为可退款终态。
func TestVideoProtocolOptionViduLifecycleSnapshots(t *testing.T) {
	for _, adapter := range []string{VideoProtocolAdapterKemei, VideoProtocolAdapterLegacy, "vidu_v2"} {
		for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoPerRequest, BillingModeVideoToken} {
			for _, state := range []string{"success", "failed", "cancelled"} {
				t.Run(adapter+"/"+string(mode)+"/"+state, func(t *testing.T) {
					svc, key, upstream, billing, logs := newVideoLifecycleFixture(mode)
					target := viduProtocolFixture("/ent/v2/img2video")
					target.Model, target.AccountID, target.ProtocolAdapter = "video-model", upstream.selection.Account.ID, adapter
					upstream.selection.Target = target
					var err error
					upstream.submit, err = ExtractVideoProtocolResponse(target, 200, []byte(`{"task_id":"upstream-task","state":"created","duration":8,"resolution":"720p"}`), "")
					require.NoError(t, err)
					request := VideoTaskSubmitRequest{Native: true, NativePath: target.CreatePath, InboundProtocol: "vidu", IdempotencyKey: "vidu-option",
						Body: []byte(`{"model":"video-model","images":["reference"],"duration":8,"resolution":"720p"}`)}
					created, err := svc.Submit(context.Background(), key, request)
					require.NoError(t, err)
					// 管理员随后切换账号方案、地址和操作，新协调器仍只能沿原任务快照查询。
					upstream.selection.Account.Credentials = map[string]any{"base_url": "https://changed.example", "video_endpoints": []string{"vidu"},
						"video_endpoint_adapters": map[string]string{"vidu": "vidu_v2"}, "video_vidu_model_paths": map[string]string{"video-model": "/ent/v2/text2video"}}
					body := []byte(`{"task_id":"upstream-task","state":"` + state + `","duration":8,"resolution":"720p"}`)
					if state == "success" {
						body = []byte(`{"task_id":"upstream-task","state":"success","duration":8,"resolution":"720p","credits":120,"creations":[{"id":"asset-only","url":"https://cdn.example/video.mp4"}]}`)
					}
					upstream.poll, err = ExtractVideoProtocolResponse(target, 200, body, "upstream-task")
					require.NoError(t, err)
					restarted := *svc
					for range 3 {
						task, getErr := svc.repo.Get(context.Background(), created.LocalTaskID)
						require.NoError(t, getErr)
						restarted.advance(context.Background(), task)
					}
					task, err := svc.repo.Get(context.Background(), created.LocalTaskID)
					require.NoError(t, err)
					require.Equal(t, target, task.Target)
					require.Equal(t, target, upstream.lastTarget)
					switch {
					case state == "failed":
						require.Equal(t, "released", task.BillingStatus)
						require.Equal(t, 1, billing.releases)
						require.Zero(t, billing.captures)
					case state == "success" && mode != BillingModeVideoToken:
						require.Equal(t, "settled", task.BillingStatus)
						require.Equal(t, 1, billing.captures)
						require.Zero(t, billing.releases)
						require.Len(t, logs.logs, 1)
						want := 16.0
						if mode == BillingModeVideoPerRequest {
							want = 2
						}
						require.InDelta(t, want, task.BillingResult.ActualAmountUSD, 1e-9)
					default:
						require.Zero(t, billing.captures+billing.releases)
						require.Empty(t, logs.logs)
						require.NotContains(t, []string{"released", "settled"}, task.BillingStatus)
					}
					_, err = svc.Submit(context.Background(), key, request)
					require.NoError(t, err)
					require.Equal(t, 1, upstream.submitted)
					require.Equal(t, 1, billing.reserves)
				})
			}
		}
	}
}

// 无 ID、冲突 ID 和其他任务的响应不能触发费用动作，也不能授权再次生成。
func TestVideoProtocolOptionViduUncertainCreation(t *testing.T) {
	for _, adapter := range []string{VideoProtocolAdapterKemei, VideoProtocolAdapterLegacy, "vidu_v2"} {
		for _, tc := range []struct {
			name        string
			body        string
			conflicting bool
		}{
			{name: "missing-id", body: `{"state":"created"}`},
			{name: "conflicting-ids", body: `{"id":"task-a","task_id":"task-b","state":"created"}`, conflicting: true},
		} {
			t.Run(adapter+"/"+tc.name, func(t *testing.T) {
				svc, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideo)
				target := viduProtocolFixture("/ent/v2/img2video")
				target.Model, target.AccountID, target.ProtocolAdapter = "video-model", upstream.selection.Account.ID, adapter
				upstream.selection.Target = target
				var err error
				upstream.submit, err = ExtractVideoProtocolResponse(target, 200, []byte(tc.body), "")
				require.NoError(t, err)
				request := VideoTaskSubmitRequest{Native: true, NativePath: target.CreatePath, InboundProtocol: "vidu", IdempotencyKey: "unknown-vidu", Body: []byte(`{"model":"video-model"}`)}
				response, err := svc.Submit(context.Background(), key, request)
				require.NoError(t, err)
				if tc.conflicting {
					require.Equal(t, http.StatusBadGateway, response.StatusCode)
					require.NotContains(t, string(response.Body), "task-a")
					require.NotContains(t, string(response.Body), "task-b")
				}
				task, err := svc.repo.Get(context.Background(), response.LocalTaskID)
				require.NoError(t, err)
				require.Empty(t, task.UpstreamTaskID)
				require.Equal(t, "submission_unknown", task.Status)
				require.Equal(t, "reconciliation", task.BillingStatus)
				for range 3 {
					svc.advance(context.Background(), task)
				}
				require.Zero(t, billing.captures+billing.releases)
				require.Zero(t, upstream.polls)
				require.Empty(t, logs.logs)
				response, err = svc.Submit(context.Background(), key, request)
				if tc.conflicting {
					// 冲突创建包不能原样重放给用户；重复提交返回归属错误且继续保留预扣。
					require.ErrorIs(t, err, errVideoTaskResponseMismatch)
					require.Nil(t, response)
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, 1, upstream.submitted)
				require.Equal(t, 1, billing.reserves)
				require.Zero(t, upstream.polls+upstream.cancels+billing.captures+billing.releases)
				require.Empty(t, logs.logs)
				_, err = ExtractVideoProtocolResponse(target, 200, []byte(`{"task_id":"other-task","state":"failed"}`), "owned-task")
				require.Error(t, err)
			})
		}
	}
}
