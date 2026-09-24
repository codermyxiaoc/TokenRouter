//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 复现周卡尾段：下次刷新早于到期，但完整下一周超出到期，仍应只发放一份额度。
func TestSubscriptionUpstreamPostgres_WeeklyTailConcurrentBilling(t *testing.T) {
	ctx := context.Background()
	f := newResetCountFixture(t, false)
	now := time.Now().Truncate(time.Microsecond)
	anchor := now.Add(-7*24*time.Hour - time.Hour)
	expires := now.Add(5 * 24 * time.Hour)
	_, err := integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET starts_at=$1, expires_at=$2,
		daily_limit_usd=NULL, monthly_limit_usd=NULL, weekly_limit_usd=70,
		weekly_window_start=$3, weekly_usage_usd=70, weekly_reset_count=3, reset_counted_at=$3 WHERE id=$4`,
		now.Add(-21*24*time.Hour), expires, anchor, f.sub.ID)
	require.NoError(t, err)
	commands := make([]service.UsageBillingCommand, 60)
	for i := range commands {
		commands[i] = *resetCountCommand(f, uuid.NewString(), 2)
	}
	var subscriptionTotal, balanceTotal float64
	for _, outcome := range billingFocusConcurrentApply(t, commands) {
		require.NoError(t, outcome.err)
		require.True(t, outcome.result.Applied)
		subscriptionTotal += outcome.result.SubscriptionAmountUSD
		balanceTotal += outcome.result.BalanceAmountUSD
	}
	require.Equal(t, 70.0, subscriptionTotal)
	require.Equal(t, 50.0, balanceTotal)
	got, err := f.repo.GetByID(ctx, f.sub.ID)
	require.NoError(t, err)
	require.Equal(t, int64(4), got.WeeklyResetCount)
	require.True(t, got.WeeklyWindowStart.Equal(anchor.Add(7*24*time.Hour)))
	require.True(t, got.ExpiresAt.Equal(expires))
	billingFocusAssertDecimal(t, "70", `SELECT weekly_usage_usd::text FROM user_subscriptions WHERE id=$1`, f.sub.ID)
	billingFocusAssertDecimal(t, "50", `SELECT balance::text FROM users WHERE id=$1`, f.sub.UserID)
	// 重试同一笔账单不能第二次扣款或再次推进重置次数。
	replay, err := f.billing.Apply(ctx, &commands[0])
	require.NoError(t, err)
	require.False(t, replay.Applied)
	again, err := f.repo.GetByID(ctx, f.sub.ID)
	require.NoError(t, err)
	require.Equal(t, got.WeeklyResetCount, again.WeeklyResetCount)
	require.Equal(t, got.WeeklyUsageUSD, again.WeeklyUsageUSD)
}

// 尾段刷新后的图片预占与取消沿用同一窗口，取消只退预占，不回退重置次数或锚点。
func TestSubscriptionUpstreamPostgres_WeeklyTailImageReserveAndRelease(t *testing.T) {
	ctx := context.Background()
	f := newResetCountFixture(t, false)
	now := time.Now().UTC().Truncate(time.Microsecond)
	anchor := now.Add(-7*24*time.Hour - time.Hour)
	_, err := integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET starts_at=$1,expires_at=$2,
		daily_limit_usd=NULL,monthly_limit_usd=NULL,weekly_limit_usd=70,weekly_window_start=$3,
		weekly_usage_usd=70,weekly_reset_count=3,reset_counted_at=$3 WHERE id=$4`,
		now.Add(-21*24*time.Hour), now.Add(5*24*time.Hour), anchor, f.sub.ID)
	require.NoError(t, err)
	batchID := "imgbatch_" + uuid.NewString()
	insertBatchImageAllowanceTestJob(t, batchID, f.sub.UserID, f.sub.UserID, f.key.ID, nil, now)
	command := &service.BatchImageBalanceHoldCommand{RequestID: service.BatchImageHoldRequestID(batchID),
		APIKeyID: f.key.ID, UserID: f.sub.UserID, ActorUserID: f.sub.UserID, BatchID: batchID, HoldAmount: 80, ReservedAt: now}
	reserved, err := f.billing.ReserveBatchImageBalance(ctx, command)
	require.NoError(t, err)
	require.Equal(t, 70.0, reserved.SubscriptionAmountUSD)
	require.Equal(t, 10.0, reserved.BalanceAmountUSD)
	var allocations []domain.BillingAllocation
	for _, allocation := range reserved.BillingAllocations {
		if allocation.Type == domain.BillingAllocationTypeSubscription {
			allocations = append(allocations, allocation)
		}
	}
	release := *command
	release.RequestID = service.BatchImageReleaseRequestID(batchID)
	release.RequestFingerprint = ""
	release.BalanceHoldAmount = reserved.BalanceAmountUSD
	release.SubscriptionHoldAllocations = allocations
	release.AllowanceReserved = true
	result, err := f.billing.ReleaseBatchImageBalance(ctx, &release)
	require.NoError(t, err)
	require.True(t, result.Applied)
	got, err := f.repo.GetByID(ctx, f.sub.ID)
	require.NoError(t, err)
	require.Equal(t, int64(4), got.WeeklyResetCount)
	require.True(t, got.WeeklyWindowStart.Equal(anchor.Add(7*24*time.Hour)))
	billingFocusAssertDecimal(t, "0", `SELECT weekly_usage_usd::text FROM user_subscriptions WHERE id=$1`, f.sub.ID)
	billingFocusAssertDecimal(t, "100", `SELECT balance::text FROM users WHERE id=$1`, f.sub.UserID)
	billingFocusAssertDecimal(t, "0", `SELECT frozen_balance::text FROM users WHERE id=$1`, f.sub.UserID)
	result, err = f.billing.ReleaseBatchImageBalance(ctx, &release)
	require.NoError(t, err)
	require.False(t, result.Applied)
}

