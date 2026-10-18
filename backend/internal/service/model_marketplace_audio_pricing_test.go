package service

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestModelMarketplaceAudioPricingMatchesVoiceSettlement(t *testing.T) {
	ptr := func(v float64) *float64 { return &v }
	for _, tc := range []struct {
		name, model, canonical, mode, unit string
		group                              Group
		channel                            *ChannelModelPricing
		price                              float64
	}{
		{name: "tts_short_name", model: "grok-tts", canonical: "tts", mode: "tts", unit: "million_characters", group: Group{RateMultiplier: 1, AudioTTSPricePerMillionChars: ptr(10)}, price: 10},
		{name: "tts_version", model: "grok-voice-tts-1.0", canonical: "tts", mode: "tts", unit: "million_characters", group: Group{RateMultiplier: 2, AudioTTSPricePerMillionChars: ptr(10)}, price: 20},
		{name: "tts_canvas", model: CreativeGrokTTSModel, canonical: "tts", mode: "tts", unit: "million_characters", group: Group{RateMultiplier: 1, AudioTTSPricePerMillionChars: ptr(10)}, price: 10},
		{name: "tts_default", model: "grok-tts", canonical: "tts", mode: "tts", unit: "million_characters", group: Group{RateMultiplier: 2}, price: 30},
		{name: "tts_explicit_free", model: "grok-tts", canonical: "tts", mode: "tts", unit: "million_characters", group: Group{RateMultiplier: 2, AudioTTSPricePerMillionChars: ptr(0)}, price: 0},
		{name: "zero_group_multiplier", model: "grok-tts", canonical: "tts", mode: "tts", unit: "million_characters", group: Group{RateMultiplier: 0, AudioTTSPricePerMillionChars: ptr(10)}, price: 0},
		{name: "ignore_image_multiplier", model: "grok-tts", canonical: "tts", mode: "tts", unit: "million_characters", group: Group{RateMultiplier: 2, ImageRateIndependent: true, ImageRateMultiplier: 99, AudioTTSPricePerMillionChars: ptr(10)}, price: 20},
		{name: "canonical_channel_price", model: "grok-voice-tts-1.0", canonical: "tts", mode: "tts", unit: "million_characters", group: Group{RateMultiplier: 2, AudioTTSPricePerMillionChars: ptr(10)}, channel: &ChannelModelPricing{BillingMode: BillingModePerRequest, PerRequestPrice: ptr(12)}, price: 24},
		{name: "canonical_channel_free", model: "grok-voice-tts-1.0", canonical: "tts", mode: "tts", unit: "million_characters", group: Group{RateMultiplier: 2, AudioTTSPricePerMillionChars: ptr(10)}, channel: &ChannelModelPricing{BillingMode: BillingModePerRequest, PerRequestPrice: ptr(0)}, price: 0},
		{name: "channel_tier_and_price_multiplier", model: "grok-tts", canonical: "tts", mode: "tts", unit: "million_characters", group: Group{RateMultiplier: 2}, channel: &ChannelModelPricing{BillingMode: BillingModePerRequest, PerRequestPrice: ptr(7), PriceMultiplier: ptr(3), Intervals: []PricingInterval{{TierLabel: "tts", PerRequestPrice: ptr(4)}}}, price: 24},
		{name: "canonical_group_model_wins", model: "grok-voice-tts-1.0", canonical: "tts", mode: "tts", unit: "million_characters", group: Group{RateMultiplier: 2, AudioTTSPricePerMillionChars: ptr(10), ModelPricing: []ChannelModelPricing{{Models: []string{"tts"}, BillingMode: BillingModePerRequest, PerRequestPrice: ptr(5)}}}, channel: &ChannelModelPricing{BillingMode: BillingModePerRequest, PerRequestPrice: ptr(12)}, price: 10},
		{name: "tts_token_price_is_not_voice", model: "grok-tts", canonical: "tts", mode: "tts", unit: "million_characters", group: Group{RateMultiplier: 1, AudioTTSPricePerMillionChars: ptr(10)}, channel: &ChannelModelPricing{BillingMode: BillingModeToken, InputPrice: ptr(999)}, price: 10},
		{name: "stt_version", model: "grok-voice-stt-1.0", canonical: "stt", mode: "stt", unit: "hour", group: Group{RateMultiplier: 2, AudioSTTPricePerHour: ptr(10)}, price: 20},
		{name: "stt_default", model: "grok-stt", canonical: "stt", mode: "stt", unit: "hour", group: Group{RateMultiplier: 1}, price: .1},
		{name: "realtime_default", model: "grok-voice-latest", canonical: "grok-voice-latest", mode: "realtime", unit: "minute", group: Group{RateMultiplier: 2}, price: .1},
		{name: "realtime_group", model: "grok-voice-think-fast-2.0", canonical: "grok-voice-think-fast-2.0", mode: "realtime", unit: "minute", group: Group{RateMultiplier: 1, AudioRealtimePricePerMin: ptr(10)}, price: 10},
		{name: "realtime_specific_channel", model: "grok-voice-think-fast-2.0", canonical: "grok-voice-think-fast-2.0", mode: "realtime", unit: "minute", group: Group{RateMultiplier: 2, AudioRealtimePricePerMin: ptr(10)}, channel: &ChannelModelPricing{BillingMode: BillingModePerRequest, PerRequestPrice: ptr(.08)}, price: .16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := tc.group
			group.ID = 52
			group.Platform = PlatformGrok
			cache := newEmptyChannelCache()
			cache.loadedAt = time.Now()
			cache.groupPlatform[group.ID] = PlatformGrok
			cache.channelByGroupID[group.ID] = &Channel{ID: 1, Status: StatusActive}
			if tc.channel != nil {
				cache.pricingByGroupModel[channelModelKey{groupID: group.ID, platform: PlatformGrok, model: tc.canonical}] = tc.channel
			}
			channels := &ChannelService{}
			channels.cache.Store(cache)
			billing := NewBillingService(nil, nil)
			resolver := NewModelPricingResolver(channels, billing)
			svc := NewModelMarketplaceService(nil, nil, &GatewayService{resolver: resolver}, billing, nil, nil, nil)
			models := svc.buildPublicModelsForGroup(context.Background(), &group, []marketplaceModelDef{{ID: tc.model, PricingModel: tc.model}})
			require.Len(t, models, 1)
			price := models[0].Pricing
			require.Equal(t, "audio", price.PricingMode)
			require.Equal(t, "priced", price.PriceStatus)
			require.NotNil(t, price.AudioPrice)
			require.Equal(t, tc.mode, price.AudioPrice.Mode)
			require.Equal(t, tc.unit, price.AudioPrice.Unit)
			require.InDelta(t, tc.price, price.AudioPrice.Price, 1e-10)
			gateway := &OpenAIGatewayService{resolver: resolver, billingService: billing}
			cost, err := gateway.calculateOpenAIRecordUsageCost(context.Background(), &OpenAIForwardResult{AudioUsage: &AudioUsage{Mode: tc.mode, DurationOrUnits: 2.5}}, &APIKey{Group: &group, GroupID: &group.ID}, []string{tc.canonical}, group.RateMultiplier, 99, 99, group.RateMultiplier, UsageTokens{}, "")
			require.NoError(t, err)
			require.InDelta(t, price.AudioPrice.Price*2.5, cost.ActualCost, 1e-10)
			if tc.mode == "tts" {
				// 云画布按同一 canonical tts 价冻结 Unicode 文本成本，不能产生第二套单价。
				canvas := &CreativePublicService{Pricing: billing, PricingResolver: resolver}
				base, err := canvas.creativeAudioBasePrice(context.Background(), &group, strings.Repeat("语", 1000))
				require.NoError(t, err)
				require.InDelta(t, price.AudioPrice.Price/1000, base*group.RateMultiplier, 1e-10)
			}
			input, output := marketplaceAudioModalities(tc.mode)
			require.Equal(t, input, models[0].InputModalities)
			require.Equal(t, output, models[0].OutputModalities)
		})
	}
}

