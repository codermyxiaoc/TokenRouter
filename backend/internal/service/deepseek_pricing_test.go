//go:build unit

package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

func TestDeepseekPeakMultiplierAt(t *testing.T) {
	weekday := func(hour, minute int) time.Time {
		return time.Date(2026, 8, 24, hour, minute, 0, 0, time.UTC)
	}
	tests := []struct {
		name string
		now  time.Time
		want float64
	}{
		{name: "weekday peak start", now: weekday(1, 0), want: 2},
		{name: "weekday peak end", now: weekday(4, 0), want: 1},
		{name: "weekday second peak", now: weekday(6, 30), want: 2},
		{name: "weekday second peak end", now: weekday(10, 0), want: 1},
		{name: "weekday off peak", now: weekday(12, 0), want: 1},
		{name: "beijing weekend", now: time.Date(2026, 8, 22, 2, 0, 0, 0, time.UTC), want: 1},
		{name: "utc weekend boundary", now: time.Date(2026, 8, 22, 16, 30, 0, 0, time.UTC), want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, deepseekPeakMultiplierAt(tt.now))
		})
	}
}

func TestGetModelPricing_DeepseekUsesOfficialRatesForStaleEntries(t *testing.T) {
	pricingService := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"deepseek-v4-pro":      {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, CacheReadInputTokenCost: 3e-8},
		"deepseek-v4-flash":    {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, CacheReadInputTokenCost: 3e-8},
		"deepseek-v3-2-251201": {InputCostPerToken: 0, OutputCostPerToken: 0},
	}}
	bs := NewBillingService(&config.Config{}, pricingService)

	tests := []struct {
		model                 string
		input, output, cached float64
	}{
		{model: "deepseek-v4-pro", input: deepseekProOffPeakInputPrice, output: deepseekProOffPeakOutputPrice, cached: deepseekProOffPeakCacheRead},
		{model: "deepseek-v4-flash", input: deepseekFlashOffPeakInputPrice, output: deepseekFlashOffPeakOutputPrice, cached: deepseekFlashOffPeakCacheRead},
		{model: "deepseek-v4-pro-0813", input: deepseekProOffPeakInputPrice, output: deepseekProOffPeakOutputPrice, cached: deepseekProOffPeakCacheRead},
		{model: "deepseek-v3-2-251201", input: deepseekFlashOffPeakInputPrice, output: deepseekFlashOffPeakOutputPrice, cached: deepseekFlashOffPeakCacheRead},
		{model: "deepseek-unknown", input: deepseekFlashOffPeakInputPrice, output: deepseekFlashOffPeakOutputPrice, cached: deepseekFlashOffPeakCacheRead},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			pricing, err := bs.GetModelPricing(tt.model)
			require.NoError(t, err)
			require.InDelta(t, tt.input, pricing.InputPricePerToken, 1e-15)
			require.InDelta(t, tt.output, pricing.OutputPricePerToken, 1e-15)
			require.InDelta(t, tt.cached, pricing.CacheReadPricePerToken, 1e-15)
		})
	}
}

func TestCalculateCostUnified_DeepseekPeakDoesNotOverrideGroupPricing(t *testing.T) {
	bs := NewBillingService(&config.Config{}, nil)
	resolver := NewModelPricingResolver(nil, bs)
	inputPrice, outputPrice := 1e-6, 2e-6
	group := &Group{
		ID:       1,
		Platform: PlatformDeepseek,
		ModelPricing: []ChannelModelPricing{{
			Models:      []string{"deepseek-v4-flash"},
			BillingMode: BillingModeToken,
			InputPrice:  &inputPrice,
			OutputPrice: &outputPrice,
		}},
	}
	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 1000}
	for _, pricingAt := range []time.Time{
		time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC),
	} {
		cost, err := bs.CalculateCostUnified(CostInput{
			Ctx: context.Background(), Model: "deepseek-v4-flash", Group: group,
			Tokens: tokens, RateMultiplier: 1, PricingAt: pricingAt, Resolver: resolver,
		})
		require.NoError(t, err)
		require.InDelta(t, 1000*inputPrice+500*outputPrice+1000*deepseekFlashOffPeakCacheRead, cost.TotalCost, 1e-12)
	}
}

