package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// MJ 的能力可独立展示，但没有显式价格时不得借用 Gemini 默认图片价。
func TestModelMarketplaceMidjourneyCapabilitiesWithoutInventedPricing(t *testing.T) {
	billing := NewBillingService(nil, nil)
	svc := NewModelMarketplaceService(nil, nil, nil, billing, nil, nil, nil)
	group := &Group{ID: 1, Platform: PlatformOpenAI, RateMultiplier: 1}
	for _, model := range []string{"MJ-v7", "MJ-v8.1", "MJ-v8.2", "mj-v8.2-chat", "MJ", "mj_custom", "MjFast"} {
		t.Run(model, func(t *testing.T) {
			models := svc.buildPublicModelsForGroup(context.Background(), group, []marketplaceModelDef{{ID: model, DisplayName: model}})
			require.Len(t, models, 1)
			require.Equal(t, model, models[0].ID)
			require.Equal(t, []string{"text"}, models[0].InputModalities)
			require.Equal(t, []string{"image"}, models[0].OutputModalities)
			require.Equal(t, "unpriced", models[0].Pricing.PriceStatus)
		})
	}
	input, output := svc.marketplaceModelModalities(marketplaceModelDef{ID: "public-art", PricingModel: "MJ-v8.2"})
	require.Equal(t, []string{"text"}, input)
	require.Equal(t, []string{"image"}, output)
	input, output = svc.marketplaceModelModalities(marketplaceModelDef{ID: "prefix-mj-v7"})
	require.Nil(t, input)
	require.Nil(t, output)
}

// 明确目录能力保持最高优先级，MJ 的名称兜底不得覆盖元数据。
func TestModelMarketplaceMidjourneyExplicitMetadataWins(t *testing.T) {
	pricing := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"mj-v8.2": {Mode: "image_generation", SupportedModalities: []string{"text", "image"}, SupportedOutputModalities: []string{"image"}},
	}}
	svc := NewModelMarketplaceService(nil, nil, nil, NewBillingService(nil, pricing), nil, nil, nil)
	input, output := svc.marketplaceModelModalities(marketplaceModelDef{ID: "MJ-v8.2"})
	require.Equal(t, []string{"text", "image"}, input)
	require.Equal(t, []string{"image"}, output)
}

// 配置按图或按次价格时才投影为图片价，显式零价及图片独立倍率仍然有效。
func TestModelMarketplaceMidjourneyConfiguredPricing(t *testing.T) {
	for _, source := range []string{PricingSourceGroup, PricingSourceChannel} {
		for _, mode := range []BillingMode{BillingModeImage, BillingModePerRequest} {
			for _, price := range []float64{0, 0.4} {
				resolved := &ResolvedPricing{
					Mode: mode, Source: source, DefaultPerRequestPrice: price,
					channelPricing: &ChannelModelPricing{BillingMode: mode, PerRequestPrice: &price},
				}
				display, ok := displayPricingFromResolved("MJ-v8.2", 3, 0.85, resolved)
				require.True(t, ok)
				require.Equal(t, "image", display.PricingMode)
				require.Equal(t, "priced", display.PriceStatus)
				require.InDelta(t, price*0.85, display.ImagePrice1K, 1e-12)
				require.InDelta(t, price*0.85, display.ImagePrice2K, 1e-12)
				require.InDelta(t, price*0.85, display.ImagePrice4K, 1e-12)
			}
		}
	}
	_, ok := displayPricingFromResolved("MJ-v8.2", 1, 1, &ResolvedPricing{
		Mode: BillingModePerRequest, Source: PricingSourceFallback, DefaultPerRequestPrice: 0.134,
	})
	require.False(t, ok)
}
