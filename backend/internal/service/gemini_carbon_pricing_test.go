//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// Carbon 档位别名归一到基名价卡，单独发布的档位价优先。
func TestGeminiCarbonPricingAliases(t *testing.T) {
	base := &LiteLLMModelPricing{InputCostPerToken: 2e-6, OutputCostPerToken: 10e-6, CacheReadInputTokenCost: 0.1e-6}
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{"gemini-4-carbon": base}}
	for _, model := range []string{
		"gemini-4-carbon", "gemini-4-carbon-high", "gemini-4-carbon-medium", "gemini-4-carbon-low", "gemini-4-carbon-tiered",
		"models/gemini-4-carbon", "projects/p/locations/global/publishers/google/models/gemini-4-carbon-high",
	} {
		t.Run(model, func(t *testing.T) {
			require.Same(t, base, svc.GetModelPricing(model))
		})
	}
	// A separately published tier price must take precedence over base normalization.
	tier := &LiteLLMModelPricing{InputCostPerToken: 3e-6}
	svc.pricingData["gemini-4-carbon-high"] = tier
	require.Same(t, tier, svc.GetModelPricing("gemini-4-carbon-high"))
	require.Equal(t, "gemini-4-carbon-cyber", normalizeGeminiThinkingTierAlias("gemini-4-carbon-cyber"))
}

// Carbon 推广期兜底价：$2/$10/$0.10，Priority 1.8 倍。
func TestGeminiCarbonFallbackBilling(t *testing.T) {
	original := gemini4CarbonStandardPriceFrom
	gemini4CarbonStandardPriceFrom = time.Now().Add(time.Hour)
	t.Cleanup(func() { gemini4CarbonStandardPriceFrom = original })
	svc := NewBillingService(&config.Config{}, nil)
	tokens := UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000}
	for _, model := range []string{"gemini-4-carbon", "gemini-4-carbon-low", "gemini-4-carbon-medium", "gemini-4-carbon-high", "models/gemini-4-carbon", "projects/p/locations/global/publishers/google/models/gemini-4-carbon"} {
		t.Run(model, func(t *testing.T) {
			cost, err := svc.CalculateCost(model, tokens, 1)
			require.NoError(t, err)
			require.InDelta(t, 2, cost.InputCost, 1e-12)
			require.InDelta(t, 10, cost.OutputCost, 1e-12)
			require.InDelta(t, 0.1, cost.CacheReadCost, 1e-12)
			require.InDelta(t, 12.1, cost.TotalCost, 1e-12)
			priority, err := svc.CalculateCostWithServiceTier(model, tokens, 1, "priority")
			require.NoError(t, err)
			require.InDelta(t, 12.1*1.8, priority.TotalCost, 1e-12)
			require.Zero(t, svc.CacheStoragePricePerTokenHour(model))
		})
	}
}

// 2026-12-01 起 Carbon 官方价翻倍，兜底价必须同步切换，避免低估费用。
func TestGeminiCarbonFallbackSwitchesToStandardPrice(t *testing.T) {
	original := gemini4CarbonStandardPriceFrom
	gemini4CarbonStandardPriceFrom = time.Now().Add(-time.Hour)
	t.Cleanup(func() { gemini4CarbonStandardPriceFrom = original })
	svc := NewBillingService(&config.Config{}, nil)
	tokens := UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000}
	cost, err := svc.CalculateCost("gemini-4-carbon-high", tokens, 1)
	require.NoError(t, err)
	require.InDelta(t, 4, cost.InputCost, 1e-12)
	require.InDelta(t, 20, cost.OutputCost, 1e-12)
	require.InDelta(t, 0.2, cost.CacheReadCost, 1e-12)
	priority, err := svc.CalculateCostWithServiceTier("gemini-4-carbon", tokens, 1, "priority")
	require.NoError(t, err)
	require.InDelta(t, 24.2*1.8, priority.TotalCost, 1e-12)
}

// Hydrogen 与 Qwen3 向量/重排模型的兜底价；未知档位不猜测计价。
func TestGeminiHydrogenAndQwen3FallbackPricing(t *testing.T) {
	svc := NewBillingService(&config.Config{}, nil)
	tokens := UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000}
	cost, err := svc.CalculateCost("gemini-4-hydrogen", tokens, 1)
	require.NoError(t, err)
	require.InDelta(t, 1.5+7.5+0.075, cost.TotalCost, 1e-12)
	priority, err := svc.CalculateCostWithServiceTier("gemini-4-hydrogen", tokens, 1, "priority")
	require.NoError(t, err)
	require.InDelta(t, (1.5+7.5+0.075)*1.8, priority.TotalCost, 1e-12)

	for model, price := range map[string]float64{
		"Qwen/Qwen3-Embedding-8B": 0.04, "qwen3-embedding-4b": 0.02, "qwen3-embedding-0.6b": 0.01,
		"Qwen/Qwen3-Reranker-8B": 0.04, "qwen3-reranker-4b": 0.02, "qwen3-reranker-0-6b": 0.01,
	} {
		cost, err := svc.CalculateCost(model, UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000}, 1)
		require.NoError(t, err, model)
		require.InDelta(t, price, cost.TotalCost, 1e-12, model)
	}
	require.Nil(t, svc.getFallbackPricing("qwen3-embedding-2b"))
	require.Nil(t, svc.getFallbackPricing("qwen3-vl-reranker-8b"))
}
