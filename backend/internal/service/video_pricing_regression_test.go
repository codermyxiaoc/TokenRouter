package service

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// 分组价卡是一份完整合同，缺档或单位不匹配不能穿透渠道，参考视频输入不改变单价。
func TestVideoPricingRegressionCompleteGroupContract(t *testing.T) {
	zero, groupPrice, channelPrice, prepay, imagePrice := 0.0, 7.0, .25, .1, .2
	channel := &ChannelModelPricing{Platform: PlatformVideo, BillingMode: BillingModeVideoToken, VideoFallbackPrice: &channelPrice,
		VideoTokenPrepay: &VideoTokenPrepayConfig{PricePerSecond: &prepay}, VideoImageInputPricing: &VideoImageInputPricing{Price: &imagePrice}}
	group := &Group{ID: 12, Platform: PlatformVideo, ModelPricing: []ChannelModelPricing{{Platform: PlatformVideo,
		Models: []string{"video-model"}, BillingMode: BillingModeVideoToken, VideoPrices: []VideoPriceTier{{Resolution: "720p", Price: &groupPrice}}}}}
	resolver := videoPricingTestResolver(group, channel)
	input := VideoPriceInput{Group: group, Model: "video-model", Resolution: "720p", RateMultiplier: 2}
	quote, err := resolver.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, PricingSourceGroup, quote.Source)
	require.Nil(t, quote.TokenPrepay)
	require.Nil(t, quote.ImageInputPricing)
	require.Equal(t, 7.0, quote.UnitPrice)
	input.Resolution = "540p"
	_, err = resolver.QuoteVideo(context.Background(), input)
	require.ErrorIs(t, err, ErrVideoPricingUnavailable)
	input.Resolution, input.HasReferenceVideo = "720p", true
	quote, err = resolver.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, PricingSourceGroup, quote.Source)
	require.Equal(t, 7.0, quote.UnitPrice)
	require.Nil(t, quote.TokenPrepay)
	require.Nil(t, quote.ImageInputPricing)
	input.HasReferenceVideo, input.Mode = false, BillingModeVideo
	_, err = resolver.QuoteVideo(context.Background(), input)
	require.ErrorIs(t, err, ErrVideoPricingUnavailable)
	input.Mode = BillingModeVideoToken
	group.ModelPricing[0].VideoPrices[0].Price = &zero
	quote, err = resolver.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.Zero(t, quote.UnitPrice)
	// 分组明确配置零回退价后，只对从未配置的分辨率生效。
	group.ModelPricing[0].VideoFallbackPrice = &zero
	input.Resolution = "540p"
	quote, err = resolver.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.True(t, quote.FallbackUsed)
	require.Zero(t, quote.UnitPrice)
	input.Resolution, input.HasReferenceVideo = "720p", true
	quote, err = resolver.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.False(t, quote.FallbackUsed)
	require.Zero(t, quote.UnitPrice)
}

// 缺少模型、缺少分辨率、非法数值或完全未定价都必须明确失败，不能借通用文本价格免费放行。
func TestVideoPricingRegressionMissingIdentityAndResolution(t *testing.T) {
	price := 3.0
	group := &Group{ID: 12, Platform: PlatformVideo}
	resolver := videoPricingTestResolver(group, &ChannelModelPricing{Platform: PlatformVideo, BillingMode: BillingModeVideoToken, VideoFallbackPrice: &price})
	for _, tc := range []struct{ model, resolution string }{
		{"", "720p"}, {" ", "720p"}, {"unknown-video-model", "720p"}, {"video-model", ""}, {"video-model", "auto"},
		{"video-model", "1280x720"}, {"video-model", "0p"}, {"video-model", "0720p"}, {"video-model", "65537p"},
	} {
		_, err := resolver.QuoteVideo(context.Background(), VideoPriceInput{Group: group, Model: tc.model, Resolution: tc.resolution, RateMultiplier: 1})
		require.ErrorIs(t, err, ErrVideoPricingUnavailable, "%+v", tc)
	}
	for _, resolution := range []string{"720", "720P", " 720p ", "2K", "4k", "540p"} {
		quote, err := resolver.QuoteVideo(context.Background(), VideoPriceInput{Group: group, Model: "video-model", Resolution: resolution, RateMultiplier: 1})
		require.NoError(t, err)
		require.True(t, quote.FallbackUsed)
	}
	for _, rate := range []float64{-1, math.NaN(), math.Inf(1)} {
		_, err := resolver.QuoteVideo(context.Background(), VideoPriceInput{Group: group, Model: "video-model", Resolution: "720p", RateMultiplier: rate})
		require.ErrorIs(t, err, ErrVideoPricingUnavailable)
	}
}

