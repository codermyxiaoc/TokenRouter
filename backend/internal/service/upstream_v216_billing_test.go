//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 调度账号不能改变用户价格；OAuth 长上下文优惠仅作用于上游成本。
func TestGPT61LongContextAccountCostDoesNotChangeUserBill(t *testing.T) {
	var userCosts []float64
	for _, typ := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		repo := &openAIRecordUsageLogRepoStub{inserted: true}
		svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
		svc.channelService = newTestChannelServiceForStats(t, &Channel{ID: 1, Status: StatusActive}, 10, PlatformOpenAI)
		err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
			Result: &OpenAIForwardResult{RequestID: "long-" + typ, Model: "gpt-6.1-sol", UpstreamModel: "gpt-6.1-sol", Usage: OpenAIUsage{InputTokens: 300000, OutputTokens: 100}, Duration: time.Second},
			APIKey: &APIKey{ID: 1, GroupID: i64p(10), Group: &Group{ID: 10, Platform: PlatformOpenAI, RateMultiplier: 1}},
			User:   &User{ID: 1}, Account: &Account{ID: 2, Platform: PlatformOpenAI, Type: typ},
		})
		require.NoError(t, err)
		require.NotNil(t, repo.lastLog)
		userCosts = append(userCosts, repo.lastLog.ActualCost)
		require.NotNil(t, repo.lastLog.AccountStatsCost)
		expected := 1.2015
		if typ == AccountTypeOAuth {
			expected = 0.601
		}
		require.InDelta(t, expected, *repo.lastLog.AccountStatsCost, 1e-12)
	}
	require.InDelta(t, userCosts[0], userCosts[1], 1e-12)
	require.InDelta(t, 1.2015, userCosts[0], 1e-12)
}

// 手动统计价（包括零价）和渠道应用开关优先于上游套餐优惠。
func TestGPT61LongContextExemptionPreservesExplicitStatsPricing(t *testing.T) {
	ctx := context.WithValue(context.Background(), accountStatsLongContextContextKey{}, true)
	bs := NewBillingService(&config.Config{}, nil)
	channel := &Channel{ID: 1, Status: StatusActive, ApplyPricingToAccountStats: true}
	cs := newTestChannelServiceForStats(t, channel, 10, PlatformOpenAI)
	cost := resolveAccountStatsCost(ctx, cs, bs, 2, 10, "gpt-6.1-sol", "", UsageTokens{InputTokens: 300000}, 1, 7, "")
	require.NotNil(t, cost)
	require.Equal(t, 7.0, *cost)
	channel.AccountStatsPricingRules = []AccountStatsPricingRule{{GroupIDs: []int64{10}, Pricing: []ChannelModelPricing{{Models: []string{"gpt-6.1-sol"}, InputPrice: testPtrFloat64(0), OutputPrice: testPtrFloat64(0)}}}}
	cs = newTestChannelServiceForStats(t, channel, 10, PlatformOpenAI)
	cost = resolveAccountStatsCost(ctx, cs, bs, 2, 10, "gpt-6.1-sol", "", UsageTokens{InputTokens: 300000}, 1, 7, "")
	require.NotNil(t, cost)
	require.Zero(t, *cost)
}

// 新 Haiku 阶梯以输入和缓存上下文总量判断，显式零价不得回退官方价格。
func TestHaiku55PricingBoundary(t *testing.T) {
	bs := NewBillingService(&config.Config{}, nil)
	for _, n := range []int{100000, 100001} {
		cost, err := bs.CalculateCost("claude-haiku-5-5", UsageTokens{InputTokens: n, OutputTokens: 100}, 1)
		require.NoError(t, err)
		factor := 1.0
		if n > 100000 {
			factor = 5
		}
		require.InDelta(t, float64(n)*1e-7*factor+100*5e-7*factor, cost.ActualCost, 1e-12)
	}
	price, err := bs.GetModelPricingWithChannel("claude-haiku-5-5", &ChannelModelPricing{BillingMode: BillingModeToken, InputPrice: testPtrFloat64(0), OutputPrice: testPtrFloat64(0)})
	require.NoError(t, err)
	require.Zero(t, price.InputPricePerToken)
	require.Zero(t, price.OutputPricePerToken)
}