// 仓储接收实际操作时刻；只有日窗口取午夜，周/月保留时分秒，历史用量首次激活不丢失。
func TestSubscriptionUpstreamPostgres_ActivationAndManualResetAnchors(t *testing.T) {
	ctx := context.Background()
	f := newResetCountFixture(t, false)
	start := time.Now().Truncate(time.Microsecond)
	_, err := integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET daily_usage_usd=5,weekly_usage_usd=6,monthly_usage_usd=7 WHERE id=$1`, f.sub.ID)
	require.NoError(t, err)
	require.NoError(t, f.repo.ActivateWindows(ctx, f.sub.ID, start, service.SubscriptionWindowActivation{Daily: true, Weekly: true, Monthly: true}))
	got, err := f.repo.GetByID(ctx, f.sub.ID)
	require.NoError(t, err)
	require.True(t, got.DailyWindowStart.Equal(timezone.StartOfDay(start)))
	require.True(t, got.WeeklyWindowStart.Equal(start))
	require.True(t, got.MonthlyWindowStart.Equal(start))
	require.Equal(t, []float64{5, 6, 7}, []float64{got.DailyUsageUSD, got.WeeklyUsageUSD, got.MonthlyUsageUSD})
	manual := start.Add(15 * time.Minute)
	require.NoError(t, f.repo.ResetUsageWindows(ctx, f.sub.ID, true, true, true, manual))
	got, err = f.repo.GetByID(ctx, f.sub.ID)
	require.NoError(t, err)
	require.True(t, got.DailyWindowStart.Equal(timezone.StartOfDay(manual)))
	require.True(t, got.WeeklyWindowStart.Equal(manual))
	require.True(t, got.MonthlyWindowStart.Equal(manual))
	require.Equal(t, []float64{0, 0, 0}, []float64{got.DailyUsageUSD, got.WeeklyUsageUSD, got.MonthlyUsageUSD})
	require.Equal(t, []int64{0, 0, 0}, resetCountValues(got))
}
