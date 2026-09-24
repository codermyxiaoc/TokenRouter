//go:build unit

package service

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 同时验证目录、目录缺失和价格服务缺失，防止生产降级时借用旧产品费率。
func newSeptemberModelPricingSources(t *testing.T) map[string]*PricingService {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)
	catalog := &PricingService{}
	catalog.pricingData, err = catalog.parsePricingData(body)
	require.NoError(t, err)
	return map[string]*PricingService{
		"catalog": catalog,
		"missing_entries": {pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": {InputCostPerToken: 99e-6},
			"gpt-6":         {InputCostPerToken: 99e-6},
			"claude-opus-5": {InputCostPerToken: 99e-6},
		}},
		"offline": nil,
	}
}

func TestSeptemberModelsOfficialPricingAndModalities(t *testing.T) {
	for source, pricingService := range newSeptemberModelPricingSources(t) {
		for _, model := range []struct {
			id                                  string
			input, output, write, read, write1h float64
			threshold                           int
		}{
			{"gpt-6-sol", 2e-6, 10e-6, 2.5e-6, 0.2e-6, 0, 272000},
			{"gpt-6-luna", 0.1e-6, 0.5e-6, 0.125e-6, 0.01e-6, 0, 272000},
			{"claude-opus-5-5", 4e-6, 20e-6, 5e-6, 0.2e-6, 8e-6, 0},
		} {
			t.Run(source+"/"+model.id, func(t *testing.T) {
				billing := NewBillingService(&config.Config{}, pricingService)
				price, err := billing.GetModelPricing(model.id)
				require.NoError(t, err)
				require.InDelta(t, model.input, price.InputPricePerToken, 1e-15)
				require.InDelta(t, model.output, price.OutputPricePerToken, 1e-15)
				require.InDelta(t, model.write, price.CacheCreationPricePerToken, 1e-15)
				require.InDelta(t, model.read, price.CacheReadPricePerToken, 1e-15)
				require.InDelta(t, model.write1h, price.CacheCreation1hPrice, 1e-15)
				require.Equal(t, model.threshold, price.LongContextInputThreshold)
				if source == "catalog" {
					input, output := pricingService.GetModelModalities(model.id)
					require.Equal(t, []string{"text", "image"}, input)
					require.Equal(t, []string{"text"}, output)
				}
			})
		}
	}
}

func TestSeptemberGPT6BillingThresholdAndTiers(t *testing.T) {
	for source, pricingService := range newSeptemberModelPricingSources(t) {
		for model, inputPrice := range map[string]float64{"gpt-6-sol": 2e-6, "gpt-6-luna": 0.1e-6} {
			for _, input := range []int{271998, 271999} {
				for _, tier := range []string{"default", "priority", "fast", "flex"} {
					t.Run(source+"/"+model+"/"+tier+"/"+strconv.Itoa(input), func(t *testing.T) {
						// 缓存写入/读取也计入272K阈值；正好到边界不提升整次请求价格。
						tokens := UsageTokens{InputTokens: input, OutputTokens: 100, CacheCreationTokens: 1, CacheReadTokens: 1}
						billing := NewBillingService(&config.Config{}, pricingService)
						cost, err := billing.CalculateCostWithServiceTier(model, tokens, 2.5, tier)
						require.NoError(t, err)
						inFactor, outFactor, tierFactor := 1.0, 1.0, 1.0
						if input+2 > 272000 {
							inFactor, outFactor = 2, 1.5
						}
						if tier == "priority" || tier == "fast" {
							tierFactor = 2
						}
						if tier == "flex" {
							tierFactor = 0.5
						}
						wantInput := float64(input) * inputPrice * inFactor * tierFactor
						wantOutput := 100 * inputPrice * 5 * outFactor * tierFactor
						wantRead := inputPrice * 0.1 * inFactor * tierFactor
						wantWrite := inputPrice * 1.25 * inFactor * tierFactor
						require.InDelta(t, wantInput, cost.InputCost, 1e-12)
						require.InDelta(t, wantOutput, cost.OutputCost, 1e-12)
						require.InDelta(t, wantRead, cost.CacheReadCost, 1e-12)
						require.InDelta(t, wantWrite, cost.CacheCreationCost, 1e-12)
						require.InDelta(t, (wantInput+wantOutput+wantRead+wantWrite)*2.5, cost.ActualCost, 1e-12)
						require.Equal(t, input+2 > 272000, cost.LongContextBillingApplied)
					})
				}
			}
		}
	}
}

func TestSeptemberOpus55CacheBreakdownFastAndLegacyIsolation(t *testing.T) {
	for source, pricingService := range newSeptemberModelPricingSources(t) {
		t.Run(source, func(t *testing.T) {
			billing := NewBillingService(&config.Config{}, pricingService)
			for _, alias := range []string{"claude-opus-5-5", "claude-opus-5.5", "anthropic/claude-opus-5-5"} {
				for _, tier := range []string{"default", "fast"} {
					cost, err := billing.CalculateCostWithServiceTier(alias, UsageTokens{
						InputTokens: 300000, OutputTokens: 1000, CacheReadTokens: 5000,
						CacheCreationTokens: 5000, CacheCreation5mTokens: 2000, CacheCreation1hTokens: 3000,
					}, 1, tier)
					require.NoError(t, err)
					factor := 1.0
					if tier == "fast" {
						factor = 2
					}
					require.InDelta(t, 1.2*factor, cost.InputCost, 1e-12)
					require.InDelta(t, 0.02*factor, cost.OutputCost, 1e-12)
					require.InDelta(t, 0.001*factor, cost.CacheReadCost, 1e-12)
					require.InDelta(t, 0.034*factor, cost.CacheCreationCost, 1e-12)
					require.False(t, cost.LongContextBillingApplied)
				}
			}
		})
	}
	// 目录只有新型号时，旧Opus 5仍使用自己的兜底价，不能被子串匹配污染。
	pricing := &PricingService{pricingData: map[string]*LiteLLMModelPricing{"claude-opus-5-5": claudeOpus55FallbackPricing}}
	legacy, err := NewBillingService(&config.Config{}, pricing).GetModelPricing("claude-opus-5")
	require.NoError(t, err)
	require.InDelta(t, 5e-6, legacy.InputPricePerToken, 1e-15)
	require.InDelta(t, 0.5e-6, legacy.CacheReadPricePerToken, 1e-15)
}

