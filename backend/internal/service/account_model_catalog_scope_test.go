//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func catalogScopeAccount(platform string, mapping map[string]any) Account {
	return Account{ID: 91, Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": mapping}}
}

// 映射是公开目录的声明，不是运行时白名单；旧缓存和未配置型号不得获得市场条目。
func TestRequestableCatalogMappingOnlyDoesNotAdvertiseDefaultsOrStaleCandidates(t *testing.T) {
	for _, platform := range []string{PlatformAnthropic, PlatformKimi, PlatformZhipu, PlatformOpenAI, PlatformDeepseek} {
		t.Run(platform, func(t *testing.T) {
			account := catalogScopeAccount(platform, map[string]any{"k3.1-pro": "kimi-k3.1-pro"})
			require.Nil(t, account.GetConfiguredRequestModels(), "保留普通账号原有有限白名单语义")
			require.True(t, account.IsModelSupported("ordinary-unmapped-request"), "不能把目录修复变成运行时白名单")
			for _, emptyWhitelist := range []bool{false, true} {
				if emptyWhitelist {
					account.Credentials["model_whitelist"] = []any{}
				}
				gateway := &GatewayService{}
				result := gateway.resolveRequestableModelsWithAccounts(context.Background(), nil, platform,
					[]string{"kimi-k3.1-pro", "kimi-k2.5", "claude-opus-5-5", "stale-model"}, []Account{account})
				require.Equal(t, []string{"k3.1-pro"}, RequestableModelIDs(result.Models))
				require.False(t, smartRoutingAccountCatalogContains(&account, "kimi-k2.5"))
				require.True(t, smartRoutingAccountCatalogContains(&account, "k3.1-pro"))
			}
		})
	}
}

// 两段明确映射均是声明依据，纯价格条目即使等于目标值也不能额外制造公开型号。
func TestRequestableCatalogChannelMappingAllowsKnownAccountTarget(t *testing.T) {
	groupID := int64(9410)
	account := catalogScopeAccount(PlatformAnthropic, map[string]any{"account-alias": "kimi-target"})
	price := 0.02
	channel := Channel{ID: 7, Status: StatusActive,
		ModelMapping: map[string]map[string]string{PlatformAnthropic: {"through-account": "account-alias", "direct-target": "kimi-target"}},
		ModelPricing: []ChannelModelPricing{{Platform: PlatformAnthropic, Models: []string{"kimi-target", "unconfigured-price"}, InputPrice: &price}},
	}
	gateway := &GatewayService{accountRepo: &modelsListAccountRepoStub{byGroup: map[int64][]Account{groupID: {account}}}, channelService: newRequestableModelsChannelService(groupID, PlatformAnthropic, channel)}
	result := gateway.ResolveRequestableModels(context.Background(), &groupID, PlatformAnthropic)
	require.ElementsMatch(t, []string{"account-alias", "through-account", "direct-target"}, RequestableModelIDs(result.Models))
	for _, id := range []string{"kimi-target", "unconfigured-price", "claude-opus-5-5"} {
		require.NotContains(t, RequestableModelIDs(result.Models), id)
	}
	require.False(t, smartRoutingAccountCatalogContains(&account, "kimi-target"))
	require.True(t, smartRoutingAccountCatalogContains(&account, "kimi-target", true))
}

// 通配映射只能展开已有平台具体型号，不向目录暴露星号或任意新探针。
func TestRequestableCatalogWildcardExpandsOnlyDeclaredConcreteDefaults(t *testing.T) {
	account := catalogScopeAccount(PlatformAnthropic, map[string]any{"claude-sonnet-*": "upstream-sonnet"})
	result := (&GatewayService{}).resolveRequestableModelsWithAccounts(context.Background(), nil, PlatformAnthropic, nil, []Account{account})
	ids := RequestableModelIDs(result.Models)
	require.Contains(t, ids, "claude-sonnet-5-5")
	require.NotContains(t, ids, "claude-opus-5-5")
	require.NotContains(t, ids, "claude-sonnet-*")
	for _, id := range ids {
		require.True(t, strings.HasPrefix(id, "claude-sonnet-"))
	}
	require.False(t, smartRoutingAccountCatalogContains(&account, "claude-sonnet-made-up"))
	require.True(t, account.IsModelSupported("claude-sonnet-made-up"), "普通请求的通配重写仍由原规则处理")
}

