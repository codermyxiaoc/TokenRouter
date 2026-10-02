package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 不论排序先遇到秒价还是 Token 价，每档及回退价都只公开自己的合同。
func TestVideoDisplayMixedPricingKeepsPerTierContractsAndQuotes(t *testing.T) {
	for _, tokenResolution := range []string{"480p", "720p"} {
		t.Run(tokenResolution, func(t *testing.T) {
			second, token, fallback, prepay, image, multiplier := .5, 8.0, 12.0, .3, .15, 2.0
			group := &Group{ID: 12, Platform: PlatformVideo, RateMultiplier: 3}
			if tokenResolution == "480p" {
				group.VideoPrice720P = &second
			} else {
				group.VideoPrice480P = &second
			}
			config := &ChannelModelPricing{Platform: PlatformVideo, BillingMode: BillingModeVideoToken,
				VideoPrices: []VideoPriceTier{{Resolution: tokenResolution, Price: &token}}, PriceMultiplier: &multiplier,
				VideoFallbackPrice: &fallback, VideoTokenPrepay: &VideoTokenPrepayConfig{PricePerSecond: &prepay},
				VideoImageInputPricing: &VideoImageInputPricing{FreeImages: 5, Price: &image}}
			resolver := videoPricingTestResolver(group, config)
			images := 7
			input := VideoPriceInput{Group: group, Model: "video-model", Resolution: tokenResolution, ReferenceImageCount: &images, RateMultiplier: 3}
			before, err := resolver.QuoteVideo(context.Background(), input)
			require.NoError(t, err)
			display := resolver.VideoDisplayPricing(context.Background(), group, "video-model")
			require.Len(t, display.VideoPrices, 2)
			require.Nil(t, display.VideoTokenPrepay)
			require.Nil(t, display.VideoImageInputPricing)
			for _, row := range display.VideoPrices {
				require.Nil(t, row.HasReferenceVideo)
				if row.Resolution == tokenResolution {
					require.Equal(t, "million_tokens", row.Unit)
					require.Equal(t, 48.0, row.Price)
					require.Equal(t, .3, *row.VideoTokenPrepay.PricePerSecond)
					require.Equal(t, .15, *row.VideoImageInputPricing.Price)
				} else {
					require.Equal(t, "second", row.Unit)
					require.Equal(t, 1.5, row.Price)
					require.Nil(t, row.VideoTokenPrepay)
					require.Nil(t, row.VideoImageInputPricing)
				}
			}
			require.NotNil(t, display.VideoFallbackPricing)
			require.Equal(t, "", display.VideoFallbackPricing.Resolution)
			require.Nil(t, display.VideoFallbackPricing.HasReferenceVideo)
			require.Equal(t, "million_tokens", display.VideoFallbackPricing.Unit)
			require.Equal(t, 72.0, display.VideoFallbackPricing.Price)
			require.Equal(t, .3, *display.VideoFallbackPricing.VideoTokenPrepay.PricePerSecond)
			require.Equal(t, .15, *display.VideoFallbackPricing.VideoImageInputPricing.Price)
			if tokenResolution == "480p" {
				require.Equal(t, string(BillingModeVideoToken), display.PricingMode)
				require.Equal(t, 72.0, *display.VideoFallbackPrice)
			} else {
				require.Equal(t, string(BillingModeVideo), display.PricingMode)
				require.Nil(t, display.VideoFallbackPrice)
			}
			after, err := resolver.QuoteVideo(context.Background(), input)
			require.NoError(t, err)
			require.Equal(t, before, after)
			tokens := int64(1_000_000)
			cost, err := after.Calculate(8, &tokens)
			require.NoError(t, err)
			require.InDelta(t, 48.3, cost.ActualCost, 1e-12)
			input.Resolution = "540p"
			fallbackQuote, err := resolver.QuoteVideo(context.Background(), input)
			require.NoError(t, err)
			require.True(t, fallbackQuote.FallbackUsed)
			require.Equal(t, display.VideoFallbackPricing.Price, fallbackQuote.UnitPrice*fallbackQuote.RateMultiplier)
		})
	}
}

