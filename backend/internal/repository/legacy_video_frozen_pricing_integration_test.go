//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 在报价落库之后修改真实套餐倍率，原订阅及余额分摊都必须使用冻结报价。
func TestLegacyVideoRepositoryFrozenPlanRateSurvivesAdminChange(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mode        string
		limit       float64
		frozenRate  float64
		scale       float64
		wantSub     float64
		wantBalance float64
	}{
		{"auto_full_subscription", service.APIKeyBillingModeAuto, 10, 2, 1, 2, 0},
		{"auto_partial_subscription_and_balance", service.APIKeyBillingModeAuto, 1, 2, 1, 1, 0.25},
		{"explicit_full_subscription", service.APIKeyBillingModeSubscription, 10, 2, 1, 2, 0},
		{"explicit_partial_subscription_and_balance", service.APIKeyBillingModeSubscription, 1, 2, 1, 1, 0.25},
		{"token_peak_already_included", service.APIKeyBillingModeAuto, 10, 6, 3, 6, 0},
		{"free_rate_stays_free", service.APIKeyBillingModeAuto, 10, 0, 0, 0, 0},
		{"independent_media_rate", service.APIKeyBillingModeAuto, 10, 0.4, 1, 0.4, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLegacyVideoFixture(t)
			ctx := context.Background()
			task := f.task()
			require.NoError(t, f.repo.Save(ctx, task))
			plan := mustCreatePlan(t, integrationEntClient, &service.SubscriptionPlan{
				Name: "legacy-frozen-" + uuid.NewString(), Price: 10, ValidityDays: 30, ValidityUnit: "day",
				GroupIDs: []int64{f.group}, GroupRateMultipliers: map[int64]float64{f.group: 2},
				DailyLimitUSD: &tc.limit, WeeklyLimitUSD: &tc.limit, MonthlyLimitUSD: &tc.limit})
			sub := mustCreateSubscription(t, integrationEntClient, &service.UserSubscription{
				UserID: f.user, PlanID: plan.ID, DailyLimitUSD: &tc.limit, WeeklyLimitUSD: &tc.limit, MonthlyLimitUSD: &tc.limit})
			cmd := &service.UsageBillingCommand{LegacyVideoTaskID: task.ID, RequestID: task.RequestID,
				APIKeyID: f.key, UserID: f.user, ActorUserID: f.user, AccountID: f.account, GroupID: &f.group,
				APIKeyBillingMode: tc.mode, SubscriptionScopeID: &sub.ID,
				BaseAmountUSD: 1, BillableAmountUSD: tc.frozenRate,
				SubscriptionRateMultiplier: tc.frozenRate, SubscriptionRateMultiplierScale: tc.scale,
				BalanceRateMultiplier: 0.5, DisablePlanGroupRateMultiplier: true}
			if tc.mode == service.APIKeyBillingModeSubscription {
				cmd.PreferredSubscriptionID = &sub.ID
			}
			// 报价通过真实数据库 JSON 往返；范围字段使用与恢复器相同的独立字段。
			evidence := struct {
				Quote *service.LegacyVideoBillingQuote
			}{&service.LegacyVideoBillingQuote{Command: cmd, SubscriptionScopeID: &sub.ID}}
			raw, err := json.Marshal(evidence)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE legacy_video_tasks SET settlement_result=$2::jsonb WHERE id=$1`, task.ID, string(raw))
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE subscription_plan_groups SET rate_multiplier=9 WHERE plan_id=$1 AND group_id=$2`, plan.ID, f.group)
			require.NoError(t, err)
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT settlement_result FROM legacy_video_tasks WHERE id=$1`, task.ID).Scan(&raw))
			require.NoError(t, json.Unmarshal(raw, &evidence))
			cmd = evidence.Quote.Command
			cmd.SubscriptionScopeID = evidence.Quote.SubscriptionScopeID
			billing := NewUsageBillingRepository(integrationEntClient, integrationDB)
			first, err := billing.Apply(ctx, cmd)
			require.NoError(t, err)
			require.True(t, first.Applied)
			require.InDelta(t, tc.wantSub, first.SubscriptionAmountUSD, 1e-8)
			require.InDelta(t, tc.wantBalance, first.BalanceAmountUSD, 1e-8)
			// 再次改价后重放原回执，既不能重算倍率也不能重复扣除订阅或余额。
			_, err = integrationDB.ExecContext(ctx, `UPDATE subscription_plan_groups SET rate_multiplier=NULL WHERE plan_id=$1 AND group_id=$2`, plan.ID, f.group)
			require.NoError(t, err)
			second, err := billing.Apply(ctx, cmd)
			require.NoError(t, err)
			require.False(t, second.Applied)
			require.InDelta(t, first.SubscriptionAmountUSD, second.SubscriptionAmountUSD, 1e-8)
			require.InDelta(t, first.BalanceAmountUSD, second.BalanceAmountUSD, 1e-8)
			var balance, subUsage float64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, f.user).Scan(&balance))
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&subUsage))
			require.InDelta(t, 100-tc.wantBalance, balance, 1e-8)
			require.InDelta(t, tc.wantSub, subUsage, 1e-8)
		})
	}
}