// 渠道通配在账号映射前执行，已知平台型号可以通过两段映射进入账号，未知探针不行。
func TestRequestableCatalogChannelWildcardKeepsFiniteMappedCandidates(t *testing.T) {
	ctx := context.Background()
	group := &Group{ID: 9411, Platform: PlatformAnthropic, Status: StatusActive}
	account := catalogScopeAccount(PlatformAnthropic, map[string]any{"account-alias": "kimi-target"})
	channel := Channel{ID: 8, Status: StatusActive, ModelMapping: map[string]map[string]string{PlatformAnthropic: {"claude-*": "account-alias"}}}
	channelService := newRequestableModelsChannelService(group.ID, group.Platform, channel)
	gateway := &GatewayService{accountRepo: &modelsListAccountRepoStub{byGroup: map[int64][]Account{group.ID: {account}}}, channelService: channelService}
	result := gateway.ResolveRequestableModels(ctx, &group.ID, group.Platform)
	require.Contains(t, RequestableModelIDs(result.Models), "claude-sonnet-5-5")
	require.NotContains(t, RequestableModelIDs(result.Models), "claude-made-up")
	require.True(t, requestableCatalogCandidateContains([]Account{account}, &channel, group.Platform, "claude-sonnet-5-5"))
	require.False(t, requestableCatalogCandidateContains([]Account{account}, &channel, group.Platform, "claude-made-up"))
	// 智能路由评价与公开列表必须采用同一有限候选，不能因渠道通配而承接不存在的探针。
	smartGateway := &GatewayService{accountRepo: &smartRoutingAccountRepo{accounts: map[int64][]Account{group.ID: {account}}}, channelService: channelService}
	smart := NewSmartRoutingService(smartGateway, nil)
	known, err := smart.EvaluateSmartRoutingGroup(ctx, group, "claude-sonnet-5-5", "/v1/messages")
	require.NoError(t, err)
	require.True(t, known.HasModel)
	unknown, err := smart.EvaluateSmartRoutingGroup(ctx, group, "claude-made-up", "/v1/messages")
	require.NoError(t, err)
	require.False(t, unknown.HasModel)
}

// 每个账号分别声明目录；另一个没有映射限制的正常账号仍提供原平台基线。
func TestRequestableCatalogMixedAccountsAndExplicitWhitelist(t *testing.T) {
	account := catalogScopeAccount(PlatformAnthropic, map[string]any{"k3.1-pro": "kimi-k3.1-pro"})
	plain := catalogScopeAccount(PlatformAnthropic, nil)
	plain.ID++
	result := (&GatewayService{}).resolveRequestableModelsWithAccounts(context.Background(), nil, PlatformAnthropic, nil, []Account{account, plain})
	require.Contains(t, RequestableModelIDs(result.Models), "k3.1-pro")
	require.Contains(t, RequestableModelIDs(result.Models), "claude-sonnet-5-5")
	account.Credentials["model_whitelist"] = []any{"kimi-k3.1-pro", "custom-direct"}
	result = (&GatewayService{}).resolveRequestableModelsWithAccounts(context.Background(), nil, PlatformAnthropic, nil, []Account{account})
	require.ElementsMatch(t, []string{"k3.1-pro", "kimi-k3.1-pro", "custom-direct"}, RequestableModelIDs(result.Models))
	require.NotContains(t, RequestableModelIDs(result.Models), "claude-sonnet-5-5")
}

// 渠道读取失败只能保留账号已有声明；已确定为空的结果不能再次填入 Claude 或 OpenAI 默认值。
func TestRequestableCatalogChannelFailureDoesNotExpandOrReviveEmpty(t *testing.T) {
	groupID := int64(9412)
	for _, platform := range []string{PlatformAnthropic, PlatformKimi, PlatformOpenAI} {
		for _, wildcard := range []bool{false, true} {
			mapping := map[string]any{"k3.1-pro": "kimi-k3.1-pro"}
			if wildcard {
				mapping = map[string]any{"unlisted-only-*": "private-target"}
			}
			account := catalogScopeAccount(platform, mapping)
			gateway := &GatewayService{accountRepo: &modelsListAccountRepoStub{byGroup: map[int64][]Account{groupID: {account}}}, channelService: NewChannelService(&requestableModelsChannelRepoStub{err: errors.New("offline channel")}, nil)}
			result := gateway.ResolveRequestableModels(context.Background(), &groupID, platform)
			if wildcard {
				require.Empty(t, result.Models)
				require.True(t, result.Restricted)
			} else {
				require.Equal(t, []string{"k3.1-pro"}, RequestableModelIDs(result.Models))
			}
		}
	}
}

