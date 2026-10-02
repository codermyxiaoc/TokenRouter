package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 市场端点只用本地配置推导，测试不会请求供应商或创建付费任务。
func TestMarketplaceVideoEndpointsFollowAdaptiveSelection(t *testing.T) {
	const unified = "/v1/video/generations"
	const videos = "/v1/videos"
	const ark = "/api/v3/contents/generations/tasks"
	const wan = "/api/v1/services/aigc/video-generation/video-synthesis"
	for _, fixture := range []struct {
		name      string
		endpoints []string
		binding   any
		modelPath string
		want      []string
	}{
		{"compat does not enable plural", []string{"compat"}, nil, "", []string{unified}},
		{"plural does not enable compat", []string{"openai_videos"}, nil, "", []string{videos}},
		{"both OpenAI endpoints explicitly enabled", []string{"compat", "openai_videos"}, nil, "", []string{unified, videos}},
		{"plural and native do not enable compat", []string{"openai_videos", "seedance"}, nil, "", []string{videos, ark}},
		{"two native endpoints never enable OpenAI", []string{"seedance", "wan"}, nil, "", []string{ark, wan}},
		{"single native never enables OpenAI", []string{"wan"}, nil, "", []string{wan}},
		{"native binding excludes enabled OpenAI endpoints", []string{"compat", "openai_videos", "seedance", "wan"}, "seedance", "", []string{ark}},
		{"multi binding narrows enabled", []string{"compat", "openai_videos", "seedance", "wan"}, []string{"compat", "wan"}, "", []string{unified, wan}},
		{"plural binding excludes compat", []string{"compat", "openai_videos", "seedance"}, "openai_videos", "", []string{videos}},
		{"compat binding excludes plural", []string{"compat", "openai_videos", "seedance"}, "compat", "", []string{unified}},
		{"kling without model path remains native", []string{"kling"}, nil, "", []string{"/text-to-video/{model}", "/image-to-video/{model}", "/omni-video/{model}", "/v1/videos/text2video", "/v1/videos/omni-video"}},
		{"kling model path cannot enable OpenAI", []string{"kling"}, nil, "/omni-video/{model}", []string{"/text-to-video/{model}", "/image-to-video/{model}", "/omni-video/{model}", "/v1/videos/text2video", "/v1/videos/omni-video"}},
		{"disabled binding omitted", []string{"compat"}, "seedance", "", nil},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointCompat)
			account.Credentials["video_endpoints"] = fixture.endpoints
			account.Credentials["model_mapping"] = map[string]any{"public-model": "private-model"}
			if fixture.binding != nil {
				account.Credentials["video_model_bindings"] = map[string]any{"private-model": fixture.binding}
			}
			if fixture.modelPath != "" {
				account.Credentials["video_model_paths"] = map[string]string{"private-model": fixture.modelPath}
			}
			svc := &ModelMarketplaceService{gatewayService: &GatewayService{}}
			models := []ModelMarketplaceModel{{ID: "public-model"}}
			svc.attachMarketplaceVideoEndpoints(context.Background(), &Group{ID: 5, Platform: PlatformVideo}, models, []Account{*account, *account})
			var paths []string
			for _, endpoint := range models[0].VideoEndpoints {
				require.Equal(t, http.MethodPost, endpoint.Method)
				require.NotContains(t, endpoint.Path, "private-model")
				require.NotContains(t, endpoint.Path, "relay.example")
				paths = append(paths, endpoint.Path)
			}
			require.Equal(t, fixture.want, paths)
		})
	}
}

// 定价键可能与路由键不同；映射后仅某个账号满足渠道限制时不能合并另一账号的协议。
func TestMarketplaceVideoEndpointsRespectMappingAndAccountRestrictions(t *testing.T) {
	channel := Channel{
		ID: 17, Status: StatusActive, RestrictModels: true, BillingModelSource: BillingModelSourceUpstream,
		ModelMapping: map[string]map[string]string{PlatformVideo: {"public-model": "channel-model"}},
		ModelPricing: []ChannelModelPricing{{Platform: PlatformVideo, Models: []string{"private-allowed"}, BillingMode: BillingModeVideo}},
	}
	allowed := videoFixtureAccount(VideoEndpointSeedance)
	allowed.Credentials["model_mapping"] = map[string]any{"channel-model": "private-allowed"}
	allowed.Credentials["video_endpoints"] = []string{"compat", "seedance"}
	allowed.Credentials["video_model_bindings"] = map[string]any{"private-allowed": "seedance"}
	denied := videoFixtureAccount(VideoEndpointWan)
	denied.ID = 12
	denied.Credentials["model_mapping"] = map[string]any{"channel-model": "private-denied"}
	channelService := newRequestableModelsChannelService(5, PlatformVideo, channel)
	svc := &ModelMarketplaceService{gatewayService: &GatewayService{channelService: channelService}}
	models := []ModelMarketplaceModel{{ID: "public-model"}}
	svc.attachMarketplaceVideoEndpoints(context.Background(), &Group{ID: 5, Platform: PlatformVideo}, models, []Account{*allowed, *denied})
	require.Equal(t, []ModelMarketplaceVideoEndpoint{
		{Method: "POST", Path: "/api/v3/contents/generations/tasks", Protocol: "seedance"},
	}, models[0].VideoEndpoints)
}

