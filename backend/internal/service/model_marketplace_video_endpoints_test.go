package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 市场端点只用本地配置推导，测试不会请求供应商或创建付费任务。
// 独立视频平台只公开统一入口 POST /v1/videos：模型在账号上有可解析的协议绑定才展示，旧端点配置按迁移规则换算，
// 原生入口与 /v1/video/generations 均不再出现在市场中。
func TestMarketplaceVideoEndpointsFollowAdaptiveSelection(t *testing.T) {
	const videos = "/v1/videos"
	for _, fixture := range []struct {
		name      string
		endpoints []string
		binding   any
		modelPath string
		protocols map[string]any
		want      []string
	}{
		{"legacy compat converts to unified entry", []string{"compat"}, nil, "", nil, []string{videos}},
		{"legacy plural converts to unified entry", []string{"openai_videos"}, nil, "", nil, []string{videos}},
		{"legacy natives convert to unified entry", []string{"seedance", "wan"}, nil, "", nil, []string{videos}},
		{"legacy native binding converts to unified entry", []string{"compat", "openai_videos", "seedance", "wan"}, "seedance", "", nil, []string{videos}},
		{"legacy kling without model path has no binding", []string{"kling"}, nil, "", nil, nil},
		{"legacy kling model path binds the model", []string{"kling"}, nil, "/omni-video/{model}", nil, []string{videos}},
		{"legacy disabled binding omitted", []string{"compat"}, "seedance", "", nil, nil},
		{"exact protocol binding", nil, nil, "", map[string]any{"private-model": map[string]any{"protocol": "seedance.seedance_v1"}}, []string{videos}},
		{"wildcard protocol binding", nil, nil, "", map[string]any{"*": map[string]any{"protocol": "wan.wan_v1"}}, []string{videos}},
		{"other model binding omitted", nil, nil, "", map[string]any{"other-model": map[string]any{"protocol": "seedance.seedance_v1"}}, nil},
		{"unsafe binding address omitted", nil, nil, "", map[string]any{"private-model": map[string]any{"protocol": "seedance.seedance_v1", "base_url": "file:///video"}}, nil},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointCompat)
			delete(account.Credentials, "video_endpoints")
			if fixture.endpoints != nil {
				account.Credentials["video_endpoints"] = fixture.endpoints
			}
			if fixture.protocols != nil {
				account.Credentials["video_model_protocols"] = fixture.protocols
			}
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
				require.Equal(t, "openai_videos", endpoint.Protocol)
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
	// 被拒绝账号的协议不会合并进来；允许账号的绑定只表现为统一入口。
	require.Equal(t, []ModelMarketplaceVideoEndpoint{
		{Method: "POST", Path: "/v1/videos", Protocol: "openai_videos"},
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
			// 显式绑定路径，确保拒绝来自路径模型校验而不是缺少绑定。
			a.Credentials["video_model_paths"] = map[string]string{"../secret": "/omni-video/{model}"}
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
	// 旧 MiniMax 端点换算为模型绑定后，市场只公开统一入口。
	require.Equal(t, []ModelMarketplaceVideoEndpoint{{Method: "POST", Path: "/v1/videos", Protocol: "openai_videos"}}, models[0].VideoEndpoints)
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
	require.Equal(t, []ModelMarketplaceVideoEndpoint{{Method: "POST", Path: "/v1/videos", Protocol: "openai_videos"}}, models[0].VideoEndpoints)
}