func TestRequestableCatalogSpecialAccountContractsRemain(t *testing.T) {
	passthrough := catalogScopeAccount(PlatformOpenAI, map[string]any{"old-alias": "old-target"})
	passthrough.Extra = map[string]any{"openai_passthrough": true}
	require.True(t, accountDeclaresCatalogModel(&passthrough, "gpt-5.6-sol"))
	require.Contains(t, mergeRequestableModelCandidates(nil, []Account{passthrough}, nil, PlatformOpenAI), "gpt-5.6-sol")
	qoder := catalogScopeAccount(PlatformQoder, map[string]any{"custom": "ultimate"})
	qoder.Type = AccountTypeCosy
	qoder.Credentials["site"] = "cn"
	require.True(t, accountDeclaresCatalogModel(&qoder, "qwen3.8-max"))
	require.Contains(t, mergeRequestableModelCandidates(qoder.GetConfiguredRequestModels(), []Account{qoder}, nil, PlatformQoder), "qwen3.8-max")
	ag := catalogScopeAccount(PlatformAntigravity, nil)
	require.True(t, accountDeclaresCatalogModel(&ag, "gemini-3-flash"))
	require.Contains(t, mergeRequestableModelCandidates(nil, []Account{ag}, nil, PlatformAntigravity), "gemini-3-flash")
}

// 映射之外的独立 Video 模型绑定也是明确目录；允许展示不会把 Seedance 绑定变为 OpenAI Videos。
func TestRequestableCatalogVideoMappingPreservesIndependentEndpointBinding(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointSeedance)
	account.Credentials["video_endpoints"] = []string{string(VideoEndpointSeedance), string(VideoEndpointOpenAIVideos)}
	account.Credentials["model_mapping"] = map[string]any{"video-alias": "mapped-upstream-video"}
	account.Credentials["video_model_bindings"] = map[string]any{"bound-only-video": "seedance"}
	require.ElementsMatch(t, []string{"video-alias", "bound-only-video"}, account.GetConfiguredRequestModels())
	result := (&GatewayService{}).resolveRequestableModelsWithAccounts(context.Background(), nil, PlatformVideo, account.GetConfiguredRequestModels(), []Account{*account})
	require.ElementsMatch(t, []string{"video-alias", "bound-only-video"}, RequestableModelIDs(result.Models))
	require.True(t, smartRoutingAccountCatalogContains(account, "bound-only-video"))
	require.False(t, smartRoutingAccountCatalogContains(account, "unlisted-video"))
	require.False(t, accountDeclaresCatalogModel(account, "unlisted-video"))
	configuration, err := account.VideoConfiguration()
	require.NoError(t, err)
	endpoint, err := configuration.SelectEndpoint("bound-only-video", "seedance", true)
	require.NoError(t, err)
	require.Equal(t, VideoEndpointSeedance, endpoint)
	_, err = configuration.SelectEndpoint("bound-only-video", "openai_videos", false)
	require.Error(t, err, "明确绑定只声明原端点，不能被目录可见性扩大权限")
	account.Credentials["model_whitelist"] = []any{"mapped-upstream-video"}
	result = (&GatewayService{}).resolveRequestableModelsWithAccounts(context.Background(), nil, PlatformVideo, []string{"video-alias", "bound-only-video"}, []Account{*account})
	require.Contains(t, RequestableModelIDs(result.Models), "video-alias")
	require.NotContains(t, RequestableModelIDs(result.Models), "bound-only-video", "正式最终白名单仍然优先于目录声明")
}

// 没有本地型号快照的平台不借用 Claude；DeepSeek 使用既有真实请求校验清单。
func TestRequestableCatalogNativeDefaultsDoNotBorrowClaude(t *testing.T) {
	for _, platform := range []string{PlatformKimi, PlatformZhipu} {
		require.Empty(t, defaultRequestModelIDsForPlatform(platform))
		require.Empty(t, defaultModelsListCandidateIDs(platform))
	}
	for _, models := range [][]string{defaultRequestModelIDsForPlatform(PlatformDeepseek), defaultModelsListCandidateIDs(PlatformDeepseek)} {
		require.Equal(t, deepseekServableModels, models)
		models[0] = "changed-local-copy"
		require.NotEqual(t, models, deepseekServableModels)
	}
}
