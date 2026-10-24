package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// Fast 的目录价、供应商前缀和 Build 名称必须一致，不能借用标准型号价格。
func TestSyncedGrok47FastCatalogPricing(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)
	pricing := &PricingService{}
	pricing.pricingData, err = pricing.parsePricingData(data)
	require.NoError(t, err)
	for _, model := range []string{"grok-4.7-fast", "xai/grok-4.7-fast", "grok-4.7-build-fast", "x-ai/grok-4.7-build-fast"} {
		t.Run(model, func(t *testing.T) {
			card := pricing.GetModelPricing(model)
			require.NotNil(t, card)
			require.InDelta(t, 4e-6, card.InputCostPerToken, 1e-15)
			require.InDelta(t, 1e-6, card.CacheReadInputTokenCost, 1e-15)
			require.InDelta(t, 12e-6, card.OutputCostPerToken, 1e-15)
			require.Equal(t, 200000, card.LongContextInputTokenThreshold)
			require.InDelta(t, 1.5, card.LongContextInputCostMultiplier, 1e-12)
		})
	}
	billing := NewBillingService(&config.Config{}, pricing)
	standard, err := billing.GetModelPricing("grok-4.7")
	require.NoError(t, err)
	require.InDelta(t, 2e-6, standard.InputPricePerToken, 1e-15)
	cost, err := billing.CalculateCost("grok-4.7-build-fast", UsageTokens{InputTokens: 150000, CacheReadTokens: 50000, OutputTokens: 10000}, 1)
	require.NoError(t, err)
	require.True(t, cost.LongContextBillingApplied)
	require.InDelta(t, 0.9, cost.InputCost, 1e-12)
	require.InDelta(t, 0.075, cost.CacheReadCost, 1e-12)
	require.InDelta(t, 0.18, cost.OutputCost, 1e-12)
	// 管理员精确配置（包括零价）优先，未知后缀不得自动继承 Fast 卡。
	custom := &LiteLLMModelPricing{LiteLLMProvider: "xai"}
	pricing.pricingData["grok-4.7-build-fast"] = custom
	require.Same(t, custom, pricing.GetModelPricing("grok-4.7-build-fast"))
	require.Nil(t, pricing.GetModelPricing("grok-4.7-fast-unknown"))
}
