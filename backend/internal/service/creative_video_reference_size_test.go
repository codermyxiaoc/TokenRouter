//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 正式 API 无内部提示时保持原合同；云画布按实际覆盖后的方案检查，不按模型名猜测。
func TestCreativeVideoReferenceSizeSelectionScope(t *testing.T) {
	for _, tc := range []struct {
		name, profile, adapter, modelAdapter string
		size                                 int64
		reject                               bool
	}{
		{name: "legacy_h3_at_limit", profile: VideoOpenAIProfileMetasoH3, size: CreativeVideoH3ReferenceImageLimit},
		{name: "legacy_h3_over_limit", profile: VideoOpenAIProfileMetasoH3, size: CreativeVideoH3ReferenceImageLimit + 1, reject: true},
		{name: "versioned_h3_over_limit", adapter: VideoProtocolAdapterMetasoH3, size: CreativeVideoH3ReferenceImageLimit + 1, reject: true},
		{name: "ordinary_api_ignores_forged_field", profile: VideoOpenAIProfileMetasoH3},
		{name: "no_reference", adapter: VideoProtocolAdapterMetasoH3},
		{name: "passthrough_unchanged", size: CreativeVideoH3ReferenceImageLimit + 1},
		{name: "explicit_legacy_overrides_old_h3", profile: VideoOpenAIProfileMetasoH3, adapter: VideoProtocolAdapterLegacy, size: CreativeVideoH3ReferenceImageLimit + 1},
		{name: "model_override_to_h3", adapter: VideoProtocolAdapterLegacy, modelAdapter: VideoProtocolAdapterMetasoH3, size: CreativeVideoH3ReferenceImageLimit + 1, reject: true},
		{name: "model_override_to_legacy", adapter: VideoProtocolAdapterMetasoH3, modelAdapter: VideoProtocolAdapterLegacy, size: CreativeVideoH3ReferenceImageLimit + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointOpenAIVideos)
			account.Credentials["video_openai_profile"] = tc.profile
			account.Credentials["model_mapping"] = map[string]any{"alias": "actual-model"}
			if tc.adapter != "" {
				account.Credentials["video_endpoint_adapters"] = map[string]string{"openai_videos": tc.adapter}
			}
			if tc.modelAdapter != "" {
				account.Credentials["video_model_adapters"] = map[string]map[string]string{"actual-model": {"openai_videos": tc.modelAdapter}}
			}
			selector := NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: &videoSelectionFixture{accounts: []Account{*account}}, concurrencyService: NewConcurrencyService(nil)})
			key := &APIKey{GroupID: &account.GroupIDs[0], Group: &Group{ID: account.GroupIDs[0], Platform: PlatformVideo, Status: StatusActive}}
			body := []byte(`{"model":"alias","duration":5,"resolution":"768p","ratio":"16:9","images":["data:image/png;base64,AAAA"],"canvasMaxReferenceImageBytes":99999999}`)
			selected, err := selector.SelectAccount(context.Background(), key, VideoTaskSubmitRequest{Body: body, InboundProtocol: "openai_videos", canvasMaxReferenceImageBytes: tc.size})
			if tc.reject {
				require.Nil(t, selected)
				require.Equal(t, "CREATIVE_VIDEO_REFERENCE_TOO_LARGE", infraerrors.Reason(err))
				return
			}
			require.NoError(t, err)
			selected.Release()
			require.Equal(t, "actual-model", selected.Metadata.Model)
		})
	}
}