func TestSeptemberModelsRespectConfiguredPricing(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna", "claude-opus-5-5"} {
		t.Run(model, func(t *testing.T) {
			own := &LiteLLMModelPricing{InputCostPerToken: 17e-6, OutputCostPerToken: 19e-6, CacheCreationInputTokenCost: 7e-6, CacheReadInputTokenCost: 3e-6}
			catalog := &PricingService{pricingData: map[string]*LiteLLMModelPricing{model: own}}
			require.Same(t, own, catalog.GetModelPricing(model))
			billing := NewBillingService(&config.Config{}, catalog)
			price, err := billing.GetModelPricingWithChannel(model, &ChannelModelPricing{
				BillingMode: BillingModeToken, InputPrice: float64Ptr(0), OutputPrice: float64Ptr(0),
				CacheWritePrice: float64Ptr(0), CacheReadPrice: float64Ptr(0), CacheWrite1hPrice: float64Ptr(0),
			})
			require.NoError(t, err)
			require.Zero(t, price.InputPricePerToken)
			require.Zero(t, price.OutputPricePerToken)
			require.Zero(t, price.CacheCreationPricePerToken)
			require.Zero(t, price.CacheReadPricePerToken)
		})
	}
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		// 使用真实RecordUsage链路验证末尾档位仍命中同产品渠道价，保留分组关闭长上下文规则。
		log := recordUsageWithChannelPricing(t, model+"-high", []ChannelModelPricing{tokenPricingForModels([]string{model}, 0.4)})
		require.InDelta(t, 0.4, log.InputCost, 1e-10)
	}
}

// 比较模型广场展示价与实际结算，覆盖普通、Fast、渠道倍率及长上下文叠加。
func TestSeptemberCacheTTLDisplayMatchesBilling(t *testing.T) {
	for source, pricingService := range newSeptemberModelPricingSources(t) {
		for _, model := range []string{"claude-opus-5-5", "gpt-6-sol", "gpt-6-luna"} {
			for _, customFast := range []*float64{nil, float64Ptr(3), float64Ptr(0)} {
				t.Run(source+"/"+model+"/"+strconv.FormatBool(customFast != nil)+"/"+strconv.Itoa(intValue(customFast)), func(t *testing.T) {
					billing := NewBillingService(&config.Config{}, pricingService)
					var channel *ChannelModelPricing
					if model != "claude-opus-5-5" {
						channel = &ChannelModelPricing{CacheWritePrice: float64Ptr(7e-6), CacheWrite1hPrice: float64Ptr(11e-6)}
					}
					pricing, err := billing.GetModelPricingWithChannel(model, channel)
					require.NoError(t, err)
					pricing.FastModeMultiplier = customFast
					original := *pricing
					for _, inputTokens := range []int{100, 300000} {
						displayPricing := pricing
						if billing.shouldApplySessionLongContextPricing(UsageTokens{InputTokens: inputTokens}, pricing) {
							displayPricing = applyLongContextDisplayMultipliers(pricing)
						}
						for _, tier := range []string{"default", "fast", "priority"} {
							selected := displayPricing
							if tier != "default" {
								var ok bool
								selected, ok = fastModeDisplayPricing(displayPricing)
								require.True(t, ok)
								factor := 2.0
								if customFast != nil {
									factor = *customFast
								}
								require.InDelta(t, displayPricing.CacheCreation5mPrice*factor, selected.CacheCreation5mPrice, 1e-15)
								require.InDelta(t, displayPricing.CacheCreation1hPrice*factor, selected.CacheCreation1hPrice, 1e-15)
							}
							shortPrice, longPrice := cacheCreationDisplayPrices(selected)
							for _, hasTTL := range []bool{false, true} {
								tokens := UsageTokens{InputTokens: inputTokens, CacheCreationTokens: 5000}
								wantCache := 5000 * shortPrice
								if hasTTL {
									tokens.CacheCreation5mTokens, tokens.CacheCreation1hTokens = 2000, 3000
									wantCache = 2000*shortPrice + 3000*longPrice
								}
								cost := billing.computeTokenBreakdown(pricing, tokens, 2.5, tier, true)
								require.InDelta(t, wantCache, cost.CacheCreationCost, 1e-12)
							}
						}
					}
					require.Equal(t, original, *pricing, "展示与计费均不能修改共享定价")
				})
			}
		}
	}
}

// 仅用于测试名称，区分沿用模型默认倍率和渠道显式倍率。
func intValue(value *float64) int {
	if value == nil {
		return -1
	}
	return int(*value)
}
