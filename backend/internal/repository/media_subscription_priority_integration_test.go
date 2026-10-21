//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 零报价仍可能在事务内改选收费套餐；正数计数信号必须使用最终实扣，不能遗漏或凭空计费。
func TestUsageBillingApplyZeroMediaQuoteUsesFinalSubscriptionCost(t *testing.T) {
	for _, tc := range []struct {
		name     string
		planRate any
		want     float64
	}{
		{name: "final_plan_is_paid", planRate: 2.4, want: 2.4},
		{name: "final_plan_is_free", planRate: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo, source, subID := newImageBillingIntegrationFixture(t, 10, 100)
			_, err := integrationDB.ExecContext(ctx, `UPDATE subscription_plan_groups SET rate_multiplier=$2 WHERE group_id=$1`, *source.Hold.GroupID, tc.planRate)
			require.NoError(t, err)
			cmd := &service.UsageBillingCommand{
				RequestID: source.Hold.RequestID, UserID: source.Hold.UserID, ActorUserID: source.Hold.ActorUserID,
				APIKeyID: source.Hold.APIKeyID, GroupID: source.Hold.GroupID, APIKeyBillingMode: service.APIKeyBillingModeAuto,
				BaseAmountUSD: 1, BillableAmountUSD: 0, SubscriptionRateMultiplier: 0, BalanceRateMultiplier: 0,
				SubscriptionRateMultiplierScale: 1, APIKeyQuotaCost: 1, APIKeyRateLimitCost: 1,
			}
			result, err := repo.Apply(ctx, cmd)
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.InDelta(t, tc.want, result.SubscriptionAmountUSD, 1e-10)
			require.Zero(t, result.BalanceAmountUSD)
			replay, err := repo.Apply(ctx, cmd)
			require.NoError(t, err)
			require.False(t, replay.Applied)
			var quota, window5h, window1d, window7d, used, balance float64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used,usage_5h,usage_1d,usage_7d FROM api_keys WHERE id=$1`, cmd.APIKeyID).Scan(&quota, &window5h, &window1d, &window7d))
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_subscriptions WHERE id=$1`, subID).Scan(&used))
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, cmd.UserID).Scan(&balance))
			for _, amount := range []float64{quota, window5h, window1d, window7d, used} {
				require.InDelta(t, tc.want, amount, 1e-10)
			}
			require.Equal(t, 10.0, balance)
		})
	}
}