func TestDeepseekPricingFileContainsOnlyCurrentCatalogEntries(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)
	pricingService := &PricingService{}
	pricingData, err := pricingService.parsePricingData(data)
	require.NoError(t, err)

	for _, removed := range []string{"deepseek-chat", "deepseek-reasoner", "deepseek-v3-2-251201"} {
		_, exists := pricingData[removed]
		require.False(t, exists, "%s 不应继续作为本地价格目录条目", removed)
	}
	for _, model := range []string{"deepseek-v4-flash", "deepseek-v4-flash-vision-exp", "deepseek-v4-pro"} {
		entry, exists := pricingData[model]
		require.True(t, exists, "%s 必须存在于本地价格目录", model)
		require.NotNil(t, entry)
	}
}

// override_file 显式给出的三价（含 0）是运营者最终价；未覆盖的字段仍按官方默认价与峰谷规则。
func TestDeepseekOverrideFileTokenPrices(t *testing.T) {
	type prices struct{ input, output, cacheRead float64 }
	official := prices{1.5e-7, 6e-7, 3e-9}
	officialPeak := prices{3e-7, 1.2e-6, 6e-9}
	custom := prices{2e-6, 3e-6, 4e-7}
	fullPatch := `{"input_cost_per_token":0.000002,"output_cost_per_token":0.000003,"cache_read_input_token_cost":0.0000004}`
	cases := []struct {
		name, model, layer, patch string
		offPeak, peak             prices
	}{
		{"flash full", "deepseek-flash", "catalog", fullPatch, custom, custom},
		{"pro full", "deepseek-v4-pro", "catalog", fullPatch, custom, custom},
		{"versioned pro full", "deepseek-v4-pro-0813", "catalog", fullPatch, custom, custom},
		{"fallback full", "deepseek-flash", "fallback", fullPatch, custom, custom},
		{"custom model override only", "deepseek-custom", "override", fullPatch, custom, custom},
		{"override only", "deepseek-flash", "override", fullPatch, custom, custom},
		{"input only", "deepseek-flash", "catalog", `{"input_cost_per_token":0.000002}`, prices{2e-6, 6e-7, 3e-9}, prices{2e-6, 1.2e-6, 6e-9}},
		{"zero input", "deepseek-flash", "catalog", `{"input_cost_per_token":0}`, prices{0, 6e-7, 3e-9}, prices{0, 1.2e-6, 6e-9}},
		{"zero output", "deepseek-flash", "catalog", `{"output_cost_per_token":0}`, prices{1.5e-7, 0, 3e-9}, prices{3e-7, 0, 6e-9}},
		{"zero cache read", "deepseek-flash", "catalog", `{"cache_read_input_token_cost":0}`, prices{1.5e-7, 6e-7, 0}, prices{3e-7, 1.2e-6, 0}},
		{"metadata only", "deepseek-flash", "catalog", `{"supports_prompt_caching":true}`, official, officialPeak},
		{"null input", "deepseek-flash", "catalog", `{"input_cost_per_token":null}`, official, officialPeak},
		{"negative input", "deepseek-flash", "catalog", `{"input_cost_per_token":-0.000002}`, official, officialPeak},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := &config.Config{}
			cfg.Pricing.DataDir = dir
			cfg.Pricing.OverrideFile = filepath.Join(dir, "overrides.json")
			require.NoError(t, os.WriteFile(cfg.Pricing.OverrideFile, []byte(`{"`+tc.model+`":`+tc.patch+`}`), 0644))
			// 故意给出过时目录价：运营者未显式覆盖的字段仍需被纠正为官方价。
			catalog := []byte(`{"` + tc.model + `":{"input_cost_per_token":0.00001,"output_cost_per_token":0.00002,"cache_read_input_token_cost":0.000001}}`)
			if tc.layer == "fallback" {
				cfg.Pricing.FallbackFile = filepath.Join(dir, "fallback.json")
				require.NoError(t, os.WriteFile(cfg.Pricing.FallbackFile, catalog, 0644))
			}
			if tc.layer != "catalog" {
				catalog = []byte(`{"other-model":{"input_cost_per_token":0.000001}}`)
			}
			svc := &PricingService{cfg: cfg}
			require.NoError(t, os.WriteFile(svc.getPricingFilePath(), catalog, 0644))
			require.NoError(t, svc.loadPricingData(svc.getPricingFilePath()))
			billing := NewBillingService(cfg, svc)
			resolver := NewModelPricingResolver(nil, billing)
			tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 2000}
			for _, slot := range []struct {
				name string
				at   time.Time
				want prices
			}{
				{"off peak", time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC), tc.offPeak},
				{"peak", time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC), tc.peak},
			} {
				t.Run(slot.name, func(t *testing.T) {
					cost, err := billing.CalculateCostUnified(CostInput{
						Ctx: context.Background(), Model: tc.model, Tokens: tokens, RateMultiplier: 1.25,
						Resolver: resolver, PricingAt: slot.at,
					})
					require.NoError(t, err)
					require.InDelta(t, 1000*slot.want.input, cost.InputCost, 1e-12)
					require.InDelta(t, 500*slot.want.output, cost.OutputCost, 1e-12)
					require.InDelta(t, 2000*slot.want.cacheRead, cost.CacheReadCost, 1e-12)
					want := 1000*slot.want.input + 500*slot.want.output + 2000*slot.want.cacheRead
					require.InDelta(t, want, cost.TotalCost, 1e-12)
					require.InDelta(t, want*1.25, cost.ActualCost, 1e-12)
					// fork 的账号统计价按真实当前时刻计价，不接受注入时点；只在低谷时点且当前
					// 真实时间也不在峰时窗口时核对 override 价同样生效，避免测试结果依赖运行时刻。
					if slot.name == "off peak" && deepseekPeakMultiplierAt(timezone.Now()) <= 1 {
						stats := tryModelFilePricing(billing, tc.model, tokens, "")
						require.NotNil(t, stats)
						require.InDelta(t, want, *stats, 1e-12, "account statistics use the same override rates")
					}
				})
			}
			// 目录查询与旧估算接口同样保留 override 价。
			pricing, err := billing.GetModelPricing(tc.model)
			require.NoError(t, err)
			require.InDelta(t, tc.offPeak.input, pricing.InputPricePerToken, 1e-15)
			require.InDelta(t, tc.offPeak.output, pricing.OutputPricePerToken, 1e-15)
			require.InDelta(t, tc.offPeak.cacheRead, pricing.CacheReadPricePerToken, 1e-15)
			// 旧估算接口按真实当前时刻计价；峰时窗口内会叠加官方峰值倍率，此时跳过低谷价断言。
			if deepseekPeakMultiplierAt(timezone.Now()) <= 1 {
				estimate, err := billing.CalculateCost(tc.model, tokens, 1)
				require.NoError(t, err)
				want := 1000*tc.offPeak.input + 500*tc.offPeak.output + 2000*tc.offPeak.cacheRead
				require.InDelta(t, want, estimate.TotalCost, 1e-12)
			}
		})
	}
}

