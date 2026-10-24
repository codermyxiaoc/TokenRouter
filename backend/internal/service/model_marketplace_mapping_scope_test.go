//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// 分别走批量预取和单组回退，确保修复不只覆盖其中一种模型广场读法。
type marketplaceMappingScopeRepo struct {
	modelsListAccountRepoStub
	prefetchFails bool
}

func (r *marketplaceMappingScopeRepo) ListSchedulable(ctx context.Context) ([]Account, error) {
	if r.prefetchFails {
		return nil, errors.New("test prefetch unavailable")
	}
	return r.modelsListAccountRepoStub.ListSchedulable(ctx)
}

// 复现账号只配置k3.1别名而市场展示大量Claude/Kimi价卡；只收窄目录，不改价卡或收费来源。
func TestModelMarketplaceMappingOnlyDoesNotInventModelsOrPrices(t *testing.T) {
	for _, platform := range []string{PlatformKimi, PlatformAnthropic} {
		for _, source := range []string{BillingModelSourceRequested, BillingModelSourceChannelMapped, BillingModelSourceUpstream} {
			for _, fallback := range []bool{false, true} {
				name := platform + "/" + source + "/prefetch"
				if fallback {
					name = platform + "/" + source + "/fallback"
				}
				t.Run(name, func(t *testing.T) {
					ctx := context.Background()
					groupID := int64(8671)
					aliasInput, aliasOutput, upstreamInput, upstreamOutput := 2e-6, 8e-6, 4e-6, 16e-6
					channel := Channel{ID: 8672, Status: StatusActive, BillingModelSource: source,
						ModelPricing: []ChannelModelPricing{
							{Platform: platform, Models: []string{"k3.1-pro"}, BillingMode: BillingModeToken, InputPrice: &aliasInput, OutputPrice: &aliasOutput},
							{Platform: platform, Models: []string{"kimi-k3.1-pro"}, BillingMode: BillingModeToken, InputPrice: &upstreamInput, OutputPrice: &upstreamOutput},
							{Platform: platform, Models: []string{"kimi-k2.5", "kimi-k2.6", "k3.1-pro-highspeed", "claude-opus-5-5"}, BillingMode: BillingModeToken, InputPrice: &upstreamInput, OutputPrice: &upstreamOutput},
						}}
					account := Account{ID: 8673, Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{groupID},
						Credentials: map[string]any{"model_mapping": map[string]any{"k3.1-pro": "kimi-k3.1-pro"}}}
					originalAccount, err := json.Marshal(account.Credentials)
					require.NoError(t, err)
					originalPricing, err := json.Marshal(channel.ModelPricing)
					require.NoError(t, err)
					repo := &marketplaceMappingScopeRepo{modelsListAccountRepoStub: modelsListAccountRepoStub{byGroup: map[int64][]Account{groupID: {account}}, all: []Account{account}}, prefetchFails: fallback}
					channels := newRequestableModelsChannelService(groupID, platform, channel)
					billing := NewBillingService(nil, nil)
					gateway := &GatewayService{accountRepo: repo, channelService: channels, resolver: NewModelPricingResolver(channels, billing)}
					group := Group{ID: groupID, Platform: platform, Name: "映射分组", Status: StatusActive, RateMultiplier: 2, ActiveAccountCount: 1}
					market := NewModelMarketplaceService(&marketplaceGroupRepoStub{groups: []Group{group}}, nil, gateway, billing, nil, nil, nil)
					groups, err := market.ListPublic(ctx)
					require.NoError(t, err)
					require.Len(t, groups, 1)
					require.Equal(t, 1, groups[0].ModelCount)
					require.Len(t, groups[0].Models, 1)
					item := groups[0].Models[0]
					require.Equal(t, "k3.1-pro", item.ID)
					require.Equal(t, "priced", item.Pricing.PriceStatus)
					input, output := aliasInput, aliasOutput
					if source == BillingModelSourceUpstream {
						input, output = upstreamInput, upstreamOutput
					}
					require.Equal(t, input*2, item.Pricing.InputPricePerToken)
					require.Equal(t, output*2, item.Pricing.OutputPricePerToken)
					models := gateway.ResolveRequestableModels(ctx, &groupID, platform)
					require.Equal(t, []string{"k3.1-pro"}, RequestableModelIDs(models.Models), "模型列表与广场使用同一目录")
					afterAccount, err := json.Marshal(account.Credentials)
					require.NoError(t, err)
					afterPricing, err := json.Marshal(channel.ModelPricing)
					require.NoError(t, err)
					require.JSONEq(t, string(originalAccount), string(afterAccount))
					require.JSONEq(t, string(originalPricing), string(afterPricing))
					require.True(t, account.IsModelSupported("unlisted-upstream-model"), "目录说明不能变成普通调用的新白名单")
					require.Equal(t, "unlisted-upstream-model", account.GetMappedModel("unlisted-upstream-model"))
				})
			}
		}
	}
}
