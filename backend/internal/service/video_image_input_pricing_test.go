package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// 固定图片费与视频部分分账，价卡倍率、独立倍率和零倍率均不得改变图片单价。
func TestVideoImageInputPricingThresholdAndMultiplier(t *testing.T) {
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoToken} {
		for _, tc := range []struct {
			name        string
			count, free int
			rate, fixed float64
		}{
			{"first", 3, 0, 2, .45}, {"below", 4, 5, 2, 0}, {"equal", 5, 5, 2, 0},
			{"excess", 7, 5, 2, .3}, {"zero multiplier", 7, 5, 0, .3}, {"no images", 0, 0, 3, 0},
		} {
			t.Run(string(mode)+"/"+tc.name, func(t *testing.T) {
				price, imagePrice, multiplier := 2., .15, 3.
				group := &Group{ID: 12, Platform: PlatformVideo, VideoRateIndependent: true, VideoRateMultiplier: tc.rate}
				card := &ChannelModelPricing{Platform: PlatformVideo, BillingMode: mode, PriceMultiplier: &multiplier,
					VideoPrices:            []VideoPriceTier{{Resolution: "768p", Price: &price}},
					VideoImageInputPricing: &VideoImageInputPricing{FreeImages: tc.free, Price: &imagePrice}}
				resolver := videoPricingTestResolver(group, card)
				quote, err := resolver.QuoteVideo(context.Background(), VideoPriceInput{Group: group, Model: "video-model", Resolution: "768p", ReferenceImageCount: &tc.count, RateMultiplier: 99})
				require.NoError(t, err)
				// JSON往返及价卡编辑后，原任务保留原始图片单价与张数。
				raw, err := json.Marshal(quote)
				require.NoError(t, err)
				imagePrice, tc.count = 100, 100
				var snapshot VideoPriceQuote
				require.NoError(t, json.Unmarshal(raw, &snapshot))
				tokens := int64(1_000_000)
				cost, err := snapshot.Calculate(1, &tokens)
				require.NoError(t, err)
				require.Equal(t, 6., cost.OutputCost)
				require.InDelta(t, tc.fixed, cost.ImageInputCost, 1e-12)
				require.InDelta(t, 6+tc.fixed, cost.TotalCost, 1e-12)
				require.InDelta(t, 6*tc.rate+tc.fixed, cost.ActualCost, 1e-12)
			})
		}
	}
}

func TestVideoImageInputPricingValidationUnknownAndLegacy(t *testing.T) {
	price, imagePrice := 1., .15
	card := ChannelModelPricing{Platform: PlatformVideo, Models: []string{"video-model"}, BillingMode: BillingModeVideo,
		VideoPrices: []VideoPriceTier{{Resolution: "768p", Price: &price}}, VideoImageInputPricing: &VideoImageInputPricing{Price: &imagePrice}}
	require.NoError(t, validatePricingEntries([]ChannelModelPricing{card}))
	require.Error(t, validateAccountStatsPricingEntries([]ChannelModelPricing{card}))
	for _, bad := range []VideoImageInputPricing{{FreeImages: -1, Price: &price}, {FreeImages: 0}, {Price: videoImagePricePointer(-1.)}, {Price: videoImagePricePointer(math.Inf(1))}, {Price: videoImagePricePointer(math.NaN())}} {
		copy := card.Clone()
		copy.VideoImageInputPricing = &bad
		require.Error(t, validatePricingEntries([]ChannelModelPricing{copy}))
	}
	copy := card.Clone()
	copy.Platform = PlatformGrok
	require.Error(t, validatePricingEntries([]ChannelModelPricing{copy}))
	copy = card.Clone()
	copy.BillingMode = BillingModeToken
	copy.VideoPrices = nil
	require.Error(t, validatePricingEntries([]ChannelModelPricing{copy}))
	group := &Group{ID: 12, Platform: PlatformVideo}
	resolver := videoPricingTestResolver(group, &card)
	input := VideoPriceInput{Group: group, Model: "video-model", Resolution: "768p", RateMultiplier: 1}
	_, err := resolver.QuoteVideo(context.Background(), input)
	require.ErrorIs(t, err, ErrVideoUsageUnavailable)
	card.VideoImageInputPricing = nil
	quote, err := resolver.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	cost, err := quote.Calculate(2, nil)
	require.NoError(t, err)
	require.Equal(t, 2., cost.ActualCost)
	require.Zero(t, cost.ImageInputCost)
}

func TestVideoImageInputPricingDisplayAndUnifiedCost(t *testing.T) {
	price, imagePrice, multiplier := 2., .15, 3.
	card := ChannelModelPricing{Platform: PlatformVideo, Models: []string{"video-model"}, BillingMode: BillingModeVideoToken, PriceMultiplier: &multiplier,
		VideoPrices: []VideoPriceTier{{Resolution: "768p", Price: &price}}, VideoImageInputPricing: &VideoImageInputPricing{FreeImages: 5, Price: &imagePrice}}
	group := &Group{ID: 12, Platform: PlatformVideo, RateMultiplier: 4, ModelPricing: []ChannelModelPricing{card}}
	resolver := videoPricingTestResolver(group, nil)
	display := resolver.VideoDisplayPricing(context.Background(), group, "video-model")
	require.Len(t, display.VideoPrices, 1)
	require.Equal(t, 24., display.VideoPrices[0].Price)
	require.Equal(t, .15, *display.VideoImageInputPricing.Price)
	count, tokens := 7, int64(1_000_000)
	cost, err := NewBillingService(nil, nil).CalculateCostUnified(CostInput{Ctx: context.Background(), Model: "video-model", Group: group,
		Resolver: resolver, VideoResolution: "768p", VideoReferenceImageCount: &count, VideoTokens: &tokens, RateMultiplier: 4})
	require.NoError(t, err)
	require.InDelta(t, .3, cost.ImageInputCost, 1e-12)
	require.InDelta(t, 24.3, cost.ActualCost, 1e-12)
	// 历史平铺秒价配置附加费时同样走视频报价，不能被旧按次分支漏收。
	group.ModelPricing[0].BillingMode = BillingModeVideo
	group.ModelPricing[0].VideoPrices = nil
	group.ModelPricing[0].PerRequestPrice = &price
	cost, err = NewBillingService(nil, nil).CalculateCostUnified(CostInput{Ctx: context.Background(), Model: "video-model", Group: group,
		Resolver: resolver, VideoResolution: "768p", VideoReferenceImageCount: &count, UsageUnits: 1, RateMultiplier: 4})
	require.NoError(t, err)
	require.InDelta(t, 24.3, cost.ActualCost, 1e-12)
	// 分组完整价卡关闭图片附加费后，不继承渠道的附加费。
	group.ModelPricing[0].VideoImageInputPricing = nil
	resolver = videoPricingTestResolver(group, &card)
	display = resolver.VideoDisplayPricing(context.Background(), group, "video-model")
	require.Nil(t, display.VideoImageInputPricing)
}

// 测试使用独立价格指针，避免依赖其他构建标签下的辅助函数。
func videoImagePricePointer(v float64) *float64 { return &v }
