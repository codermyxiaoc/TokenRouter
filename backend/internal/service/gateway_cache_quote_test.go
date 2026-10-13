//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 存储能力可以来自实际模型，但不能把未配置的公开别名输入费用误记为免费。
func TestGeminiCacheQuoteRejectsMissingInputPriceAndPreservesExplicitZero(t *testing.T) {
	const alias = "unpriced-public-alias-v216"
	bs := NewBillingService(&config.Config{}, nil)
	svc := &GatewayService{billingService: bs}
	user := &User{ID: 1}
	key := &APIKey{ID: 2, User: user, GroupID: i64p(3), Group: &Group{ID: 3, Platform: PlatformGemini, Hydrated: true, RateMultiplier: 1}}
	input := &RecordUsageInput{APIKey: key, User: user, Account: &Account{ID: 4, Platform: PlatformGemini}, Result: &ForwardResult{Model: alias, UpstreamModel: "gemini-3.8-flash", Usage: ClaudeUsage{InputTokens: 1000}, CacheStorageTokenHours: 1000}, ChannelUsageFields: ChannelUsageFields{BillingModelSource: BillingModelSourceRequested, OriginalModel: alias}}
	require.True(t, svc.CacheStoragePriced(context.Background(), input))
	require.Nil(t, svc.QuoteUsageCostBreakdown(context.Background(), input))
	// 普通请求保持原来的缺价处理，避免此次缓存功能改变既有业务入口。
	ordinary := svc.calculateTokenCost(context.Background(), input.Result, key, input.Account, alias, alias, BillingModelSourceRequested, "", 1, nil)
	require.NotNil(t, ordinary)
	require.Zero(t, ordinary.ActualCost)
	// 明确配置的零价有有效 ModelPricing，不能与解析失败混为一谈。
	bs.pricingService = &PricingService{pricingData: map[string]*LiteLLMModelPricing{alias: {InputCostPerToken: 0, OutputCostPerToken: 0}}}
	quote := svc.QuoteUsageCostBreakdown(context.Background(), input)
	require.NotNil(t, quote)
	require.Zero(t, quote.InputCost)
	require.Greater(t, quote.TotalCost, 0.0)
	// 仅延长已有缓存只需要存储价格，不重新收取或要求输入价格。
	bs.pricingService = nil
	input.Result.Usage = ClaudeUsage{}
	storageOnly := svc.QuoteUsageCostBreakdown(context.Background(), input)
	require.NotNil(t, storageOnly)
	require.InDelta(t, quote.TotalCost, storageOnly.TotalCost, 1e-12)
}