// 大图拒绝一个 H3 候选后仍允许其他合法协议；混合账号不把本站限制误施加到整个模型。
func TestCreativeVideoReferenceSizeAllowsOtherEligibleCandidate(t *testing.T) {
	h3 := videoFixtureAccount(VideoEndpointOpenAIVideos)
	h3.Credentials["video_openai_profile"] = VideoOpenAIProfileMetasoH3
	legacy := videoFixtureAccount(VideoEndpointOpenAIVideos)
	legacy.ID = h3.ID + 1
	selector := NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: &videoSelectionFixture{accounts: []Account{*h3, *legacy}}, concurrencyService: NewConcurrencyService(nil)})
	key := &APIKey{GroupID: &h3.GroupIDs[0], Group: &Group{ID: h3.GroupIDs[0], Platform: PlatformVideo, Status: StatusActive}}
	body := []byte(`{"model":"m","duration":5,"resolution":"768p","ratio":"16:9","images":["data:image/png;base64,AAAA"]}`)
	selected, err := selector.SelectAccount(context.Background(), key, VideoTaskSubmitRequest{Body: body, InboundProtocol: "openai_videos", canvasMaxReferenceImageBytes: CreativeVideoH3ReferenceImageLimit + 1})
	require.NoError(t, err)
	defer selected.Release()
	require.Equal(t, legacy.ID, selected.Account.ID)
	require.Equal(t, body, selected.Body)
}

// 通过真实桥接和真实选号验证拦截先于报价、视频建账与预扣，并保留已受理任务的优先恢复。
func TestCreativeVideoReferenceSizePreservesAssetAndBillingBoundary(t *testing.T) {
	for _, size := range []int64{CreativeVideoH3ReferenceImageLimit, CreativeVideoH3ReferenceImageLimit + 1} {
		t.Run(map[bool]string{true: "over_limit", false: "at_limit"}[size > CreativeVideoH3ReferenceImageLimit], func(t *testing.T) {
			s, e, upstream, billing, _ := creativeVideoTestFixture(t)
			e.Params.Endpoint, e.Params.Resolution = "openai_videos", "768p"
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
			key.Group.ModelPricing[0].VideoPrices[0].Resolution = "768p"
			// PNG 后的填充只控制夹具大小；有效头与图像尺寸仍经过正常图片校验。
			content := make([]byte, size)
			copy(content, makeTestPNG(t, 16, 24))
			original := append([]byte(nil), content...)
			e.Params.References = []CreativeCanvasReference{{AssetID: "original"}}
			assets := []CreativeCanvasInputAsset{{Asset: &CreativeCanvasAsset{ID: "original", UserID: e.UserID, MediaType: "image", MIMEType: "image/png"}, Content: content}}
			if size > CreativeVideoH3ReferenceImageLimit {
				// 若误进入报价，此空依赖会直接暴露；拒绝必须在任何计费工作之前。
				tasks.pricing = nil
			}
			id, err := s.Submit(context.Background(), e, assets)
			require.Equal(t, size, transport.request.canvasMaxReferenceImageBytes)
			require.Equal(t, original, assets[0].Content)
			require.False(t, gjson.GetBytes(transport.request.Body, "canvasMaxReferenceImageBytes").Exists())
			if size > CreativeVideoH3ReferenceImageLimit {
				require.Empty(t, id)
				require.Equal(t, "CREATIVE_VIDEO_REFERENCE_TOO_LARGE", infraerrors.Reason(err))
				require.Empty(t, tasks.repo.(*videoLifecycleRepo).tasks)
				require.Zero(t, upstream.submitted)
				require.Zero(t, billing.reserves)
				require.Zero(t, billing.captures)
				require.Zero(t, billing.releases)
				return
			}
			require.NoError(t, err)
			require.NotEmpty(t, id)
			// 画布统一请求把参考图写成带角色的素材对象，原图字节不得被改写。
			require.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(original), gjson.GetBytes(transport.request.Body, "images.0.url").String())
			require.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(original), transport.request.Canonical.Images[0].URL)
			require.Equal(t, 1, upstream.submitted)
			require.Equal(t, 1, billing.reserves)
			// 已有任务不因素材后来变化、目录移除或新限制而重新生成或触碰账务。
			assets[0].Content = append(content, 0)
			s.public.Settings.(*creativeFakeSettingReader).models = nil
			recovered, err := s.Submit(context.Background(), e, assets)
			require.NoError(t, err)
			require.Equal(t, id, recovered)
			require.Equal(t, 1, transport.selects)
			require.Equal(t, 1, upstream.submitted)
			require.Equal(t, 1, billing.reserves)
			require.Zero(t, billing.captures)
			require.Zero(t, billing.releases)
		})
	}
}

