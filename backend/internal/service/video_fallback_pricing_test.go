package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// 缺分辨率才允许回退；已知分辨率不因输入参考视频而改用兜底价。
func TestVideoFallbackResolutionBoundaryAndFrozenPrepay(t *testing.T) {
	price, fallback, prepay, multiplier := 10.0, 20.0, .3, 2.0
	config := ChannelModelPricing{Platform: PlatformVideo, Models: []string{"video-model"}, BillingMode: BillingModeVideoToken,
		PriceMultiplier: &multiplier, VideoPrices: []VideoPriceTier{{Resolution: "720p", Price: &price}},
		VideoFallbackPrice: &fallback, VideoTokenPrepay: &VideoTokenPrepayConfig{PricePerSecond: &prepay}}
	group := &Group{ID: 12, Platform: PlatformVideo, RateMultiplier: 3, ModelPricing: []ChannelModelPricing{config}}
	resolver := videoPricingTestResolver(group, nil)
	input := VideoPriceInput{Group: group, Model: "video-model", Resolution: "720P", RateMultiplier: 3}
	quote, err := resolver.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.False(t, quote.FallbackUsed)
	require.Equal(t, 20.0, quote.UnitPrice)
	input.HasReferenceVideo = true
	quote, err = resolver.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.False(t, quote.FallbackUsed)
	require.Equal(t, 20.0, quote.UnitPrice)
	input.Resolution = "540p"
	quote, err = resolver.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.True(t, quote.FallbackUsed)
	require.Equal(t, 40.0, quote.UnitPrice)
	require.Equal(t, .3, *quote.TokenPrepay.PricePerSecond)
	raw, err := json.Marshal(quote)
	require.NoError(t, err)
	prepay, fallback, multiplier = 9, 99, 99
	var restored VideoPriceQuote
	require.NoError(t, json.Unmarshal(raw, &restored))
	require.True(t, restored.FallbackUsed)
	require.Equal(t, .3, *restored.TokenPrepay.PricePerSecond)
	tokens := int64(1_000_000)
	cost, err := restored.Calculate(10, &tokens)
	require.NoError(t, err)
	require.Equal(t, 120.0, cost.ActualCost)
}

// 纯回退价也是完整价卡；统一入口按真实秒数或 Token 数和真实倍率结算。
func TestVideoFallbackOnlyValidationUnifiedCostAndDisplay(t *testing.T) {
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoToken} {
		for _, price := range []float64{0, 1.25} {
			group := &Group{ID: 12, Platform: PlatformVideo, RateMultiplier: 4, VideoRateIndependent: true, VideoRateMultiplier: 3}
			multiplier, prepay := 2.0, .075
			pricing := ChannelModelPricing{Platform: PlatformVideo, Models: []string{"video-model"}, BillingMode: mode,
				VideoFallbackPrice: &price, PriceMultiplier: &multiplier}
			if mode == BillingModeVideoToken {
				pricing.VideoTokenPrepay = &VideoTokenPrepayConfig{PricePerSecond: &prepay}
			}
			require.NoError(t, validatePricingEntries([]ChannelModelPricing{pricing}))
			require.True(t, pricing.HasEffectivePricing())
			group.ModelPricing = []ChannelModelPricing{pricing}
			resolver := videoPricingTestResolver(group, nil)
			tokens := int64(2_000_000)
			cost, err := NewBillingService(nil, nil).CalculateCostUnified(CostInput{Ctx: context.Background(), Group: group, Model: "video-model",
				Resolver: resolver, VideoResolution: "540p", RateMultiplier: 4, UsageUnits: 2, VideoTokens: &tokens})
			require.NoError(t, err)
			require.Equal(t, price*2*2*3, cost.ActualCost)
			display := resolver.VideoDisplayPricing(context.Background(), group, "video-model")
			require.Equal(t, "priced", display.PriceStatus)
			require.Equal(t, string(mode), display.PricingMode)
			require.Empty(t, display.VideoPrices)
			require.NotNil(t, display.VideoFallbackPrice)
			require.Equal(t, price*2*3, *display.VideoFallbackPrice)
			require.NotNil(t, display.VideoFallbackPricing)
			require.Equal(t, price*2*3, display.VideoFallbackPricing.Price)
			require.Empty(t, display.VideoFallbackPricing.Resolution)
			require.Nil(t, display.VideoFallbackPricing.HasReferenceVideo)
			if mode == BillingModeVideoToken {
				require.Equal(t, prepay, *display.VideoTokenPrepay.PricePerSecond)
				require.Equal(t, "million_tokens", display.VideoFallbackPricing.Unit)
				require.Equal(t, prepay, *display.VideoFallbackPricing.VideoTokenPrepay.PricePerSecond)
			} else {
				require.Equal(t, "second", display.VideoFallbackPricing.Unit)
				require.Nil(t, display.VideoFallbackPricing.VideoTokenPrepay)
			}
		}
	}
}

