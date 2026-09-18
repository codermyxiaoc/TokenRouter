package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 图片缓存仍服从 fork 的渠道显式价格、区间倍率与整体倍率，显式零价不得回退目录价。
func TestCodexDirectImagesCachePricingOverrides(t *testing.T) {
	base := &ModelPricing{CacheReadPricePerToken: 1.25e-6, ImageCacheReadPricePerToken: 2e-6}
	tokens := UsageTokens{CacheReadTokens: 50, ImageCacheReadTokens: 40}
	billing := &BillingService{}
	for _, price := range []float64{0, 3e-6} {
		cloned := *base
		applyChannelTokenPriceOverrides(&cloned, &ChannelModelPricing{CacheReadPrice: &price})
		cost := billing.computeTokenBreakdown(&cloned, tokens, 1, "", false)
		require.InDelta(t, 50*price, cost.CacheReadCost, 1e-12)
		interval := intervalToModelPricingWithBase(&PricingInterval{CacheReadPrice: &price}, false, nil, base)
		require.InDelta(t, 50*price, billing.computeTokenBreakdown(interval, tokens, 1, "", false).CacheReadCost, 1e-12)
	}
	multiplier := 3.0
	expected := (10*1.25e-6 + 40*2e-6) * multiplier
	scaled := multiplyModelPricing(base, multiplier)
	require.InDelta(t, expected, billing.computeTokenBreakdown(scaled, tokens, 1, "", false).CacheReadCost, 1e-12)
	interval := intervalToModelPricingWithBase(&PricingInterval{CacheReadMultiplier: &multiplier}, false, nil, base)
	require.InDelta(t, expected, billing.computeTokenBreakdown(interval, tokens, 1, "", false).CacheReadCost, 1e-12)
	require.Equal(t, 2e-6, base.ImageCacheReadPricePerToken)
}

// 每张图片计价继续由原有规则决定，新用量明细只记录事实，不额外按 token 再扣一笔。
func TestCodexDirectImagesCacheUsagePreservesPerImageSettlement(t *testing.T) {
	repo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	price := 0.2
	result := &OpenAIForwardResult{RequestID: "direct-cache-billing", Model: "gpt-image-2", ImageCount: 1, ImageOutputSizes: []string{"1024x1024"}, Duration: time.Second,
		Usage: OpenAIUsage{InputTokens: 100, ImageInputTokens: 80, CacheReadInputTokens: 50, ImageCacheReadTokens: 40, OutputTokens: 200, ImageOutputTokens: 200}}
	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result,
		APIKey: &APIKey{ID: 1001, GroupID: i64p(12), Group: &Group{ID: 12, RateMultiplier: 1, ImagePrice1K: &price}},
		User:   &User{ID: 2001}, Account: &Account{ID: 3001}})
	require.NoError(t, err)
	require.NotNil(t, repo.lastLog)
	require.InDelta(t, price, repo.lastLog.ActualCost, 1e-12)
	require.Equal(t, 40, repo.lastLog.ImageSizeBreakdown["image_cache_read_tokens"])
	require.Equal(t, 1, repo.lastLog.ImageSizeBreakdown[ImageBillingSize1K])
	require.NotContains(t, result.ImageSizeBreakdown, "image_cache_read_tokens")
	require.Equal(t, 80, repo.lastLog.ImageInputTokens)
	require.Equal(t, 50, repo.lastLog.CacheReadTokens)
}