// 同一份最终倍率只应用一次；预扣秒价、固定图片费不随卡倍率或视频独立倍率变化。
func TestVideoPricingRegressionEffectiveRateAndRawPrepay(t *testing.T) {
	for _, tc := range []struct {
		name                                     string
		cardRate, effectiveRate, independentRate float64
		independent                              bool
		wantRate                                 float64
	}{
		{"effective_balance_rate", 2, .4, 0, false, .4}, {"effective_subscription_rate", 2, .6, 0, false, .6},
		{"independent_overrides_effective", 2, 99, .25, true, .25}, {"independent_zero", 2, 99, 0, true, 0},
		{"zero_card_price", 0, 2, 0, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			price, prepay, imagePrice, count := 3., .07, .5, 3
			card := &ChannelModelPricing{Platform: PlatformVideo, BillingMode: BillingModeVideoToken, PriceMultiplier: &tc.cardRate, VideoFallbackPrice: &price,
				VideoTokenPrepay: &VideoTokenPrepayConfig{PricePerSecond: &prepay}, VideoImageInputPricing: &VideoImageInputPricing{FreeImages: 1, Price: &imagePrice}}
			group := &Group{ID: 12, Platform: PlatformVideo, RateMultiplier: 17, VideoRateIndependent: tc.independent, VideoRateMultiplier: tc.independentRate}
			resolver := videoPricingTestResolver(group, card)
			quote, err := resolver.QuoteVideo(context.Background(), VideoPriceInput{Group: group, Model: "video-model", Resolution: "540p", RateMultiplier: tc.effectiveRate, ReferenceImageCount: &count})
			require.NoError(t, err)
			require.Equal(t, tc.wantRate, quote.RateMultiplier)
			require.Equal(t, .07, *quote.TokenPrepay.PricePerSecond)
			tokens := int64(2_000_000)
			cost, err := quote.Calculate(5, &tokens)
			require.NoError(t, err)
			require.Equal(t, 6*tc.cardRate, cost.OutputCost)
			require.Equal(t, 1., cost.ImageInputCost)
			require.InDelta(t, 6*tc.cardRate*tc.wantRate+1, cost.ActualCost, 1e-12)
			_, err = quote.Calculate(5, nil)
			require.ErrorIs(t, err, ErrVideoUsageUnavailable)
			tokens = 0
			cost, err = quote.Calculate(5, &tokens)
			require.NoError(t, err)
			require.Equal(t, 1., cost.ActualCost)
		})
	}
}

// 厂商 2K/4K 档位独立于数字像素档；规范化只能合并大小写与数字 p 后缀。
func TestVideoPricingRegressionResolutionIdentityAndZeroTier(t *testing.T) {
	zero, other, fallback := 0., 4., 99.
	group := &Group{ID: 12, Platform: PlatformVideo, ModelPricing: []ChannelModelPricing{{Platform: PlatformVideo, Models: []string{"video-model"}, BillingMode: BillingModeVideo,
		VideoPrices: []VideoPriceTier{{Resolution: "2K", Price: &zero}, {Resolution: "2160p", Price: &other}}, VideoFallbackPrice: &fallback}}}
	resolver := videoPricingTestResolver(group, nil)
	for _, tc := range []struct {
		resolution string
		price      float64
		fallback   bool
	}{{"2k", 0, false}, {"2160P", 4, false}, {"2160", 4, false}, {"4K", 99, true}} {
		quote, err := resolver.QuoteVideo(context.Background(), VideoPriceInput{Group: group, Model: "video-model", Resolution: tc.resolution, RateMultiplier: 2})
		require.NoError(t, err)
		require.Equal(t, tc.price, quote.UnitPrice)
		require.Equal(t, tc.fallback, quote.FallbackUsed)
		cost, err := quote.Calculate(30.5, nil)
		require.NoError(t, err)
		require.Equal(t, tc.price*61, cost.ActualCost)
		for _, duration := range []float64{0, -1, math.NaN(), math.Inf(1)} {
			_, err = quote.Calculate(duration, nil)
			require.ErrorIs(t, err, ErrVideoUsageUnavailable)
		}
	}
}
