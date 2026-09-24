//go:build unit

package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 同时从原始目录和字段覆盖文件加载，覆盖显式零值及 null 删除的真实解析链路。
func septemberOverridePricing(t *testing.T, model string, patch map[string]any, overrideFile bool) *BillingService {
	t.Helper()
	entry := map[string]any{
		"input_cost_per_token": 2e-6, "output_cost_per_token": 10e-6,
		"cache_creation_input_token_cost": 2.5e-6, "cache_read_input_token_cost": 0.2e-6,
	}
	pricing := &PricingService{cfg: &config.Config{}}
	if overrideFile {
		body, err := json.Marshal(map[string]any{model: patch})
		require.NoError(t, err)
		pricing.cfg.Pricing.OverrideFile = filepath.Join(t.TempDir(), "prices.json")
		require.NoError(t, os.WriteFile(pricing.cfg.Pricing.OverrideFile, body, 0600))
	} else {
		for key, value := range patch {
			entry[key] = value
		}
	}
	body, err := json.Marshal(map[string]any{model: entry})
	require.NoError(t, err)
	pricing.pricingData, err = pricing.parsePricingData(body)
	require.NoError(t, err)
	return NewBillingService(&config.Config{}, pricing)
}

func TestSeptemberCatalogOverridesPreserveExplicitPrices(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		for _, source := range []string{"catalog", "override"} {
			for _, tc := range []struct {
				name          string
				patch         map[string]any
				standardWrite float64
				priority      [4]float64
			}{
				{
					name:          "explicit_free_cache",
					patch:         map[string]any{"cache_creation_input_token_cost": 0},
					standardWrite: 0, priority: [4]float64{4e-6, 20e-6, 0, 0.4e-6},
				},
				{
					name: "custom_priority",
					patch: map[string]any{"input_cost_per_token_priority": 7e-6, "output_cost_per_token_priority": 31e-6,
						"cache_creation_input_token_cost_priority": 4e-6, "cache_read_input_token_cost_priority": 0.7e-6},
					standardWrite: 2.5e-6, priority: [4]float64{7e-6, 31e-6, 4e-6, 0.7e-6},
				},
				{
					name: "all_priority_free",
					patch: map[string]any{"input_cost_per_token_priority": 0, "output_cost_per_token_priority": 0,
						"cache_creation_input_token_cost_priority": 0, "cache_read_input_token_cost_priority": 0},
					standardWrite: 2.5e-6, priority: [4]float64{},
				},
				{
					name:          "partial_priority_free",
					patch:         map[string]any{"cache_read_input_token_cost_priority": 0},
					standardWrite: 2.5e-6, priority: [4]float64{4e-6, 20e-6, 5e-6, 0},
				},
				{
					name:          "missing_prices_use_official_ratios",
					patch:         map[string]any{"cache_creation_input_token_cost": nil, "input_cost_per_token_priority": nil},
					standardWrite: 2.5e-6, priority: [4]float64{4e-6, 20e-6, 5e-6, 0.4e-6},
				},
			} {
				t.Run(model+"/"+source+"/"+tc.name, func(t *testing.T) {
					billing := septemberOverridePricing(t, model, tc.patch, source == "override")
					price, err := billing.GetModelPricing(model)
					require.NoError(t, err)
					require.InDelta(t, tc.standardWrite, price.CacheCreationPricePerToken, 1e-15)
					// 全部 Fast 单价显式免费时，模型广场仍须展示该档位。
					fastPrice, ok := fastModeDisplayPricing(price)
					require.True(t, ok)
					shown := []float64{fastPrice.InputPricePerToken, fastPrice.OutputPricePerToken, fastPrice.CacheCreationPricePerToken, fastPrice.CacheReadPricePerToken}
					for i, want := range tc.priority {
						require.InDelta(t, want, shown[i], 1e-15)
					}
					tokens := UsageTokens{InputTokens: 10000, OutputTokens: 10000, CacheCreationTokens: 10000, CacheReadTokens: 10000}
					for _, tier := range []string{"priority", "fast"} {
						cost, err := billing.CalculateCostWithServiceTier(model, tokens, 1, tier)
						require.NoError(t, err)
						actual := []float64{cost.InputCost, cost.OutputCost, cost.CacheCreationCost, cost.CacheReadCost}
						for i, want := range tc.priority {
							require.InDelta(t, want*10000, actual[i], 1e-12)
						}
					}
					// Fast 计算不得回写目录或影响普通请求的缓存免费配置。
					standard, err := billing.CalculateCostWithServiceTier(model, tokens, 1, "default")
					require.NoError(t, err)
					require.InDelta(t, tc.standardWrite*10000, standard.CacheCreationCost, 1e-12)
				})
			}
		}
	}
}

func TestSeptemberCatalogOverridesPreserveChannelPricing(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		t.Run(model, func(t *testing.T) {
			billing := septemberOverridePricing(t, model, nil, false)
			channel := &ChannelModelPricing{BillingMode: BillingModeToken,
				InputPrice: float64Ptr(3e-6), OutputPrice: float64Ptr(11e-6),
				CacheWritePrice: float64Ptr(6e-6), CacheReadPrice: float64Ptr(0),
			}
			cost, err := billing.calculateCostInternal(model, UsageTokens{
				InputTokens: 10000, OutputTokens: 10000, CacheCreationTokens: 10000, CacheReadTokens: 10000,
			}, 1, "priority", channel)
			require.NoError(t, err)
			require.InDelta(t, 0.06, cost.InputCost, 1e-12)
			require.InDelta(t, 0.22, cost.OutputCost, 1e-12)
			require.InDelta(t, 0.12, cost.CacheCreationCost, 1e-12)
			require.Zero(t, cost.CacheReadCost)
		})
	}
}

func TestSeptemberCatalogOverridesKeepLegacyGPTPolicy(t *testing.T) {
	// 旧型号仍执行原固定 Fast 倍率及缺省缓存写价策略，不随新型号调整。
	for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-luna"} {
		t.Run(model, func(t *testing.T) {
			billing := septemberOverridePricing(t, model, map[string]any{
				"cache_creation_input_token_cost": 0, "input_cost_per_token_priority": 7e-6,
			}, false)
			price, err := billing.GetModelPricing(model)
			require.NoError(t, err)
			require.InDelta(t, 2.5e-6, price.CacheCreationPricePerToken, 1e-15)
			require.InDelta(t, 4e-6, price.InputPricePerTokenPriority, 1e-15)
			require.False(t, price.InputPricePerTokenPriorityExplicit)
		})
	}
}
