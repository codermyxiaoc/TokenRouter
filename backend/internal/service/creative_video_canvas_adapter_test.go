//go:build unit

package service

import (
	"context"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 自动方向来自可信图片字节或原模型默认值，画布保存的 adaptive 始终不被改写。
func TestCreativeVideoCanvasAdaptiveDirection(t *testing.T) {
	for _, tc := range []struct {
		name, defaultRatio, want string
		width, height            int
	}{
		{"landscape", "9:16", "16:9", 24, 16},
		{"portrait", "16:9", "9:16", 16, 24},
		{"square", "9:16", "16:9", 16, 16},
		{"without_image_portrait", "9:16", "9:16", 0, 0},
		{"without_image_other_default", "1:1", "16:9", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := creativeVideoTestExecution()
			e.Params.AspectRatio = "adaptive"
			model := &CreativeModelPublic{VideoCapabilities: &CreativeVideoCapabilities{DefaultAspectRatio: tc.defaultRatio}}
			var assets []CreativeCanvasInputAsset
			if tc.width > 0 {
				assets = []CreativeCanvasInputAsset{{Asset: &CreativeCanvasAsset{MIMEType: "image/png"}, Content: makeTestPNG(t, tc.width, tc.height)}}
			}
			ratio, err := creativeVideoCanvasAdaptiveRatio(e, model, assets)
			require.NoError(t, err)
			require.Equal(t, tc.want, ratio)
			require.Equal(t, "adaptive", e.Params.AspectRatio)
			e.Params.AspectRatio = "4:3"
			ratio, err = creativeVideoCanvasAdaptiveRatio(e, model, assets)
			require.NoError(t, err)
			require.Empty(t, ratio)
		})
	}
}

// 真正的选号路径只让明确 H3 方案使用内部提示；普通 API、其它适配和显式比例不变。
func TestCreativeVideoCanvasAdaptiveSelectionIsScopedToMetaso(t *testing.T) {
	for _, tc := range []struct {
		name, profile, adapter, hint, ratio, resolution, size string
		endpoint                                              VideoEndpoint
		wantError                                             bool
	}{
		{name: "legacy_h3", profile: "metaso_h3", hint: "9:16", size: "720x1280"},
		{name: "explicit_h3", adapter: VideoProtocolAdapterMetasoH3, hint: "16:9", size: "1280x720"},
		{name: "ordinary_api", profile: "metaso_h3", wantError: true},
		{name: "invalid_internal_hint", profile: "metaso_h3", hint: "adaptive", wantError: true},
		{name: "explicit_ratio", profile: "metaso_h3", hint: "9:16", ratio: "16:9", size: "1280x720"},
		{name: "unsupported_tier_no_downgrade", profile: "metaso_h3", hint: "16:9", resolution: "720p", wantError: true},
		{name: "passthrough", hint: "9:16"},
		{name: "legacy_override", profile: "metaso_h3", adapter: VideoProtocolAdapterLegacy, hint: "9:16"},
		{name: "native_seedance", endpoint: VideoEndpointSeedance, adapter: VideoProtocolAdapterSeedance, hint: "9:16", resolution: "720p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := tc.endpoint
			if endpoint == "" {
				endpoint = VideoEndpointOpenAIVideos
			}
			account := videoFixtureAccount(endpoint)
			account.Credentials["video_openai_profile"] = tc.profile
			if tc.adapter != "" {
				account.Credentials["video_endpoint_adapters"] = map[string]string{string(endpoint): tc.adapter}
			}
			selector := NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: &videoSelectionFixture{accounts: []Account{*account}}, concurrencyService: NewConcurrencyService(nil)})
			key := &APIKey{GroupID: &account.GroupIDs[0], Group: &Group{ID: account.GroupIDs[0], Platform: PlatformVideo, Status: StatusActive}}
			ratio, resolution := tc.ratio, tc.resolution
			if ratio == "" {
				ratio = "adaptive"
			}
			if resolution == "" {
				resolution = "768p"
			}
			// JSON 中即使伪造同名提示，也不能替代未导出的服务端字段。
			raw := []byte(`{"model":"video-model","prompt":"fixture","duration":5,"resolution":"` + resolution + `","ratio":"` + ratio + `","images":["data:image/png;base64,AAAA"],"canvasAdaptiveRatio":"16:9"}`)
			request := VideoTaskSubmitRequest{Body: raw, InboundProtocol: string(endpoint), Native: endpoint == VideoEndpointSeedance, canvasAdaptiveRatio: tc.hint}
			selected, err := selector.SelectAccount(context.Background(), key, request)
			if tc.wantError {
				require.Error(t, err)
				require.Equal(t, "VIDEO_INVALID_OPENAI_PARAMETERS", infraerrors.Reason(err))
				return
			}
			require.NoError(t, err)
			defer selected.Release()
			require.Equal(t, ratio, gjson.GetBytes(request.Body, "ratio").String())
			if tc.size != "" {
				require.Equal(t, tc.size, gjson.GetBytes(selected.Body, "size").String())
				require.Equal(t, tc.size, selected.Metadata.Size)
				require.Equal(t, "768p", selected.Metadata.Resolution)
				require.False(t, gjson.GetBytes(selected.Body, "ratio").Exists())
			} else {
				require.Equal(t, ratio, gjson.GetBytes(selected.Body, "ratio").String())
			}
			require.Equal(t, float64(5), selected.Metadata.DurationSeconds)
			require.Equal(t, 1, *selected.Metadata.ReferenceImageCount)
		})
	}
}

type creativeAdaptiveVideoTransport struct {
	*videoLifecycleUpstream
	selector *VideoUpstreamService
	request  VideoTaskSubmitRequest
	selects  int
}

