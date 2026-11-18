//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 上游端点价格倍数只放大默认模型价的 token 费用，存储费不受影响。
func TestUpstreamPriceMultiplierScalesTokenCostOnly(t *testing.T) {
	pricing := &PricingService{}
	data, err := pricing.parsePricingData([]byte(`{"gemini-3.7-flash":{"litellm_provider":"gemini","input_cost_per_token":0.00000075,
		"output_cost_per_token":0.00000375,"cache_read_input_token_cost":0.000000075,"cache_storage_cost_per_token_per_hour":0.000001}}`))
	require.NoError(t, err)
	pricing.pricingData = data
	gateway := &GatewayService{billingService: NewBillingService(&config.Config{}, pricing)}
	groupID := int64(3)
	apiKey := &APIKey{GroupID: &groupID, Group: &Group{ID: groupID, Hydrated: true}}

	const (
		inputTokens  = 10000
		outputTokens = 1000
		cacheTokens  = 5000
		tokenHours   = 20000.0
	)
	baseInput := inputTokens * 0.00000075
	baseOutput := outputTokens * 0.00000375
	baseCacheRead := cacheTokens * 0.000000075
	storage := tokenHours * 0.000001
	opts := &recordUsageOpts{PricingAt: time.Now()}

	cost := gateway.calculateRecordUsageCost(context.Background(), &ForwardResult{
		Model:                   "gemini-3.7-flash",
		Usage:                   ClaudeUsage{InputTokens: inputTokens, OutputTokens: outputTokens, CacheReadInputTokens: cacheTokens},
		CacheStorageTokenHours:  tokenHours,
		UpstreamPriceMultiplier: 1.1,
	}, apiKey, nil, "gemini-3.7-flash", "gemini-3.7-flash", "", "", 2, 2, opts)
	require.InDelta(t, baseInput*1.1, cost.InputCost, 1e-12)
	require.InDelta(t, baseOutput*1.1, cost.OutputCost, 1e-12)
	require.InDelta(t, baseCacheRead*1.1, cost.CacheReadCost, 1e-12)
	tokenTotal := (baseInput + baseOutput + baseCacheRead) * 1.1
	require.InDelta(t, tokenTotal+storage, cost.TotalCost, 1e-12)
	require.InDelta(t, (tokenTotal+storage)*2, cost.ActualCost, 1e-12)

	unset := gateway.calculateRecordUsageCost(context.Background(), &ForwardResult{
		Model: "gemini-3.7-flash",
		Usage: ClaudeUsage{InputTokens: inputTokens, OutputTokens: outputTokens, CacheReadInputTokens: cacheTokens},
	}, apiKey, nil, "gemini-3.7-flash", "gemini-3.7-flash", "", "", 1, 1, opts)
	require.InDelta(t, baseInput+baseOutput+baseCacheRead, unset.TotalCost, 1e-12)
}

// 按次、按图计费不受上游端点倍数影响。
func TestApplyUpstreamPriceFactorTokenModeOnly(t *testing.T) {
	result := &ForwardResult{UpstreamPriceMultiplier: 1.1}
	for mode, want := range map[string]float64{"": 1.1, string(BillingModeToken): 1.1, string(BillingModePerRequest): 1, string(BillingModeImage): 1} {
		cost := &CostBreakdown{InputCost: 1, TotalCost: 1, ActualCost: 1, BillingMode: mode}
		applyUpstreamPriceFactor(cost, result)
		require.InDelta(t, want, cost.TotalCost, 1e-12, mode)
		require.InDelta(t, want, cost.ActualCost, 1e-12, mode)
	}
	applyUpstreamPriceFactor(nil, result)
}

// 账号统计成本只对模型定价文件的默认价叠加端点倍数。
func TestAccountStatsUpstreamPriceFactorScalesModelFileCost(t *testing.T) {
	base := 2.0
	require.Equal(t, &base, scaleAccountStatsModelFileCost(context.Background(), &base))
	scaled := scaleAccountStatsModelFileCost(withAccountStatsUpstreamPriceFactor(context.Background(), 1.1), &base)
	require.InDelta(t, 2.2, *scaled, 1e-12)
	require.InDelta(t, 2.0, base, 1e-12, "原值不能被原地修改")
	require.Nil(t, scaleAccountStatsModelFileCost(withAccountStatsUpstreamPriceFactor(context.Background(), 1.1), nil))
}