// 删除 override 后热重载应恢复官方价策略。
func TestDeepseekOverrideFileReloadRestoresOfficialPolicy(t *testing.T) {
	svc := newHotReloadPricingService(t, `{"deepseek-flash":{"input_cost_per_token":0.00001,"output_cost_per_token":0.00002}}`,
		`{"deepseek-flash":{"input_cost_per_token":0.000002,"output_cost_per_token":0.000003,"cache_read_input_token_cost":0}}`)
	billing := NewBillingService(svc.cfg, svc)
	resolver := NewModelPricingResolver(nil, billing)
	check := func(wantInput, wantOutput, wantCacheRead float64) {
		t.Helper()
		cost, err := billing.CalculateCostUnified(CostInput{Ctx: context.Background(), Model: "deepseek-flash",
			Tokens: UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 2000}, RateMultiplier: 1,
			Resolver: resolver, PricingAt: time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC)})
		require.NoError(t, err)
		require.InDelta(t, wantInput, cost.InputCost, 1e-12)
		require.InDelta(t, wantOutput, cost.OutputCost, 1e-12)
		require.InDelta(t, wantCacheRead, cost.CacheReadCost, 1e-12)
	}
	check(0.002, 0.0015, 0)
	require.NoError(t, os.WriteFile(svc.cfg.Pricing.OverrideFile, []byte(`{"deepseek-flash":{"output_cost_per_token":0}}`), 0644))
	svc.reloadIfCustomFilesChanged()
	check(0.0003, 0, 0.000012)
	require.NoError(t, os.Remove(svc.cfg.Pricing.OverrideFile))
	svc.reloadIfCustomFilesChanged()
	check(0.0003, 0.0006, 0.000012)
}