func TestModelMarketplaceAudioIdentityAndAmbiguity(t *testing.T) {
	for _, model := range []string{"grok-tts", "grok-voice-tts", "grok-voice-tts-1.0", "xai/GROK-TTS", "grok-tts-2-0"} {
		mode, key, unit := marketplaceGrokAudioIdentity(model)
		require.Equal(t, "tts", mode, model)
		require.Equal(t, "tts", key)
		require.Equal(t, "million_characters", unit)
	}
	for _, model := range []string{"grok-4.6-audio", "grok-2-audio", "grok-speech-1", "grok-voice-custom", "grok-voice-think-fast-99.0", "grok-voice-tts-preview", "gpt-4o-audio-preview", "tts-1", "grok-web-search", "grok-x-search"} {
		mode, _, _ := marketplaceGrokAudioIdentity(model)
		require.Empty(t, mode, model)
	}
	s := NewModelMarketplaceService(nil, nil, nil, NewBillingService(nil, nil), nil, nil, nil)
	g := &Group{ID: 1, Platform: PlatformGrok, RateMultiplier: 1}
	models := s.buildPublicModelsForGroup(context.Background(), g, []marketplaceModelDef{{ID: "my-audio", PricingModel: "grok-tts"}, {ID: "ambiguous", PricingModel: "grok-tts", PricingAmbiguous: true}, {ID: "grok-speech-1", PricingModel: "grok-speech-1"}, {ID: "grok-4.6", PricingModel: "grok-4.6"}})
	require.Equal(t, "my-audio", models[0].ID)
	require.Equal(t, "audio", models[0].Pricing.PricingMode)
	require.Equal(t, "unpriced", models[1].Pricing.PriceStatus)
	require.Nil(t, models[1].Pricing.AudioPrice)
	require.Equal(t, "unpriced", models[2].Pricing.PriceStatus)
	require.Nil(t, models[2].Pricing.AudioPrice)
	require.Equal(t, "token", models[3].Pricing.PricingMode)
	require.Nil(t, models[3].Pricing.AudioPrice)
	g.Platform = PlatformOpenAI
	other := s.getPublicModelDisplayPricing(context.Background(), g, "grok-tts", nil)
	require.Nil(t, other.AudioPrice)
}

