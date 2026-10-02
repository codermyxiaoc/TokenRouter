package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func videoPricingTestResolver(group *Group, pricing *ChannelModelPricing) *ModelPricingResolver {
	cache := newEmptyChannelCache()
	cache.loadedAt = time.Now()
	cache.groupPlatform[group.ID] = "video"
	cache.channelByGroupID[group.ID] = &Channel{ID: 1, Status: StatusActive}
	if pricing != nil {
		cache.pricingByGroupModel[channelModelKey{groupID: group.ID, platform: "video", model: "video-model"}] = pricing
	}
	channels := &ChannelService{}
	channels.cache.Store(cache)
	return NewModelPricingResolver(channels, NewBillingService(nil, nil))
}

func TestVideoQuoteReferenceResolutionAndSnapshot(t *testing.T) {
	withVideo, withoutVideo, multiplier := 46.0, 77.0, 2.0
	group := &Group{ID: 12, Platform: "video"}
	pricing := &ChannelModelPricing{BillingMode: BillingModeVideoToken, PriceMultiplier: &multiplier, VideoPrices: []VideoPriceTier{
		{Resolution: "1080p", HasReferenceVideo: true, Price: &withVideo},
		{Resolution: "1080p", HasReferenceVideo: false, Price: &withoutVideo},
	}}
	resolver := videoPricingTestResolver(group, pricing)
	input := VideoPriceInput{Group: group, Model: "video-model", Resolution: "1080P", HasReferenceVideo: true, RateMultiplier: .5}
	quote, err := resolver.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, BillingModeVideoToken, quote.Mode)
	require.Equal(t, "1080p", quote.Resolution)
	tokens := int64(1_000_000)
	cost, err := quote.Calculate(999, &tokens)
	require.NoError(t, err)
	// 旧双行无论输入是否带参考视频，都优先使用无参考标记的分辨率单价。
	require.Equal(t, 154.0, cost.TotalCost)
	require.Equal(t, 77.0, cost.ActualCost)

	// 修改渠道后，序列化的任务快照仍维持原价和单位。
	raw, err := json.Marshal(quote)
	require.NoError(t, err)
	withoutVideo = 900
	var restored VideoPriceQuote
	require.NoError(t, json.Unmarshal(raw, &restored))
	cost, err = restored.Calculate(1, &tokens)
	require.NoError(t, err)
	require.Equal(t, 77.0, cost.ActualCost)
	input.HasReferenceVideo = false
	quote, err = resolver.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, 1800.0, quote.UnitPrice)
	input.Resolution = "720p"
	_, err = resolver.QuoteVideo(context.Background(), input)
	require.ErrorIs(t, err, ErrVideoPricingUnavailable)
}

func TestVideoQuotePriorityAndUnitIsolation(t *testing.T) {
	secondPrice, tokenPrice, zero := .2, 77.0, 0.0
	group := &Group{ID: 12, Platform: "video", VideoPrice1080P: &secondPrice}
	channel := &ChannelModelPricing{BillingMode: BillingModeVideoToken, VideoPrices: []VideoPriceTier{{Resolution: "1080p", Price: &tokenPrice}}}
	resolver := videoPricingTestResolver(group, channel)
	input := VideoPriceInput{Group: group, Model: "video-model", Resolution: "1080p", RateMultiplier: 2}
	quote, err := resolver.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, BillingModeVideo, quote.Mode)
	// 显式 Token 请求跳过旧秒价，缺 Token 价绝不会按秒收取。
	input.Mode = BillingModeVideoToken
	quote, err = resolver.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, tokenPrice, quote.UnitPrice)
	group.ModelPricing = []ChannelModelPricing{{Models: []string{"video-model"}, BillingMode: BillingModeVideoToken,
		VideoPrices: []VideoPriceTier{{Resolution: "1080p", Price: &zero}}}}
	group.VideoRateIndependent, group.VideoRateMultiplier = true, .25
	quote, err = resolver.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, PricingSourceGroup, quote.Source)
	require.Zero(t, quote.UnitPrice)
	require.Equal(t, .25, quote.RateMultiplier)
	input.HasReferenceVideo = true
	quote, err = resolver.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, PricingSourceGroup, quote.Source)
	require.Zero(t, quote.UnitPrice)
	require.Equal(t, .25, quote.RateMultiplier)
	group.ModelPricing = nil
	for _, mode := range []BillingMode{BillingModeToken, BillingModeImage, BillingModePerRequest, BillingModeVideo} {
		channel.BillingMode = mode
		_, err = resolver.QuoteVideo(context.Background(), input)
		require.ErrorIs(t, err, ErrVideoPricingUnavailable)
	}
}