// 目录限制只在所有候选都是 H3 时公开；模型映射与覆盖优先级与真实选账号一致。
func TestCreativeVideoReferenceSizeCatalog(t *testing.T) {
	for _, tc := range []struct {
		name, modelAdapter string
		adapters           []string
		want               int64
	}{
		{name: "legacy_h3", adapters: []string{"legacy_h3"}, want: CreativeVideoH3ReferenceImageLimit},
		{name: "explicit_h3", adapters: []string{VideoProtocolAdapterMetasoH3}, want: CreativeVideoH3ReferenceImageLimit},
		{name: "all_h3", adapters: []string{"legacy_h3", VideoProtocolAdapterMetasoH3}, want: CreativeVideoH3ReferenceImageLimit},
		{name: "mixed_h3_first", adapters: []string{VideoProtocolAdapterMetasoH3, VideoProtocolAdapterLegacy}},
		{name: "mixed_h3_last", adapters: []string{VideoProtocolAdapterLegacy, VideoProtocolAdapterMetasoH3}},
		{name: "legacy_only", adapters: []string{VideoProtocolAdapterLegacy}},
		{name: "model_override_h3", adapters: []string{VideoProtocolAdapterLegacy}, modelAdapter: VideoProtocolAdapterMetasoH3, want: CreativeVideoH3ReferenceImageLimit},
		{name: "model_override_legacy", adapters: []string{VideoProtocolAdapterMetasoH3}, modelAdapter: VideoProtocolAdapterLegacy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, e, _, _, _ := creativeVideoTestFixture(t)
			var accounts []Account
			for i, adapter := range tc.adapters {
				account := videoFixtureAccount(VideoEndpointOpenAIVideos)
				account.ID += int64(i)
				account.GroupIDs = []int64{e.Params.GroupID}
				account.Credentials["model_mapping"] = map[string]any{e.Params.Model: "upstream-model"}
				if adapter == "legacy_h3" {
					account.Credentials["video_openai_profile"] = VideoOpenAIProfileMetasoH3
				} else {
					account.Credentials["video_endpoint_adapters"] = map[string]string{"openai_videos": adapter}
				}
				if tc.modelAdapter != "" {
					account.Credentials["video_model_adapters"] = map[string]map[string]string{"upstream-model": {"openai_videos": tc.modelAdapter}}
				}
				accounts = append(accounts, *account)
			}
			s.public.AccountRepo = &creativeFakeAccountRepo{byGroup: map[int64][]Account{e.Params.GroupID: accounts}}
			s.public.Settings.(*creativeFakeSettingReader).models[0].VideoEndpoint = VideoEndpointOpenAIVideos
			models, err := s.public.ListModels(context.Background(), e.UserID)
			require.NoError(t, err)
			require.Len(t, models.Data, 1)
			require.Equal(t, tc.want, models.Data[0].VideoCapabilities.MaxReferenceImageBytes)
			raw, err := json.Marshal(models.Data[0])
			require.NoError(t, err)
			require.Equal(t, tc.want > 0, gjson.GetBytes(raw, "video_capabilities.max_reference_image_bytes").Exists())
		})
	}
	// 公共限制来自真实候选，不能由管理员保存字段伪造或覆盖。
	caps, err := normalizeCreativeVideoCapabilities(VideoEndpointOpenAIVideos, nil)
	require.NoError(t, err)
	caps.MaxReferenceImageBytes = 1
	normalized, err := normalizeCreativeVideoCapabilities(VideoEndpointOpenAIVideos, caps)
	require.NoError(t, err)
	require.Zero(t, normalized.MaxReferenceImageBytes)
}

// 错误展示只依赖稳定码，不把原图路径、凭据、提示词或上游响应带回用户。
func TestCreativeVideoReferenceSizeFailureProjection(t *testing.T) {
	s, repo, params := canvasBatchFixture(t)
	s.SetExecutor("video", &canvasRejectedExecutor{err: infraerrors.BadRequest("CREATIVE_VIDEO_REFERENCE_TOO_LARGE", "private-path-secret")})
	s.reconcileOne(context.Background(), &CreativeCanvasExecution{ID: "reference-rejected", Kind: "video", Params: params, Status: "queued"})
	require.Equal(t, "failed", repo.saved.Status)
	require.Equal(t, "CREATIVE_VIDEO_REFERENCE_TOO_LARGE", repo.saved.ErrorCode)
	require.Contains(t, repo.saved.ErrorMessage, "2 MiB")
	require.Contains(t, repo.saved.ErrorMessage, "未提交生成或预扣")
	require.NotContains(t, repo.saved.ErrorMessage, "private-path-secret")
}

