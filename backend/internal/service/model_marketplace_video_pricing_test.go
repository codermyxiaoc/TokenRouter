package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestModelMarketplaceVideoPricingMatchesSettlement(t *testing.T) {
	ptr := func(v float64) *float64 { return &v }
	model := "grok-imagine-video-1.5"
	tests := []struct {
		name    string
		group   Group
		channel *ChannelModelPricing
		prices  []float64
		units   []string
	}{
		{name: "内置每秒价格应用分组倍率", group: Group{RateMultiplier: 2}, prices: []float64{0.16, 0.28, 0.50}},
		{name: "模型族覆盖优先且保留零价", group: Group{RateMultiplier: 2, VideoPrice480P: ptr(0.9), VideoPrice720P: ptr(0.4), VideoModelPrices: map[string]map[string]float64{model: {"480p": 0}}}, prices: []float64{0, 0.8, 0.50}},
		{name: "视频独立倍率不使用图片倍率", group: Group{RateMultiplier: 2, VideoRateIndependent: true, VideoRateMultiplier: 0.5, ImageRateIndependent: true, ImageRateMultiplier: 9}, prices: []float64{0.04, 0.07, 0.125}},
		{name: "视频独立零倍率为免费", group: Group{RateMultiplier: 2, VideoRateIndependent: true}, prices: []float64{0, 0, 0}},
		{name: "渠道按秒分辨率价格", group: Group{RateMultiplier: 2}, channel: &ChannelModelPricing{BillingMode: BillingModeVideo, PerRequestPrice: ptr(0.3), Intervals: []PricingInterval{{TierLabel: "720p", PerRequestPrice: ptr(0.6)}}}, prices: []float64{0.6, 1.2, 0.6}},
		{name: "分组视频覆盖逐档优先于渠道按次", group: Group{RateMultiplier: 2, VideoPrice480P: ptr(0.1)}, channel: &ChannelModelPricing{BillingMode: BillingModePerRequest, PerRequestPrice: ptr(0.7)}, prices: []float64{0.2, 1.4, 1.4}, units: []string{"second", "request", "request"}},
		{name: "渠道图片兼容价格保持按次", group: Group{RateMultiplier: 2}, channel: &ChannelModelPricing{BillingMode: BillingModeImage, PerRequestPrice: ptr(0.7)}, prices: []float64{1.4, 1.4, 1.4}, units: []string{"request", "request", "request"}},
		{name: "分组逐模型视频价优先于专属和渠道", group: Group{RateMultiplier: 2, VideoPrice480P: ptr(0.9), ModelPricing: []ChannelModelPricing{{Models: []string{model}, BillingMode: BillingModeVideo, PerRequestPrice: ptr(0), Intervals: []PricingInterval{{TierLabel: "720p", PerRequestPrice: ptr(0.6)}}}}}, channel: &ChannelModelPricing{BillingMode: BillingModeVideo, PerRequestPrice: ptr(2)}, prices: []float64{0, 1.2, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group := tt.group
			group.ID, group.Platform = 52, PlatformGrok
			cache := newEmptyChannelCache()
			cache.loadedAt = time.Now()
			cache.groupPlatform[group.ID] = PlatformGrok
			cache.channelByGroupID[group.ID] = &Channel{ID: 1, Status: StatusActive}
			if tt.channel != nil {
				cache.pricingByGroupModel[channelModelKey{groupID: group.ID, platform: PlatformGrok, model: model}] = tt.channel
			}
			channels := &ChannelService{}
			channels.cache.Store(cache)
			billing := NewBillingService(nil, nil)
			resolver := NewModelPricingResolver(channels, billing)
			svc := NewModelMarketplaceService(nil, nil, &GatewayService{resolver: resolver}, billing, nil, nil, nil)
			models := svc.buildPublicModelsForGroup(context.Background(), &group, []marketplaceModelDef{{ID: model, PricingModel: model}})
			require.Len(t, models, 1)
			pricing := models[0].Pricing
			require.Equal(t, "video", pricing.PricingMode)
			require.Equal(t, "priced", pricing.PriceStatus)
			require.Len(t, pricing.VideoPrices, 3)
			gateway := &OpenAIGatewayService{billingService: billing, resolver: resolver}
			key := &APIKey{GroupID: &group.ID, Group: &group}
			for i, tier := range pricing.VideoPrices {
				unit := "second"
				if tt.units != nil {
					unit = tt.units[i]
				}
				require.Equal(t, unit, tier.Unit)
				require.InDelta(t, tt.prices[i], tier.Price, 1e-10)
				// 同一模型、分辨率以真实结算入口核对，按次渠道不可乘视频时长。
				cost := gateway.calculateOpenAIVideoCost(context.Background(), model, key, &OpenAIForwardResult{VideoCount: 2, VideoDurationSeconds: 8, VideoResolution: tier.Resolution}, resolveVideoRateMultiplier(key, group.RateMultiplier))
				want := tier.Price * 2
				if unit == "second" {
					want *= 8
				}
				require.InDelta(t, want, cost.ActualCost, 1e-10)
			}
		})
	}
}

func TestModelMarketplaceVideoPricingPreservesModelIdentityAndAmbiguity(t *testing.T) {
	svc := NewModelMarketplaceService(nil, nil, nil, NewBillingService(nil, nil), nil, nil, nil)
	group := &Group{ID: 12, Platform: PlatformGrok, RateMultiplier: 1}
	models := svc.buildPublicModelsForGroup(context.Background(), group, []marketplaceModelDef{
		{ID: "my-video", PricingModel: "grok-imagine-video-1.5"},
		{ID: "ambiguous-video", PricingModel: "grok-imagine-video-1.5", PricingAmbiguous: true},
		{ID: "grok-4.6", PricingModel: "grok-4.6"},
		{ID: "other-video-1.5", PricingModel: "other-video-1.5"},
	})
	require.Equal(t, "my-video", models[0].ID)
	require.Equal(t, "video", models[0].Pricing.PricingMode)
	require.Equal(t, "unpriced", models[1].Pricing.PriceStatus)
	require.Empty(t, models[2].Pricing.VideoPrices)
	require.Equal(t, "token", models[2].Pricing.PricingMode)
	require.Empty(t, models[3].Pricing.VideoPrices)
}
