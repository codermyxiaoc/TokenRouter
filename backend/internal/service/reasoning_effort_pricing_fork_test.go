//go:build unit

package service

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// 保证升级或只设置其它档位时，旧 Max 价格及 Fable 默认价都保持不变。
func TestReasoningEffortPricingLegacyAndConfigured(t *testing.T) {
	legacy := 2.5
	for _, tc := range []struct {
		name, model, effort string
		pricing             *ModelPricing
		want                float64
	}{
		{"fable-default", "claude-fable-5-1", "max", nil, 3},
		{"empty-map", "claude-fable-5-1", "max", &ModelPricing{ReasoningEffortMultipliers: map[string]float64{}}, 3},
		{"other-effort-keeps-legacy", "claude-fable-5-1", "max", &ModelPricing{MaxReasoningEffortMultiplier: &legacy, ReasoningEffortMultipliers: map[string]float64{"high": 4}}, 2.5},
		{"explicit-overrides-legacy", "claude-fable-5-1", "max", &ModelPricing{MaxReasoningEffortMultiplier: &legacy, ReasoningEffortMultipliers: map[string]float64{"max": 1}}, 1},
		{"high", "gpt-6-sol", " HIGH ", &ModelPricing{ReasoningEffortMultipliers: map[string]float64{"high": 1.5}}, 1.5},
		{"none-explicit", "gpt-6-sol", "none", &ModelPricing{ReasoningEffortMultipliers: map[string]float64{"none": 0.5}}, 0.5},
		{"no-level-is-not-none", "gpt-6-sol", "", &ModelPricing{ReasoningEffortMultipliers: map[string]float64{"none": 0.5}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, reasoningEffortBillingMultiplier(tc.model, tc.effort, tc.pricing))
		})
	}
}

// 同一价格经过区间、缓存、分组倍率后仅叠加一次档位倍率；媒体按次始终不受影响。
func TestReasoningEffortPricingTokenBucketsAndMediaIsolation(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)
	resolved := &ResolvedPricing{Mode: BillingModeToken, BasePricing: &ModelPricing{
		InputPricePerToken: 0.01, OutputPricePerToken: 0.02, ImageInputPricePerToken: 0.03,
		ImageOutputPricePerToken: 0.04, CacheCreationPricePerToken: 0.005, CacheReadPricePerToken: 0.001,
		ReasoningEffortMultipliers: map[string]float64{"high": 2},
	}}
	input := CostInput{Ctx: context.Background(), Model: "test-model", Resolver: resolver, Resolved: resolved, RateMultiplier: 3,
		Tokens: UsageTokens{InputTokens: 100, OutputTokens: 20, ImageInputTokens: 10, ImageOutputTokens: 5, CacheCreationTokens: 10, CacheReadTokens: 50}}
	base, err := bs.CalculateCostUnified(input)
	require.NoError(t, err)
	input.ReasoningEffort = "high"
	withEffort, err := bs.CalculateCostUnified(input)
	require.NoError(t, err)
	require.InDelta(t, base.TotalCost*2, withEffort.TotalCost, 1e-12)
	require.InDelta(t, base.ActualCost*2, withEffort.ActualCost, 1e-12)
	require.InDelta(t, base.ImageInputCost*2, withEffort.ImageInputCost, 1e-12)
	require.InDelta(t, base.CacheReadCost*2, withEffort.CacheReadCost, 1e-12)
	resolved.Intervals = []PricingInterval{{MinTokens: 0, InputPrice: testPtrFloat64(0)}}
	withEffort, err = bs.CalculateCostUnified(input)
	require.NoError(t, err)
	require.Zero(t, withEffort.InputCost)
	for _, mode := range []BillingMode{BillingModePerRequest, BillingModeImage, BillingModeVideo} {
		input.Resolved = &ResolvedPricing{Mode: mode, DefaultPerRequestPrice: 0.1, channelPricing: &ChannelModelPricing{ReasoningEffortMultipliers: map[string]float64{"high": 100}}}
		input.RequestCount = 2
		cost, err := bs.CalculateCostUnified(input)
		require.NoError(t, err)
		require.InDelta(t, 0.2, cost.TotalCost, 1e-12)
	}
}

func TestReasoningEffortPricingValidationAndCacheIsolation(t *testing.T) {
	for _, value := range []float64{0, -1, math.Inf(1), math.NaN()} {
		require.Error(t, checkBillingModeRequirements(ChannelModelPricing{ReasoningEffortMultipliers: map[string]float64{"high": value}}))
	}
	require.Error(t, checkBillingModeRequirements(ChannelModelPricing{ReasoningEffortMultipliers: map[string]float64{"typo": 2}}))
	require.Error(t, checkBillingModeRequirements(ChannelModelPricing{BillingMode: BillingModeImage, PerRequestPrice: testPtrFloat64(1), ReasoningEffortMultipliers: map[string]float64{"high": 2}}))
	require.Error(t, validateAccountStatsPricingEntries([]ChannelModelPricing{{ReasoningEffortMultipliers: map[string]float64{"high": 2}}}))
	pricing := ChannelModelPricing{ReasoningEffortMultipliers: map[string]float64{"high": 2}}
	require.True(t, pricing.HasEffectivePricing())
	cloned := pricing.Clone()
	cloned.ReasoningEffortMultipliers["high"] = 9
	require.Equal(t, 2.0, pricing.ReasoningEffortMultipliers["high"])
}

func TestReasoningEffortPricingGeminiOutboundLevel(t *testing.T) {
	for _, body := range []string{`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"HIGH"}}}`, `{"generation_config":{"thinking_config":{"thinking_level":"high"}}}`} {
		require.Equal(t, "high", *extractGeminiReasoningEffortFromBody([]byte(body)))
	}
	require.Nil(t, extractGeminiReasoningEffortFromBody([]byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}`)))
}