// 渠道别名可改变真正匹配的模型级方案；大小提示不得沿用公开名称下的旧方案。
func TestCreativeVideoReferenceSizeCatalogFollowsChannelMapping(t *testing.T) {
	for _, tc := range []struct {
		name, publicAdapter, actualAdapter string
		additionalLegacy                   bool
		want                               int64
	}{
		{name: "channel_maps_h3_to_legacy", publicAdapter: VideoProtocolAdapterMetasoH3, actualAdapter: VideoProtocolAdapterLegacy},
		{name: "channel_maps_legacy_to_h3", publicAdapter: VideoProtocolAdapterLegacy, actualAdapter: VideoProtocolAdapterMetasoH3, want: CreativeVideoH3ReferenceImageLimit},
		{name: "other_candidate_only_matches_channel_target", publicAdapter: VideoProtocolAdapterMetasoH3, actualAdapter: VideoProtocolAdapterMetasoH3, additionalLegacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, e, _, _, _ := creativeVideoTestFixture(t)
			account := videoFixtureAccount(VideoEndpointOpenAIVideos)
			account.GroupIDs = []int64{e.Params.GroupID}
			account.Credentials["model_mapping"] = map[string]any{e.Params.Model: "public-target", "channel-target": "actual-target"}
			account.Credentials["video_model_adapters"] = map[string]map[string]string{
				"public-target": {"openai_videos": tc.publicAdapter}, "actual-target": {"openai_videos": tc.actualAdapter},
			}
			accounts := []Account{*account}
			if tc.additionalLegacy {
				legacy := videoFixtureAccount(VideoEndpointOpenAIVideos)
				legacy.ID++
				legacy.GroupIDs = account.GroupIDs
				legacy.Credentials["model_mapping"] = map[string]any{"channel-target": "only-channel-model"}
				accounts = append(accounts, *legacy)
			}
			s.public.AccountRepo = &creativeFakeAccountRepo{byGroup: map[int64][]Account{e.Params.GroupID: accounts}}
			s.public.Settings.(*creativeFakeSettingReader).models[0].VideoEndpoint = VideoEndpointOpenAIVideos
			channel := newRequestableModelsChannelService(e.Params.GroupID, PlatformVideo, Channel{ID: 88, Status: StatusActive, ModelMapping: map[string]map[string]string{PlatformVideo: {e.Params.Model: "channel-target"}}})
			s.public.PricingResolver = NewModelPricingResolver(channel, nil)
			models, err := s.public.ListModels(context.Background(), e.UserID)
			require.NoError(t, err)
			require.Len(t, models.Data, 1)
			require.Equal(t, tc.want, models.Data[0].VideoCapabilities.MaxReferenceImageBytes)
			// 真实选账号同样采用渠道后的方案，目录 hint 与后台强制检查一致。
			selector := NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: &videoSelectionFixture{accounts: accounts}, concurrencyService: NewConcurrencyService(nil), channelService: channel})
			key := s.public.ApiKeyRepo.(*creativeFakeManagedKeyRepo).key
			selected, err := selector.SelectAccount(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","duration":5,"resolution":"768p","ratio":"16:9","images":["data:image/png;base64,AAAA"]}`), InboundProtocol: "openai_videos", canvasMaxReferenceImageBytes: CreativeVideoH3ReferenceImageLimit + 1})
			if tc.want > 0 {
				require.Nil(t, selected)
				require.Equal(t, "CREATIVE_VIDEO_REFERENCE_TOO_LARGE", infraerrors.Reason(err))
			} else {
				require.NoError(t, err)
				selected.Release()
				adapter, err := VideoTargetProtocolAdapter(selected.Target)
				require.NoError(t, err)
				require.Equal(t, VideoProtocolAdapterLegacy, adapter)
			}
		})
	}
}