// 实际资金分配只切换套餐覆盖开关；冻结后的订阅价格和余额独立倍率不能被后来编辑覆盖。
func TestImageBillingReservationMediaSubscriptionPriority(t *testing.T) {
	for _, tc := range []struct {
		name        string
		priority    bool
		independent float64
		planRate    any
		balanceOnly bool
		wantSub     float64
		wantHold    float64
		wantBalance float64
	}{
		{name: "old_snapshot_independent", independent: 2, planRate: .5, wantSub: 1, wantHold: 5, wantBalance: 4},
		{name: "priority_mixed_sources", priority: true, independent: 2, planRate: .5, wantSub: 1, wantHold: 2, wantBalance: 1},
		{name: "missing_plan_falls_back", priority: true, independent: 2, planRate: nil, wantSub: 1, wantHold: 5, wantBalance: 4},
		{name: "zero_plan_falls_back", priority: true, independent: 2, planRate: 0.0, wantSub: 1, wantHold: 5, wantBalance: 4},
		{name: "zero_independent_paid_plan", priority: true, independent: 0, planRate: .5, wantSub: 1},
		{name: "zero_independent_missing_plan", priority: true, independent: 0, planRate: nil},
		{name: "balance_ignores_priority", priority: true, independent: 2, planRate: .5, balanceOnly: true, wantHold: 6, wantBalance: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo, cmd, subID := newImageBillingIntegrationFixture(t, 10, 1)
			cmd.Hold.BaseAmountUSD = 3
			cmd.Hold.SubscriptionRateMultiplier = tc.independent
			cmd.Hold.BalanceRateMultiplier = tc.independent
			cmd.Hold.DisablePlanGroupRateMultiplier = !tc.priority
			if tc.balanceOnly {
				cmd.Hold.APIKeyBillingMode = service.APIKeyBillingModeBalance
			}
			_, err := integrationDB.ExecContext(ctx, `UPDATE subscription_plan_groups SET rate_multiplier=$2 WHERE group_id=$1`, *cmd.Hold.GroupID, tc.planRate)
			require.NoError(t, err)
			hold, err := repo.ReserveImageBilling(ctx, cmd)
			require.NoError(t, err)
			require.True(t, hold.Applied)
			require.Equal(t, !tc.priority, hold.Hold.DisablePlanGroupRateMultiplier)
			require.InDelta(t, tc.wantHold, hold.Hold.BalanceHoldAmount, 1e-10)
			require.InDelta(t, tc.wantSub+tc.wantHold, hold.Hold.HoldAmount, 1e-10)
			// 捕获不能重读套餐倍率；实际图片用量减少时只退回未消费的冻结额。
			_, err = integrationDB.ExecContext(ctx, `UPDATE subscription_plan_groups SET rate_multiplier=99 WHERE group_id=$1`, *cmd.Hold.GroupID)
			require.NoError(t, err)
			capture := imageBillingCaptureCommand(hold)
			result, err := repo.CaptureImageBilling(ctx, hold.ID, capture, 2.5)
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.InDelta(t, tc.wantSub, result.SubscriptionAmountUSD, 1e-10)
			require.InDelta(t, tc.wantBalance, result.BalanceAmountUSD, 1e-10)
			var balance, frozen, subUsed, keyUsed float64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance,frozen_balance FROM users WHERE id=$1`, cmd.Hold.UserID).Scan(&balance, &frozen))
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_subscriptions WHERE id=$1`, subID).Scan(&subUsed))
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used FROM api_keys WHERE id=$1`, cmd.Hold.APIKeyID).Scan(&keyUsed))
			require.InDelta(t, 10-tc.wantBalance, balance, 1e-10)
			require.Zero(t, frozen)
			require.InDelta(t, tc.wantSub, subUsed, 1e-10)
			require.InDelta(t, tc.wantSub+tc.wantBalance, keyUsed, 1e-10)
			replay, err := repo.CaptureImageBilling(ctx, hold.ID, capture, 2.5)
			require.NoError(t, err)
			require.False(t, replay.Applied)
		})
	}
}

// Token 后结算在终态读取套餐覆盖，组独立倍率及开关来自原任务；固定图片附加费仍为一倍。
func TestVideoDeferredBillingMediaSubscriptionPriority(t *testing.T) {
	for _, tc := range []struct {
		name        string
		priority    bool
		independent float64
		planRate    any
		wantSub     float64
		wantBalance float64
	}{
		{name: "old_snapshot_independent", independent: 2, planRate: .5, wantSub: 1, wantBalance: 5},
		{name: "priority_mixed_sources", priority: true, independent: 2, planRate: .5, wantSub: 1, wantBalance: 2},
		{name: "missing_plan_falls_back", priority: true, independent: 2, planRate: nil, wantSub: 1, wantBalance: 5},
		{name: "zero_plan_falls_back", priority: true, independent: 2, planRate: 0.0, wantSub: 1, wantBalance: 5},
		{name: "zero_independent_paid_plan", priority: true, independent: 0, planRate: .5, wantSub: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newVideoBillingFixture(t)
			sub := videoResetTestSubscription(t, f)
			_, err := integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET daily_limit_usd=1 WHERE id=$1`, sub.ID)
			require.NoError(t, err)
			task := f.reserve(newDeferredVideoTestTask(t, f, func(r *service.VideoTaskRecord) {
				r.Hold.SubscriptionRateMultiplier = tc.independent
				r.Hold.BalanceRateMultiplier = tc.independent
				r.Hold.DisablePlanGroupRateMultiplier = !tc.priority
				r.Hold.VideoFixedAmountUSD = .25
				count, fixed := 1, .25
				r.Quote.ReferenceImageCount = &count
				r.Quote.ImageInputPricing = &service.VideoImageInputPricing{Price: &fixed}
			}))
			// 延迟 Token 任务不提前占用套餐，完成时读取该套餐对最终组的新配置。
			_, err = integrationDB.ExecContext(ctx, `UPDATE subscription_plan_groups SET rate_multiplier=$3 WHERE plan_id=$1 AND group_id=$2`, sub.PlanID, f.groupID, tc.planRate)
			require.NoError(t, err)
			cmd := f.command(task, "completed", 3)
			cmd.VideoActualFixedAmountUSD = .25
			result, err := f.repo.CaptureBatchImageBalance(ctx, &cmd)
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.InDelta(t, tc.wantSub, result.SubscriptionAmountUSD, 1e-10)
			require.InDelta(t, tc.wantBalance+.25, result.BalanceAmountUSD, 1e-10)
			f.money(1000-tc.wantBalance-.25, 0, tc.wantSub+tc.wantBalance+.25, 3)
			replay, err := f.repo.CaptureBatchImageBalance(ctx, &cmd)
			require.NoError(t, err)
			require.False(t, replay.Applied)
		})
	}
}