// 新配置严格限制平台、单位、有限非负价格及账号统计使用范围。
func TestVideoFallbackAndPrepayValidation(t *testing.T) {
	zero := 0.0
	valid := ChannelModelPricing{Platform: PlatformVideo, Models: []string{"video-model"}, BillingMode: BillingModeVideoToken,
		VideoFallbackPrice: &zero, VideoTokenPrepay: &VideoTokenPrepayConfig{PricePerSecond: &zero}}
	require.NoError(t, validatePricingEntries([]ChannelModelPricing{valid}))
	for _, modify := range []func(*ChannelModelPricing){
		func(p *ChannelModelPricing) { p.Platform = PlatformOpenAI },
		func(p *ChannelModelPricing) { p.BillingMode = BillingModeToken },
		func(p *ChannelModelPricing) { p.BillingMode = BillingModeVideo },
		func(p *ChannelModelPricing) { p.VideoTokenPrepay.PricePerSecond = nil },
		func(p *ChannelModelPricing) { p.VideoFallbackPrice = nil },
	} {
		broken := valid.Clone()
		modify(&broken)
		require.Error(t, validatePricingEntries([]ChannelModelPricing{broken}))
	}
	for _, bad := range []float64{-1, math.NaN(), math.Inf(1)} {
		broken := valid.Clone()
		broken.VideoFallbackPrice = &bad
		require.Error(t, validatePricingEntries([]ChannelModelPricing{broken}))
		broken = valid.Clone()
		broken.VideoTokenPrepay.PricePerSecond = &bad
		require.Error(t, validatePricingEntries([]ChannelModelPricing{broken}))
	}
	prepayOnly := valid.Clone()
	prepayOnly.VideoFallbackPrice = nil
	prepayOnly.VideoPrices = []VideoPriceTier{{Resolution: "720p", Price: &zero}}
	require.Error(t, validateAccountStatsPricingEntries([]ChannelModelPricing{prepayOnly}))
	fallbackOnly := valid.Clone()
	fallbackOnly.VideoTokenPrepay = nil
	require.Error(t, validateAccountStatsPricingEntries([]ChannelModelPricing{fallbackOnly}))
}

// Clone 和通用倍率不会修改原始指针，固定预扣秒价保持原价。
func TestVideoFallbackAndPrepayCloneScaleLegacy(t *testing.T) {
	fallback, prepay := 2.0, .1
	original := ChannelModelPricing{VideoFallbackPrice: &fallback, VideoTokenPrepay: &VideoTokenPrepayConfig{PricePerSecond: &prepay}}
	clone := original.Clone()
	multiplyChannelPricingFields(&clone, 3)
	require.Equal(t, 6.0, *clone.VideoFallbackPrice)
	require.Equal(t, .1, *clone.VideoTokenPrepay.PricePerSecond)
	*clone.VideoFallbackPrice, *clone.VideoTokenPrepay.PricePerSecond = 4, 5
	require.Equal(t, 2.0, *original.VideoFallbackPrice)
	require.Equal(t, .1, *original.VideoTokenPrepay.PricePerSecond)
	var legacy VideoPriceQuote
	require.NoError(t, json.Unmarshal([]byte(`{"version":1,"mode":"video_token","unit_price":3,"rate_multiplier":2}`), &legacy))
	require.Nil(t, legacy.TokenPrepay)
	require.False(t, legacy.FallbackUsed)
	tokens := int64(1_000_000)
	cost, err := legacy.Calculate(1, &tokens)
	require.NoError(t, err)
	require.Equal(t, 6.0, cost.ActualCost)
}