func (s *creativeAdaptiveVideoTransport) SelectAccount(ctx context.Context, key *APIKey, request VideoTaskSubmitRequest) (*VideoUpstreamSelection, error) {
	s.request = request
	s.selects++
	return s.selector.SelectAccount(ctx, key, request)
}

// 截图参数走真实桥接、H3 转换及原账本；模拟终态验证单次预扣、恢复和捕获/释放。
func TestCreativeVideoCanvasAdaptiveMetasoPreservesBilling(t *testing.T) {
	for _, terminal := range []string{"completed", "failed", "missing_price"} {
		t.Run(terminal, func(t *testing.T) {
			s, e, upstream, billing, _ := creativeVideoTestFixture(t)
			e.Params.Endpoint, e.Params.Resolution, e.Params.AspectRatio = "openai_videos", "768p", "adaptive"
			account := videoFixtureAccount(VideoEndpointOpenAIVideos)
			account.GroupIDs = []int64{e.Params.GroupID}
			account.Credentials["model_mapping"] = map[string]any{e.Params.Model: e.Params.Model}
			account.Credentials["video_openai_profile"] = VideoOpenAIProfileMetasoH3
			s.public.AccountRepo = &creativeFakeAccountRepo{byGroup: map[int64][]Account{e.Params.GroupID: {*account}}}
			s.public.Settings.(*creativeFakeSettingReader).models[0].VideoEndpoint = VideoEndpointOpenAIVideos
			transport := &creativeAdaptiveVideoTransport{videoLifecycleUpstream: upstream, selector: NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: &videoSelectionFixture{accounts: []Account{*account}}, concurrencyService: NewConcurrencyService(nil)})}
			tasks := s.tasks.(*VideoTaskService)
			tasks.upstream, tasks.accounts = transport, videoLifecycleAccounts{account: account}
			key := s.public.ApiKeyRepo.(*creativeFakeManagedKeyRepo).key
			if terminal != "missing_price" {
				key.Group.ModelPricing[0].VideoPrices[0].Resolution = "768p"
			}
			e.Params.References = []CreativeCanvasReference{{AssetID: "portrait"}}
			assets := []CreativeCanvasInputAsset{{Asset: &CreativeCanvasAsset{ID: "portrait", UserID: e.UserID, MediaType: "image", MIMEType: "image/png"}, Content: makeTestPNG(t, 16, 24)}}
			id, err := s.Submit(context.Background(), e, assets)
			require.Equal(t, "9:16", transport.request.canvasAdaptiveRatio)
			require.Equal(t, "adaptive", gjson.GetBytes(transport.request.Body, "ratio").String())
			require.NotContains(t, string(transport.request.Body), "canvasAdaptiveRatio")
			if terminal == "missing_price" {
				require.Error(t, err)
				require.Empty(t, id)
				require.Zero(t, upstream.submitted)
				require.Zero(t, billing.reserves)
				return
			}
			require.NoError(t, err)
			task, err := s.repo.Get(context.Background(), id)
			require.NoError(t, err)
			require.Equal(t, "720x1280", task.Metadata.Size)
			require.Equal(t, "768p", task.Quote.Resolution)
			require.Equal(t, float64(10), task.Hold.HoldAmount)
			require.Equal(t, 1, billing.reserves)
			// 已受理后移除目录、素材不可用、旧自定义比例也只恢复原任务，不再解析或提交。
			s.public.Settings.(*creativeFakeSettingReader).models = nil
			e.Params.AspectRatio = "32:9"
			recovered, err := s.Submit(context.Background(), e, nil)
			require.NoError(t, err)
			require.Equal(t, id, recovered)
			require.Equal(t, 1, transport.selects)
			require.Equal(t, 1, upstream.submitted)
			upstream.poll = &VideoUpstreamResponse{StatusCode: 200, Status: terminal, Metadata: VideoRequestMetadata{Resolution: "768p", Size: "720x1280", DurationSeconds: 5}, VideoURL: "https://unused.example/result.mp4", Body: []byte(`{"status":"` + terminal + `"}`)}
			tasks.advance(context.Background(), task)
			fresh, err := s.repo.Get(context.Background(), id)
			require.NoError(t, err)
			tasks.advance(context.Background(), fresh)
			if terminal == "completed" {
				require.Equal(t, "settled", fresh.BillingStatus)
				require.Equal(t, float64(10), fresh.BillingResult.ActualAmountUSD)
				require.Equal(t, 1, billing.captures)
				require.Zero(t, billing.releases)
			} else {
				require.Equal(t, "released", fresh.BillingStatus)
				require.Equal(t, 1, billing.releases)
				require.Zero(t, billing.captures)
			}
		})
	}
}

// 对用户仅投影本地稳定错误码，不回显底层错误附带的提示词、图片地址或凭据。
func TestCreativeVideoCanvasProtocolFailureIsSafeAndActionable(t *testing.T) {
	for _, code := range []string{"VIDEO_INVALID_OPENAI_PARAMETERS", "VIDEO_PROTOCOL_PARAMETERS_INVALID", "VIDEO_METADATA_CONFLICT"} {
		t.Run(code, func(t *testing.T) {
			s, repo, params := canvasBatchFixture(t)
			executor := &canvasRejectedExecutor{err: infraerrors.BadRequest(code, "secret-token-and-image-url")}
			s.SetExecutor("video", executor)
			s.reconcileOne(context.Background(), &CreativeCanvasExecution{ID: "video-rejected", Kind: "video", Params: params, Status: "queued"})
			require.Equal(t, "failed", repo.saved.Status)
			require.Equal(t, code, repo.saved.ErrorCode)
			require.Contains(t, repo.saved.ErrorMessage, "分辨率、比例、时长")
			require.NotContains(t, repo.saved.ErrorMessage, "secret")
		})
	}
}
