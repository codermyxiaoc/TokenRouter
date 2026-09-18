//go:build unit

package service

import (
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

// 默认名单约束只作用于没有可用显式模型配置的账号，保留 fork 独立白名单和可选改写。
func TestDeepseekDefaultModelGuardPreservesExplicitConfiguration(t *testing.T) {
	for _, model := range []string{"deepseek-flash", "deepseek-v4-pro", "deepseek-v4-pro-0813", " DEEPSEEK-FLASH[1m][1m] "} {
		require.True(t, (&Account{Platform: PlatformDeepseek}).IsModelSupported(model), model)
	}
	for _, credentials := range []map[string]any{nil, {"model_whitelist": []any{}}, {"model_mapping": map[string]any{}}} {
		require.False(t, (&Account{Platform: PlatformDeepseek, Credentials: credentials}).IsModelSupported("gpt-6-astra"))
	}
	for _, tc := range []struct {
		name      string
		creds     map[string]any
		model     string
		supported bool
	}{
		{"explicit whitelist", map[string]any{"model_whitelist": []any{"custom-model"}}, "custom-model", true},
		{"whitelist blocks others", map[string]any{"model_whitelist": []any{"custom-model"}}, "deepseek-flash", false},
		{"mapping alias", map[string]any{"model_mapping": map[string]any{"alias": "custom-model"}}, "alias", true},
		{"mapping remains optional rewrite", map[string]any{"model_mapping": map[string]any{"alias": "custom-model"}}, "other-model", true},
		{"wildcard mapping", map[string]any{"model_mapping": map[string]any{"custom-*": "deepseek-flash"}}, "custom-123", true},
		{"mapping obeys final whitelist", map[string]any{"model_mapping": map[string]any{"alias": "custom-model"}, "model_whitelist": []any{"deepseek-flash"}}, "alias", false},
		{"legacy self map", map[string]any{"model_mapping": map[string]any{"custom-model": "custom-model"}}, "custom-model", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.supported, (&Account{Platform: PlatformDeepseek, Credentials: tc.creds}).IsModelSupported(tc.model))
		})
	}
	require.True(t, (&Account{Platform: PlatformKimi}).IsModelSupported("custom-model"))
	require.Equal(t, "deepseek-flash", normalizeOpenAIModelForUpstream(&Account{Platform: PlatformDeepseek}, " deepseek-flash[1m][1m] "))
	require.Equal(t, "model[1m]", normalizeOpenAIModelForUpstream(&Account{Platform: PlatformKimi}, "model[1m]"))
	require.True(t, openAIModelSupportsMaxReasoningEffort("deepseek-flash"))
}

// 官方当前仍保留 Pro 独立价格；9 月 14 日前后两个默认来源一致，显式分组/渠道价不变。
func TestDeepseekOfficialPricingRetainsProAndSources(t *testing.T) {
	referenceTime := time.Date(2026, 9, 14, 4, 0, 0, 0, time.UTC)
	bs := NewBillingService(&config.Config{}, nil)
	resolver := NewModelPricingResolver(nil, bs)
	for _, model := range []string{"deepseek-v4-pro", "deepseek-v4-pro-0813"} {
		for _, tc := range []struct {
			name                 string
			at                   time.Time
			input, output, cache float64
		}{
			{"before", referenceTime.Add(-time.Nanosecond), 6.6e-7, 1.98e-6, 2.2e-8},
			{"at", referenceTime, 6.6e-7, 1.98e-6, 2.2e-8},
			{"after", referenceTime.Add(time.Nanosecond), 6.6e-7, 1.98e-6, 2.2e-8},
		} {
			t.Run(model+tc.name, func(t *testing.T) {
				price, err := bs.GetModelPricing(model)
				require.NoError(t, err)
				require.Equal(t, tc.input, price.InputPricePerToken)
				require.Equal(t, tc.output, price.OutputPricePerToken)
				require.Equal(t, tc.cache, price.CacheReadPricePerToken)
				for _, source := range []string{PricingSourceLiteLLM, PricingSourceFallback} {
					resolved := &ResolvedPricing{Source: source, BasePricing: &ModelPricing{InputPricePerToken: 9, OutputPricePerToken: 8, CacheReadPricePerToken: 7}}
					cost, err := bs.calculateTokenCost(resolved, CostInput{Model: model, Tokens: UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 200}, RateMultiplier: 1, PricingAt: tc.at, Resolver: resolver})
					require.NoError(t, err)
					require.InDelta(t, (1000*tc.input+500*tc.output+200*tc.cache)*deepseekPeakMultiplierAt(tc.at), cost.TotalCost, 1e-12)
					require.Equal(t, float64(9), resolved.BasePricing.InputPricePerToken, "不能污染共享价卡")
				}
			})
		}
	}
	for _, source := range []string{PricingSourceGroup, PricingSourceChannel} {
		for _, at := range []time.Time{referenceTime.Add(-time.Hour), referenceTime.Add(3 * time.Hour)} {
			resolved := &ResolvedPricing{Source: source, BasePricing: &ModelPricing{InputPricePerToken: 2e-6, OutputPricePerToken: 4e-6, CacheReadPricePerToken: 0}}
			cost, err := bs.calculateTokenCost(resolved, CostInput{Model: "deepseek-v4-pro", Tokens: UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 200}, RateMultiplier: 1, PricingAt: at, Resolver: resolver})
			require.NoError(t, err)
			require.InDelta(t, 0.004, cost.TotalCost, 1e-12)
		}
	}
	price, err := bs.GetModelPricing("deepseek-flash")
	require.NoError(t, err)
	require.Equal(t, 1.5e-7, price.InputPricePerToken)
}