func TestVideoQuoteUsageDoesNotGuessOrClamp(t *testing.T) {
	resolution, err := NormalizeVideoPriceResolution("2K")
	require.NoError(t, err)
	require.Equal(t, "2k", resolution)
	quote := &VideoPriceQuote{Version: 1, Mode: BillingModeVideoToken, UnitPrice: 77, RateMultiplier: 1}
	_, err = quote.Calculate(10, nil)
	require.ErrorIs(t, err, ErrVideoUsageUnavailable)
	zero := int64(0)
	cost, err := quote.Calculate(10, &zero)
	require.NoError(t, err)
	require.Zero(t, cost.ActualCost)
	quote.Mode, quote.UnitPrice = BillingModeVideo, .5
	cost, err = quote.Calculate(30.5, nil)
	require.NoError(t, err)
	require.Equal(t, 15.25, cost.TotalCost)
	_, err = quote.Calculate(-1, nil)
	require.ErrorIs(t, err, ErrVideoUsageUnavailable)
}

func TestVideoPricingValidationAndUnifiedIsolation(t *testing.T) {
	price, negative, nan := 10.0, -1.0, math.NaN()
	valid := ChannelModelPricing{Models: []string{"video-model"}, Platform: "video", BillingMode: BillingModeVideoToken,
		VideoPrices: []VideoPriceTier{{Resolution: "2160p", HasReferenceVideo: true, Price: &price}}}
	require.NoError(t, validatePricingEntries([]ChannelModelPricing{valid}))
	for _, broken := range []ChannelModelPricing{
		{Platform: PlatformOpenAI, BillingMode: BillingModeVideoToken, VideoPrices: valid.VideoPrices},
		{BillingMode: BillingModeVideoToken},
		{BillingMode: BillingModeToken, VideoPrices: valid.VideoPrices},
		{BillingMode: BillingModeVideoToken, VideoPrices: []VideoPriceTier{{Resolution: "1080p"}}},
		{BillingMode: BillingModeVideoToken, VideoPrices: []VideoPriceTier{{Resolution: "auto", Price: &price}}},
		{BillingMode: BillingModeVideoToken, VideoPrices: []VideoPriceTier{{Resolution: "1080p", Price: &negative}}},
		{BillingMode: BillingModeVideoToken, VideoPrices: []VideoPriceTier{{Resolution: "1080p", Price: &nan}}},
		{BillingMode: BillingModeVideoToken, VideoPrices: []VideoPriceTier{{Resolution: "1080p", Price: &price}, {Resolution: "1080P", Price: &price}}},
	} {
		if broken.Platform == "" {
			broken.Platform = PlatformVideo
		}
		require.Error(t, validatePricingEntries([]ChannelModelPricing{broken}))
	}
	group := &Group{ID: 12, Platform: "video", ModelPricing: []ChannelModelPricing{valid}}
	resolver := videoPricingTestResolver(group, nil)
	billing := NewBillingService(nil, nil)
	_, err := billing.CalculateCostUnified(CostInput{Ctx: context.Background(), Model: "video-model", Group: group,
		Resolver: resolver, Tokens: UsageTokens{OutputTokens: 1_000_000}, RateMultiplier: 1})
	require.ErrorIs(t, err, ErrVideoPricingUnavailable)
	tokens := int64(1_000_000)
	cost, err := billing.CalculateCostUnified(CostInput{Ctx: context.Background(), Model: "video-model", Group: group,
		Resolver: resolver, VideoResolution: "2160p", HasReferenceVideo: true, VideoTokens: &tokens, RateMultiplier: 1})
	require.NoError(t, err)
	require.Equal(t, price, cost.ActualCost)
	// 秒价矩阵通过旧统一入口同样按真实秒数计算，不会误落入零默认按次价。
	group.ModelPricing[0].BillingMode = BillingModeVideo
	cost, err = billing.CalculateCostUnified(CostInput{Ctx: context.Background(), Model: "video-model", Group: group,
		Resolver: resolver, SizeTier: "2160p", HasReferenceVideo: true, UsageUnits: 30.5, RateMultiplier: 1})
	require.NoError(t, err)
	require.Equal(t, 305.0, cost.ActualCost)
}
