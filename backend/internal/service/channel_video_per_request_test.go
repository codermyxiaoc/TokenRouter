package service

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// 渠道与分组采用同一个按次合同，显式零价有效，缺价和跨平台使用均拒绝。
func TestVideoPerRequestAdminPricingContract(t *testing.T) {
	zero, price, multiplier := 0.0, .4, 2.0
	card := ChannelModelPricing{Platform: PlatformVideo, Models: []string{"video-model"},
		BillingMode: BillingModeVideoPerRequest, VideoFallbackPrice: &zero, PriceMultiplier: &multiplier}
	require.True(t, card.BillingMode.IsValid())
	require.True(t, card.BillingMode.IsValidUsageFilter())
	require.True(t, card.HasEffectivePricing())
	require.NoError(t, validatePricingEntries([]ChannelModelPricing{card}))
	groupCard := card.Clone()
	groupCard.Platform = ""
	groupCards, err := normalizeGroupModelPricing(PlatformVideo, []ChannelModelPricing{groupCard})
	require.NoError(t, err)
	require.Equal(t, PlatformVideo, groupCards[0].Platform)
	require.Zero(t, *groupCards[0].VideoFallbackPrice)
	for _, tc := range []struct {
		name   string
		modify func(*ChannelModelPricing)
	}{
		{"wrong-platform", func(c *ChannelModelPricing) { c.Platform = PlatformOpenAI }},
		{"missing-price", func(c *ChannelModelPricing) { c.VideoFallbackPrice = nil }},
		{"old-chat-price-only", func(c *ChannelModelPricing) { c.VideoFallbackPrice = nil; c.PerRequestPrice = &price }},
		{"token-prepay", func(c *ChannelModelPricing) { c.VideoTokenPrepay = &VideoTokenPrepayConfig{PricePerSecond: &price} }},
		{"invalid-price", func(c *ChannelModelPricing) { v := math.Inf(1); c.VideoFallbackPrice = &v }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := card.Clone()
			tc.modify(&invalid)
			require.Error(t, validatePricingEntries([]ChannelModelPricing{invalid}))
		})
	}
	card.VideoFallbackPrice = nil
	card.VideoPrices = []VideoPriceTier{{Resolution: "768p", Price: &price}}
	require.True(t, card.HasEffectivePricing())
	require.NoError(t, validatePricingEntries([]ChannelModelPricing{card}))
}

// 统一入口不能把按次价当聊天 Token 或秒价，并始终单独叠加参考图固定费。
func TestVideoPerRequestUnifiedCostAndMarketContract(t *testing.T) {
	price, cardRate, fixed, imageCount := 2.0, 1.5, .15, 7
	group := &Group{ID: 12, Platform: PlatformVideo, RateMultiplier: 4, VideoRateIndependent: true, VideoRateMultiplier: 2}
	group.ModelPricing = []ChannelModelPricing{{Platform: PlatformVideo, Models: []string{"video-model"},
		BillingMode: BillingModeVideoPerRequest, VideoFallbackPrice: &price, PriceMultiplier: &cardRate,
		VideoImageInputPricing: &VideoImageInputPricing{FreeImages: 5, Price: &fixed}}}
	resolver := videoPricingTestResolver(group, nil)
	for _, duration := range []float64{0, 8, 30} {
		cost, err := NewBillingService(nil, nil).CalculateCostUnified(CostInput{Ctx: context.Background(),
			Group: group, Model: "video-model", Resolver: resolver, VideoResolution: "768p", UsageUnits: duration,
			RateMultiplier: 4, VideoReferenceImageCount: &imageCount})
		require.NoError(t, err)
		require.InDelta(t, 3.3, cost.TotalCost, 1e-10)
		require.InDelta(t, 6.3, cost.ActualCost, 1e-10)
		require.Equal(t, string(BillingModeVideoPerRequest), cost.BillingMode)
	}
	display := resolver.VideoDisplayPricing(context.Background(), group, "video-model")
	require.Equal(t, "priced", display.PriceStatus)
	require.Equal(t, string(BillingModeVideoPerRequest), display.PricingMode)
	require.NotNil(t, display.VideoFallbackPricing)
	require.Equal(t, "request", display.VideoFallbackPricing.Unit)
	require.Equal(t, 6.0, display.VideoFallbackPricing.Price)
	require.Equal(t, fixed, *display.VideoFallbackPricing.VideoImageInputPricing.Price)
}