// 渠道解析和统一计费各自只应用一次价卡倍率，市场每个分辨率只展示一个单价。
func TestVideoFallbackChannelQuoteUnifiedAndDisplay(t *testing.T) {
	price, fallback, multiplier, prepay := 1.0, 3.0, 2.0, .1
	pricing := &ChannelModelPricing{Platform: PlatformVideo, Models: []string{"video-model"}, BillingMode: BillingModeVideoToken,
		PriceMultiplier: &multiplier, VideoPrices: []VideoPriceTier{{Resolution: "720p", Price: &price}},
		VideoFallbackPrice: &fallback, VideoTokenPrepay: &VideoTokenPrepayConfig{PricePerSecond: &prepay}}
	group := &Group{ID: 12, Platform: PlatformVideo, RateMultiplier: 4}
	resolver := videoPricingTestResolver(group, pricing)
	quote, err := resolver.QuoteVideo(context.Background(), VideoPriceInput{Group: group, Model: "video-model", Resolution: "540p", RateMultiplier: 4})
	require.NoError(t, err)
	require.Equal(t, PricingSourceChannel, quote.Source)
	require.Equal(t, 6.0, quote.UnitPrice)
	require.Equal(t, prepay, *quote.TokenPrepay.PricePerSecond)
	tokens := int64(1_000_000)
	cost, err := NewBillingService(nil, nil).CalculateCostUnified(CostInput{Ctx: context.Background(), Group: group, GroupID: &group.ID,
		Model: "video-model", Resolver: resolver, VideoResolution: "540p", RateMultiplier: 4, VideoTokens: &tokens})
	require.NoError(t, err)
	require.Equal(t, 24.0, cost.ActualCost)
	display := resolver.VideoDisplayPricing(context.Background(), group, "video-model")
	require.Equal(t, 24.0, *display.VideoFallbackPrice)
	require.Equal(t, .1, *display.VideoTokenPrepay.PricePerSecond)
	require.Len(t, display.VideoPrices, 1)
	require.Equal(t, "720p", display.VideoPrices[0].Resolution)
	require.Nil(t, display.VideoPrices[0].HasReferenceVideo)
	require.Equal(t, 8.0, display.VideoPrices[0].Price)
	require.Equal(t, 3.0, *pricing.VideoFallbackPrice)
	require.Equal(t, .1, *pricing.VideoTokenPrepay.PricePerSecond)
}

// 旧分组秒价优先于渠道，不能被渠道回退价改写为 Token 展示模式。
func TestVideoFallbackDisplayPreservesLegacyGroupSecondPriority(t *testing.T) {
	secondPrice, fallback, prepay, multiplier := .5, 3.0, .1, 2.0
	group := &Group{ID: 12, Platform: PlatformVideo, RateMultiplier: 4, VideoPrice720P: &secondPrice}
	pricing := &ChannelModelPricing{Platform: PlatformVideo, Models: []string{"video-model"}, BillingMode: BillingModeVideoToken,
		PriceMultiplier: &multiplier, VideoFallbackPrice: &fallback, VideoTokenPrepay: &VideoTokenPrepayConfig{PricePerSecond: &prepay}}
	resolver := videoPricingTestResolver(group, pricing)
	quote, err := resolver.QuoteVideo(context.Background(), VideoPriceInput{Group: group, Model: "video-model", Resolution: "720p", RateMultiplier: 4})
	require.NoError(t, err)
	require.Equal(t, BillingModeVideo, quote.Mode)
	require.Equal(t, PricingSourceGroup, quote.Source)
	display := resolver.VideoDisplayPricing(context.Background(), group, "video-model")
	require.Equal(t, "video", display.PricingMode)
	require.Len(t, display.VideoPrices, 1)
	require.Equal(t, 2.0, display.VideoPrices[0].Price)
	require.Equal(t, "second", display.VideoPrices[0].Unit)
	require.Nil(t, display.VideoFallbackPrice)
	require.Nil(t, display.VideoTokenPrepay)
	// 新逐档回退结构独立保留 Token 单位，旧标量仍省略以免旧客户端误标为每秒。
	require.Equal(t, "million_tokens", display.VideoFallbackPricing.Unit)
	require.Equal(t, 24.0, display.VideoFallbackPricing.Price)
	require.Equal(t, .1, *display.VideoFallbackPricing.VideoTokenPrepay.PricePerSecond)
	// 同单位回退确实对分组未覆盖的分辨率有效，仍公开实收单价。
	pricing.BillingMode = BillingModeVideo
	pricing.VideoTokenPrepay = nil
	display = resolver.VideoDisplayPricing(context.Background(), group, "video-model")
	require.Equal(t, "video", display.PricingMode)
	require.Equal(t, 24.0, *display.VideoFallbackPrice)
	require.Len(t, display.VideoPrices, 1)
	require.Equal(t, 2.0, display.VideoPrices[0].Price)
}
