//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 实际 RecordUsage 链路验证渠道推理价格与套餐倍率组合，旧 Max、默认 3x 和零价均不改变。
func TestReasoningEffortSubscriptionRecordUsageCompatibility(t *testing.T) {
	legacy := 2.5
	for _, tc := range []struct {
		name, effort string
		legacy       *float64
		levels       map[string]float64
		free         bool
		factor       float64
	}{
		{name: "fable_default_max", effort: "max", factor: 3},
		{name: "legacy_max", effort: "max", legacy: &legacy, factor: 2.5},
		{name: "other_level_preserves_legacy", effort: "max", legacy: &legacy, levels: map[string]float64{"high": 1.5}, factor: 2.5},
		{name: "new_max_overrides_legacy", effort: "max", legacy: &legacy, levels: map[string]float64{"max": 1}, factor: 1},
		{name: "channel_high", effort: "high", levels: map[string]float64{"high": 1.5}, factor: 1.5},
		{name: "explicit_none", effort: "none", levels: map[string]float64{"none": .5}, factor: .5},
		{name: "missing_is_not_none", levels: map[string]float64{"none": .5}, factor: 1},
		{name: "explicit_zero_prices", effort: "max", levels: map[string]float64{"max": 100}, free: true, factor: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			billingRepo := &openAIRecordUsageBillingRepoStub{}
			userRate := .17
			rateRepo := &openAIUserGroupRateRepoStub{rate: &userRate}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, rateRepo)
			const model = "claude-fable-5-1"
			input, output := .01, .02
			if tc.free {
				input, output = 0, 0
			}
			cache := newEmptyChannelCache()
			cache.pricingByGroupModel[channelModelKey{groupID: 88, model: model}] = &ChannelModelPricing{
				BillingMode: BillingModeToken, InputPrice: &input, OutputPrice: &output,
				MaxReasoningEffortMultiplier: tc.legacy, ReasoningEffortMultipliers: tc.levels,
			}
			cache.channelByGroupID[88] = &Channel{ID: 1, Status: StatusActive}
			cache.groupPlatform[88], cache.loadedAt = "", time.Now()
			cs := &ChannelService{}
			cs.cache.Store(cache)
			svc.resolver = NewModelPricingResolver(cs, svc.billingService)
			sub := &UserSubscription{ID: 99, UserID: 200, PlanID: 199, Plan: &SubscriptionPlan{ID: 199, GroupIDs: []int64{88}, GroupRateMultipliers: map[int64]float64{88: .4}}}
			key := &APIKey{ID: 100, GroupID: i64p(88), Group: &Group{ID: 88, RateMultiplier: 2}}
			result := &OpenAIForwardResult{RequestID: "effort-subscription-" + tc.name, Model: model, ReasoningEffort: &tc.effort, Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 20}}
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: key, User: &User{ID: 200}, Account: &Account{ID: 300}, Subscription: sub})
			require.NoError(t, err)
			require.NotNil(t, usageRepo.lastLog)
			require.NotNil(t, billingRepo.lastCmd)
			base := 1.4 * tc.factor
			require.InDelta(t, base, usageRepo.lastLog.TotalCost, 1e-12)
			require.InDelta(t, base*.4, usageRepo.lastLog.ActualCost, 1e-12)
			require.InDelta(t, base*.4, billingRepo.lastCmd.BillableAmountUSD, 1e-12)
			require.InDelta(t, .4, usageRepo.lastLog.RateMultiplier, 1e-12)
			require.Zero(t, rateRepo.calls, "套餐额外倍率应覆盖用户和默认分组倍率")
		})
	}
}

// 搜索是 token 费用之外的附加项，推理倍率不可误乘搜索次数费用。
func TestReasoningEffortSearchSurchargeIsNotMultiplied(t *testing.T) {
	bs := newTestBillingService()
	svc := &OpenAIGatewayService{billingService: bs, resolver: NewModelPricingResolver(nil, bs)}
	key := &APIKey{GroupID: i64p(88), Group: &Group{ID: 88, SearchPricePer1k: testPtrFloat64(3), ModelPricing: []ChannelModelPricing{{
		Models: []string{"gpt-6-sol"}, BillingMode: BillingModeToken, InputPrice: testPtrFloat64(.01), OutputPrice: testPtrFloat64(.02),
		ReasoningEffortMultipliers: map[string]float64{"high": 5},
	}}}}
	effort := "high"
	result := &OpenAIForwardResult{Model: "gpt-6-sol", ReasoningEffort: &effort}
	tokens := UsageTokens{InputTokens: 100, OutputTokens: 20}
	base, err := svc.calculateOpenAIRecordUsageCost(context.Background(), result, key, []string{"gpt-6-sol"}, .4, .4, .4, .4, tokens, "")
	require.NoError(t, err)
	result.SearchCount = 2
	withSearch, err := svc.calculateOpenAIRecordUsageCost(context.Background(), result, key, []string{"gpt-6-sol"}, .4, .4, .4, .4, tokens, "")
	require.NoError(t, err)
	require.InDelta(t, 7, base.TotalCost, 1e-12)
	require.InDelta(t, .006, withSearch.TotalCost-base.TotalCost, 1e-12)
	require.InDelta(t, .006*.4, withSearch.ActualCost-base.ActualCost, 1e-12)
}
