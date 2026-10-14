//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

// 验证真正的报价构造路径，避免只比较两次重放命令却漏掉数据库重新套用套餐倍率。
func TestLegacyVideoQuoteFreezesSelectedSubscriptionRates(t *testing.T) {
	for _, tc := range []struct {
		name            string
		token           bool
		mode            string
		scoped          bool
		planRate        float64
		peak            float64
		independent     bool
		videoRate       float64
		wantSub         float64
		wantBalance     float64
		wantScale       float64
		wantDisablePlan bool
	}{
		{"token_plan_and_peak_once", true, APIKeyBillingModeAuto, true, 2, 3, false, 0, 6, 21, 3, true},
		{"token_free_peak", true, APIKeyBillingModeAuto, true, 2, 0, false, 0, 0, 0, 0, true},
		{"token_missing_plan_override_uses_group", true, APIKeyBillingModeAuto, true, 0, 3, false, 0, 21, 21, 3, true},
		{"video_plan_without_peak", false, APIKeyBillingModeAuto, true, 2, 3, false, 0, 2, 7, 1, true},
		{"video_independent_overrides_plan", false, APIKeyBillingModeAuto, true, 2, 3, true, 0.4, 0.4, 0.4, 1, true},
		{"video_independent_free", false, APIKeyBillingModeAuto, true, 2, 3, true, 0, 0, 0, 1, true},
		{"explicit_subscription", false, APIKeyBillingModeSubscription, true, 2, 3, false, 0, 2, 7, 1, true},
		{"unscoped_auto_keeps_per_plan_allocation", true, APIKeyBillingModeAuto, false, 2, 3, false, 0, 21, 21, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := &openAIRecordUsageLogRepoStub{inserted: true}
			s := newOpenAIRecordUsageServiceForTest(logs, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			group := &Group{ID: 3, Platform: PlatformGrok, RateMultiplier: 7,
				PeakRateEnabled: true, PeakStart: "09:00", PeakEnd: "18:00", PeakRateMultiplier: tc.peak,
				VideoRateIndependent: tc.independent, VideoRateMultiplier: tc.videoRate}
			sub := &UserSubscription{ID: 55, UserID: 1, Plan: &SubscriptionPlan{ID: 77,
				GroupIDs: []int64{group.ID}, GroupRateMultipliers: map[int64]float64{group.ID: tc.planRate}}}
			key := &APIKey{ID: 2, UserID: 1, GroupID: &group.ID, Group: group, BillingMode: tc.mode}
			if tc.mode == APIKeyBillingModeSubscription {
				key.PreferredSubscriptionID = &sub.ID
			}
			result := &OpenAIForwardResult{RequestID: "grok-video:frozen-plan", Model: "grok-imagine-video",
				VideoCount: 1, VideoResolution: "480p", VideoDurationSeconds: 8}
			if tc.token {
				price := 10.0
				group.Platform = PlatformOpenAI
				group.ModelPricing = []ChannelModelPricing{{Platform: PlatformOpenAI, Models: []string{"seedance-frozen"}, BillingMode: BillingModeToken, OutputPrice: &price}}
				s.resolver = NewModelPricingResolver(nil, s.billingService)
				result = &OpenAIForwardResult{RequestID: "grok-video:seedance:frozen-plan", Model: "seedance-frozen", Usage: OpenAIUsage{OutputTokens: 1000}}
			}
			input := &OpenAIRecordUsageInput{PrepareLegacyVideoQuote: true, LegacyVideoTaskID: 5,
				Result: result, APIKey: key, User: &User{ID: 1}, Account: &Account{ID: 9, Platform: group.Platform},
				Subscription: sub, PricingAt: time.Date(2026, 10, 14, 12, 0, 0, 0, timezone.Location())}
			if tc.scoped {
				input.SubscriptionScopeID = &sub.ID
			}
			require.NoError(t, s.RecordUsage(context.Background(), input))
			require.NotNil(t, input.LegacyVideoQuote)
			// 和生产一样经过 JSON 落盘再恢复；内部订阅范围由独立字段保存。
			raw, err := json.Marshal(input.LegacyVideoQuote)
			require.NoError(t, err)
			var quote LegacyVideoBillingQuote
			require.NoError(t, json.Unmarshal(raw, &quote))
			require.NotNil(t, quote.Command)
			require.InDelta(t, tc.wantSub, quote.Command.SubscriptionRateMultiplier, 1e-10)
			require.InDelta(t, tc.wantBalance, quote.Command.BalanceRateMultiplier, 1e-10)
			require.InDelta(t, tc.wantScale, quote.Command.SubscriptionRateMultiplierScale, 1e-10)
			require.Equal(t, tc.wantDisablePlan, quote.Command.DisablePlanGroupRateMultiplier)
			require.Equal(t, quote.Command.SubscriptionRateMultiplier, quote.Rates.SubscriptionRateMultiplier)
			require.Equal(t, quote.Command.DisablePlanGroupRateMultiplier, quote.Rates.DisablePlanGroupRateMultiplier)
			require.Zero(t, logs.calls)
			require.Zero(t, s.usageBillingRepo.(*openAIRecordUsageBillingRepoStub).calls)
		})
	}
}