func TestModelMarketplaceAudioInvalidPriceDoesNotBecomeFree(t *testing.T) {
	ptr := func(v float64) *float64 { return &v }
	s := NewModelMarketplaceService(nil, nil, nil, NewBillingService(nil, nil), nil, nil, nil)
	for _, value := range []float64{-1, math.NaN(), math.Inf(1)} {
		g := &Group{ID: 1, Platform: PlatformGrok, RateMultiplier: 1, AudioTTSPricePerMillionChars: ptr(value)}
		got := s.getPublicModelDisplayPricing(context.Background(), g, "grok-tts", nil)
		require.Equal(t, "unpriced", got.PriceStatus)
		require.Nil(t, got.AudioPrice)
		g.AudioTTSPricePerMillionChars = ptr(10)
		g.RateMultiplier = value
		got = s.getPublicModelDisplayPricing(context.Background(), g, "grok-tts", nil)
		require.Equal(t, "unpriced", got.PriceStatus)
	}
	g := &Group{ID: 1, Platform: PlatformGrok, RateMultiplier: 1, ModelPricing: []ChannelModelPricing{{Models: []string{"tts"}, BillingMode: BillingModePerRequest}}}
	got := s.getPublicModelDisplayPricing(context.Background(), g, "grok-tts", nil)
	require.Equal(t, "unpriced", got.PriceStatus)
	g.ModelPricing[0].PerRequestPrice = ptr(-1)
	g.RateMultiplier = 0
	got = s.getPublicModelDisplayPricing(context.Background(), g, "grok-tts", nil)
	require.Equal(t, "unpriced", got.PriceStatus)
	for _, value := range []float64{-1, math.NaN(), math.Inf(1)} {
		g.ModelPricing[0].PerRequestPrice = ptr(10)
		g.ModelPricing[0].Intervals = []PricingInterval{{TierLabel: "tts", PerRequestPrice: ptr(value)}}
		g.ModelPricing[0].PriceMultiplier = ptr(0)
		got = s.getPublicModelDisplayPricing(context.Background(), g, "grok-tts", nil)
		require.Equal(t, "unpriced", got.PriceStatus, "非法层级价不能经零倍率显示为免费")
	}
}

func TestModelMarketplaceTTSAliasPriceDoesNotOverrideCanonicalVoicePrice(t *testing.T) {
	canonical, alias := 7.0, 99.0
	g := &Group{ID: 1, Platform: PlatformGrok, RateMultiplier: 1, ModelPricing: []ChannelModelPricing{
		{Models: []string{"grok-voice-tts-1.0"}, BillingMode: BillingModePerRequest, PerRequestPrice: &alias},
		{Models: []string{"tts"}, BillingMode: BillingModePerRequest, PerRequestPrice: &canonical},
	}}
	s := NewModelMarketplaceService(nil, nil, nil, NewBillingService(nil, nil), nil, nil, nil)
	got := s.getPublicModelDisplayPricing(context.Background(), g, "grok-voice-tts-1.0", nil)
	require.Equal(t, canonical, got.AudioPrice.Price)
}

func TestModelMarketplaceRealtimeLatestKeepsExactPriceIdentity(t *testing.T) {
	price := .08
	g := &Group{ID: 1, Platform: PlatformGrok, RateMultiplier: 1, ModelPricing: []ChannelModelPricing{{Models: []string{"grok-voice-think-fast-2.0"}, BillingMode: BillingModePerRequest, PerRequestPrice: &price}}}
	s := NewModelMarketplaceService(nil, nil, nil, NewBillingService(nil, nil), nil, nil, nil)
	latest := s.getPublicModelDisplayPricing(context.Background(), g, "grok-voice-latest", nil)
	version := s.getPublicModelDisplayPricing(context.Background(), g, "grok-voice-think-fast-2.0", nil)
	require.Equal(t, .05, latest.AudioPrice.Price)
	require.Equal(t, .08, version.AudioPrice.Price)
}