// 相同单位也不能把渠道图片费套到旧分组秒价；回退合同必须参与共同说明计算。
func TestVideoDisplaySameUnitMixedImageFeeAndFallback(t *testing.T) {
	price, fallback, image := .5, .9, .15
	group := &Group{ID: 12, Platform: PlatformVideo, RateMultiplier: 2, VideoPrice480P: &price}
	config := &ChannelModelPricing{Platform: PlatformVideo, BillingMode: BillingModeVideo, VideoFallbackPrice: &fallback,
		VideoImageInputPricing: &VideoImageInputPricing{Price: &image}}
	display := videoPricingTestResolver(group, config).VideoDisplayPricing(context.Background(), group, "video-model")
	require.Len(t, display.VideoPrices, 1)
	require.Nil(t, display.VideoImageInputPricing)
	require.Nil(t, display.VideoPrices[0].VideoImageInputPricing)
	require.Equal(t, .15, *display.VideoFallbackPricing.VideoImageInputPricing.Price)
	require.Equal(t, 1.8, *display.VideoFallbackPrice)
	// 增加相同单位的渠道矩阵后，说明仍只属于渠道档及回退，不属于分组档。
	config.VideoPrices = []VideoPriceTier{{Resolution: "720p", Price: &price}}
	display = videoPricingTestResolver(group, config).VideoDisplayPricing(context.Background(), group, "video-model")
	require.Len(t, display.VideoPrices, 2)
	require.Nil(t, display.VideoImageInputPricing)
	require.Nil(t, display.VideoPrices[0].VideoImageInputPricing)
	require.Equal(t, .15, *display.VideoPrices[1].VideoImageInputPricing.Price)
	require.Equal(t, "second", display.VideoPrices[1].Unit)
}

// 零价、关闭和显式零预扣是不同状态；各行、回退及旧共同字段都持有独立快照。
func TestVideoDisplayPerTierZeroAndCloning(t *testing.T) {
	zero := 0.0
	config := ChannelModelPricing{Platform: PlatformVideo, Models: []string{"video-model"}, BillingMode: BillingModeVideoToken,
		VideoPrices:        []VideoPriceTier{{Resolution: "480p", Price: &zero}, {Resolution: "720p", Price: &zero}},
		VideoFallbackPrice: &zero, VideoTokenPrepay: &VideoTokenPrepayConfig{PricePerSecond: &zero},
		VideoImageInputPricing: &VideoImageInputPricing{FreeImages: 5, Price: &zero}}
	group := &Group{ID: 12, Platform: PlatformVideo, RateMultiplier: 3, ModelPricing: []ChannelModelPricing{config}}
	resolver := videoPricingTestResolver(group, nil)
	display := resolver.VideoDisplayPricing(context.Background(), group, "video-model")
	require.Len(t, display.VideoPrices, 2)
	require.Zero(t, display.VideoFallbackPricing.Price)
	require.Zero(t, *display.VideoTokenPrepay.PricePerSecond)
	require.Zero(t, *display.VideoImageInputPricing.Price)
	*display.VideoPrices[0].VideoTokenPrepay.PricePerSecond = 99
	*display.VideoPrices[0].VideoImageInputPricing.Price = 99
	require.Nil(t, display.VideoPrices[0].HasReferenceVideo)
	require.Zero(t, *display.VideoPrices[1].VideoTokenPrepay.PricePerSecond)
	require.Zero(t, *display.VideoFallbackPricing.VideoTokenPrepay.PricePerSecond)
	require.Zero(t, *display.VideoTokenPrepay.PricePerSecond)
	require.Zero(t, *display.VideoImageInputPricing.Price)
	require.Nil(t, display.VideoPrices[1].HasReferenceVideo)
	require.Zero(t, *group.ModelPricing[0].VideoTokenPrepay.PricePerSecond)
	require.Zero(t, *group.ModelPricing[0].VideoImageInputPricing.Price)
	fresh := resolver.VideoDisplayPricing(context.Background(), group, "video-model")
	require.Zero(t, *fresh.VideoPrices[0].VideoImageInputPricing.Price)
	require.Zero(t, *fresh.VideoPrices[0].VideoTokenPrepay.PricePerSecond)
}