func TestMarketplaceVideoEndpointsExcludeInvalidAccounts(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		change func(*Account)
	}{
		{"disabled", func(a *Account) { a.Schedulable = false }},
		{"different group", func(a *Account) { a.GroupIDs = []int64{8} }},
		{"different platform", func(a *Account) { a.Platform = PlatformOpenAI }},
		{"oauth", func(a *Account) { a.Type = AccountTypeOAuth }},
		{"model whitelist", func(a *Account) { a.Credentials["model_whitelist"] = []string{"different-model"} }},
		{"model rate limit", func(a *Account) {
			a.Extra = map[string]any{modelRateLimitsKey: map[string]any{"m": map[string]any{"rate_limit_reset_at": time.Now().Add(time.Hour).Format(time.RFC3339)}}}
		}},
		{"invalid address", func(a *Account) { a.Credentials["base_url"] = "file:///video" }},
		{"unsafe Kling model", func(a *Account) {
			a.Credentials["video_endpoints"] = []string{"kling"}
			a.Credentials["model_mapping"] = map[string]any{"m": "../secret"}
		}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointCompat)
			fixture.change(account)
			models := []ModelMarketplaceModel{{ID: "m"}}
			(&ModelMarketplaceService{gatewayService: &GatewayService{}}).attachMarketplaceVideoEndpoints(context.Background(), &Group{ID: 5, Platform: PlatformVideo}, models, []Account{*account})
			require.Empty(t, models[0].VideoEndpoints)
		})
	}
}

func TestMarketplaceVideoEndpointsLegacyGrokAndSeedanceGates(t *testing.T) {
	svc := &ModelMarketplaceService{gatewayService: &GatewayService{}}
	account := videoFixtureAccount(VideoEndpointCompat)
	account.Platform = PlatformGrok
	group := &Group{ID: 5, Platform: PlatformGrok, AllowImageGeneration: true}
	models := []ModelMarketplaceModel{{ID: "grok-imagine-video"}, {ID: "grok-4.5"}}
	svc.attachMarketplaceVideoEndpoints(context.Background(), group, models, []Account{*account})
	require.Len(t, models[0].VideoEndpoints, 2)
	require.Empty(t, models[1].VideoEndpoints)
	for _, disabled := range []bool{false, true} {
		account.Extra = map[string]any{GrokMediaEligibleExtraKey: !disabled}
		group.AllowImageGeneration = disabled
		models = []ModelMarketplaceModel{{ID: "grok-imagine-video"}}
		svc.attachMarketplaceVideoEndpoints(context.Background(), group, models, []Account{*account})
		require.Empty(t, models[0].VideoEndpoints)
	}
	account.Platform, group.Platform, group.AllowImageGeneration = PlatformOpenAI, PlatformOpenAI, true
	for _, enabled := range []bool{false, true} {
		account.Credentials["openai_workload_capabilities"] = []string{"text_generation"}
		if enabled {
			account.Credentials["openai_workload_capabilities"] = []string{"seedance"}
		}
		models = []ModelMarketplaceModel{{ID: "video-alias", OutputModalities: []string{"video"}}, {ID: "text-model"}}
		svc.attachMarketplaceVideoEndpoints(context.Background(), group, models, []Account{*account})
		require.Equal(t, enabled, len(models[0].VideoEndpoints) == 1)
		require.Empty(t, models[1].VideoEndpoints)
	}
	// 旧链路没有视频定价模式或目录模态时，最终上游型号仍可识别；客户端别名不参与猜测。
	account.Credentials["model_mapping"] = map[string]any{"my-clip": "doubao-seedance-2-0", "unknown-clip": "ep-private-model", "seedance-alias": "gpt-5.5"}
	models = []ModelMarketplaceModel{{ID: "my-clip", Pricing: ModelDisplayPricing{PricingMode: "token"}}, {ID: "unknown-clip"}, {ID: "seedance-alias"}}
	svc.attachMarketplaceVideoEndpoints(context.Background(), group, models, []Account{*account})
	require.Len(t, models[0].VideoEndpoints, 1)
	require.Empty(t, models[1].VideoEndpoints)
	require.Empty(t, models[2].VideoEndpoints)
}

func TestMarketplaceVideoEndpointsAttachThroughPrefetchedListing(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointMiniMax)
	account.Credentials["model_mapping"] = map[string]any{"my-video": "provider-video"}
	svc := &ModelMarketplaceService{gatewayService: &GatewayService{}}
	models := svc.listPublicModelsForGroupWithAccounts(context.Background(), &Group{ID: 5, Platform: PlatformVideo}, []Account{*account})
	require.Len(t, models, 1)
	require.Equal(t, "my-video", models[0].ID)
	require.Equal(t, []ModelMarketplaceVideoEndpoint{{Method: "POST", Path: "/v2/video_generation", Protocol: "minimax"}}, models[0].VideoEndpoints)
	require.Equal(t, "unpriced", models[0].Pricing.PriceStatus, "缺少定价不应伪造价格或隐藏已有端点")
}

// 回退路径只读一次账号，避免模型目录与端点来自更新前后不同快照。
type marketplaceVideoAccountSnapshotRepo struct {
	AccountRepository
	account Account
	calls   int
}

func (repo *marketplaceVideoAccountSnapshotRepo) ListSchedulableByGroupID(context.Context, int64) ([]Account, error) {
	repo.calls++
	return []Account{repo.account}, nil
}

func TestMarketplaceVideoEndpointsFallbackUsesSameAccountSnapshot(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointMiniMax)
	account.Credentials["model_mapping"] = map[string]any{"my-video": "provider-video"}
	repo := &marketplaceVideoAccountSnapshotRepo{account: *account}
	svc := &ModelMarketplaceService{gatewayService: &GatewayService{accountRepo: repo}}
	models := svc.listPublicModelsForGroup(context.Background(), &Group{ID: 5, Platform: PlatformVideo})
	require.Equal(t, 1, repo.calls)
	require.Len(t, models, 1)
	require.Equal(t, []ModelMarketplaceVideoEndpoint{{Method: "POST", Path: "/v2/video_generation", Protocol: "minimax"}}, models[0].VideoEndpoints)
}
