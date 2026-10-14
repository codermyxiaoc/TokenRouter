//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

// resetPrioritySubscription 使用周窗口构造刷新先后与到期先后相反的套餐，避免测试依赖零点。
func resetPrioritySubscription(t *testing.T, userID, groupID int64, resetAfter, expiresAfter time.Duration, limit, used, rate float64) *service.UserSubscription {
	t.Helper()
	client := testEntClient(t)
	plan := mustCreatePlan(t, client, &service.SubscriptionPlan{
		Name: "reset-priority-" + uuid.NewString(), Price: 1, ValidityDays: 30, ValidityUnit: "day",
		GroupIDs: []int64{groupID}, GroupRateMultipliers: map[int64]float64{groupID: rate}, WeeklyLimitUSD: float64Ptr(limit),
	})
	now := time.Now().UTC().Truncate(time.Microsecond)
	window := now.Add(-7*24*time.Hour + resetAfter)
	return mustCreateSubscription(t, client, &service.UserSubscription{
		UserID: userID, PlanID: plan.ID, StartsAt: now.Add(-8 * 24 * time.Hour), ExpiresAt: now.Add(expiresAfter),
		WeeklyWindowStart: &window, WeeklyLimitUSD: float64Ptr(limit), WeeklyUsageUSD: used,
	})
}

func resetPriorityUsage(t *testing.T, subscriptionID int64) float64 {
	t.Helper()
	var used float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT weekly_usage_usd FROM user_subscriptions WHERE id=$1`, subscriptionID).Scan(&used))
	return used
}

// 只读倍率解析必须使用最近刷新且有额度的套餐，仍排除耗尽及不覆盖分组的候选。
func TestUsageBillingResetPriorityResolver(t *testing.T) {
	for _, scenario := range []string{"near-reset", "near-exhausted", "near-other-group", "near-stale-window"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			repo, reserve, _ := newImageBillingIntegrationFixture(t, 10, 0)
			userID, groupID := reserve.Hold.UserID, *reserve.Hold.GroupID
			far := resetPrioritySubscription(t, userID, groupID, 12*time.Hour, 2*24*time.Hour, 10, 0, 2)
			near := resetPrioritySubscription(t, userID, groupID, time.Hour, 10*24*time.Hour, 10, 0, .5)
			want := near.ID
			switch scenario {
			case "near-exhausted":
				_, err := integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET weekly_usage_usd=weekly_limit_usd WHERE id=$1`, near.ID)
				require.NoError(t, err)
				want = far.ID
			case "near-other-group":
				other := mustCreateGroup(t, testEntClient(t), &service.Group{Name: "reset-other-" + uuid.NewString(), Platform: service.PlatformOpenAI})
				_, err := integrationDB.ExecContext(ctx, `UPDATE subscription_plan_groups SET group_id=$2 WHERE plan_id=$1`, near.PlanID, other.ID)
				require.NoError(t, err)
				want = far.ID
			case "near-stale-window":
				// 两周期前的窗口已经耗尽，归一化后应恢复额度且保持原有刷新节奏。
				_, err := integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET starts_at=starts_at-INTERVAL '7 days', weekly_window_start=weekly_window_start-INTERVAL '7 days', weekly_usage_usd=weekly_limit_usd WHERE id=$1`, near.ID)
				require.NoError(t, err)
			}
			selected, err := repo.ResolveUsableSubscriptionForGroup(ctx, userID, groupID)
			require.NoError(t, err)
			require.Equal(t, want, selected.ID)
			if scenario == "near-stale-window" {
				require.Zero(t, selected.WeeklyUsageUSD)
				require.Equal(t, 10.0, resetPriorityUsage(t, near.ID), "只读选择不能写回清零后的用量")
			}
		})
	}
}

// 普通自动结算先消耗最近刷新套餐，再按每个套餐自己的倍率及余额倍率分摊。
func TestUsageBillingResetPriorityMixedRates(t *testing.T) {
	ctx := context.Background()
	repo, reserve, _ := newImageBillingIntegrationFixture(t, 10, 0)
	userID, groupID := reserve.Hold.UserID, *reserve.Hold.GroupID
	far := resetPrioritySubscription(t, userID, groupID, 12*time.Hour, 2*24*time.Hour, 2, 0, 2)
	near := resetPrioritySubscription(t, userID, groupID, time.Hour, 10*24*time.Hour, 1, 0, .5)
	cmd := &service.UsageBillingCommand{
		RequestID: "reset-mixed-" + uuid.NewString(), APIKeyID: reserve.Hold.APIKeyID, UserID: userID, GroupID: &groupID,
		APIKeyBillingMode: service.APIKeyBillingModeAuto, BaseAmountUSD: 5, SubscriptionRateMultiplier: 1,
		SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: .25, IncludeAllocationPricing: true,
	}
	result, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.InDelta(t, 3, result.SubscriptionAmountUSD, 1e-8)
	require.InDelta(t, .5, result.BalanceAmountUSD, 1e-8)
	require.Len(t, result.BillingAllocations, 3)
	require.Equal(t, near.ID, *result.BillingAllocations[0].SubscriptionID)
	require.Equal(t, .5, result.BillingAllocations[0].RateMultiplier)
	require.Equal(t, far.ID, *result.BillingAllocations[1].SubscriptionID)
	require.Equal(t, 2.0, result.BillingAllocations[1].RateMultiplier)
	require.Equal(t, domain.BillingAllocationTypeBalance, result.BillingAllocations[2].Type)
	require.InDelta(t, 1, resetPriorityUsage(t, near.ID), 1e-8)
	require.InDelta(t, 2, resetPriorityUsage(t, far.ID), 1e-8)
	replayed, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, replayed.Applied)
	billingFocusAssertDecimal(t, "9.5", `SELECT balance::text FROM users WHERE id=$1`, userID)
}

// 明确指定订阅、异步固化范围和仅余额模式不能被新的自动优先级改写。
func TestUsageBillingResetPriorityExplicitSource(t *testing.T) {
	for _, source := range []string{"subscription", "scope", "balance"} {
		t.Run(source, func(t *testing.T) {
			repo, reserve, _ := newImageBillingIntegrationFixture(t, 10, 0)
			userID, groupID := reserve.Hold.UserID, *reserve.Hold.GroupID
			far := resetPrioritySubscription(t, userID, groupID, 12*time.Hour, 2*24*time.Hour, 10, 0, 2)
			near := resetPrioritySubscription(t, userID, groupID, time.Hour, 10*24*time.Hour, 10, 0, .5)
			cmd := &service.UsageBillingCommand{
				RequestID: "reset-explicit-" + uuid.NewString(), APIKeyID: reserve.Hold.APIKeyID, UserID: userID, GroupID: &groupID,
				APIKeyBillingMode: service.APIKeyBillingModeAuto, BaseAmountUSD: 1, SubscriptionRateMultiplier: 1,
				SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: .25,
			}
			switch source {
			case "subscription":
				cmd.APIKeyBillingMode, cmd.PreferredSubscriptionID = service.APIKeyBillingModeSubscription, &far.ID
			case "scope":
				cmd.SubscriptionScopeID = &far.ID
			case "balance":
				cmd.APIKeyBillingMode = service.APIKeyBillingModeBalance
			}
			result, err := repo.Apply(context.Background(), cmd)
			require.NoError(t, err)
			require.Zero(t, resetPriorityUsage(t, near.ID))
			if source == "balance" {
				require.Zero(t, resetPriorityUsage(t, far.ID))
				require.InDelta(t, .25, result.BalanceAmountUSD, 1e-8)
			} else {
				require.InDelta(t, 2, resetPriorityUsage(t, far.ID), 1e-8)
				require.Zero(t, result.BalanceAmountUSD)
			}
		})
	}
}

// 异步图片创建时按最近刷新选择，创建后重排其他套餐及编辑倍率不能改变已冻结合同。
func TestImageBillingResetPrioritySnapshotAndRelease(t *testing.T) {
	for _, capture := range []bool{false, true} {
		name := "release"
		if capture {
			name = "capture"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repo, cmd, _ := newImageBillingIntegrationFixture(t, 10, 0)
			userID, groupID := cmd.Hold.UserID, *cmd.Hold.GroupID
			far := resetPrioritySubscription(t, userID, groupID, 12*time.Hour, 2*24*time.Hour, 10, 0, .5)
			near := resetPrioritySubscription(t, userID, groupID, time.Hour, 10*24*time.Hour, 10, 0, 2)
			hold, err := repo.ReserveImageBilling(ctx, cmd)
			require.NoError(t, err)
			require.Len(t, hold.Hold.SubscriptionHoldAllocations, 1)
			require.Equal(t, near.ID, *hold.Hold.SubscriptionHoldAllocations[0].SubscriptionID)
			require.InDelta(t, .24, resetPriorityUsage(t, near.ID), 1e-8)
			_, err = integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET weekly_window_start=NOW()-INTERVAL '7 days'+INTERVAL '30 minutes' WHERE id=$1`, far.ID)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE subscription_plan_groups SET rate_multiplier=9 WHERE plan_id=$1`, near.PlanID)
			require.NoError(t, err)
			if capture {
				result, err := repo.CaptureImageBilling(ctx, hold.ID, imageBillingCaptureCommand(hold), .06)
				require.NoError(t, err)
				require.InDelta(t, .12, result.SubscriptionAmountUSD, 1e-8)
				require.InDelta(t, .12, resetPriorityUsage(t, near.ID), 1e-8)
			} else {
				require.NoError(t, repo.ReleaseImageBilling(ctx, hold.ID, userID, cmd.Hold.APIKeyID))
				require.Zero(t, resetPriorityUsage(t, near.ID))
			}
			require.Zero(t, resetPriorityUsage(t, far.ID))
			billingFocusAssertDecimal(t, "10", `SELECT balance::text FROM users WHERE id=$1`, userID)
		})
	}
}

// 秒计费视频与图片复用自动分配顺序，终态仍按原冻结资金和倍率退还差额。
func TestVideoBillingResetPrioritySnapshot(t *testing.T) {
	f := newVideoBillingFixture(t)
	far := resetPrioritySubscription(t, f.userID, f.groupID, 12*time.Hour, 2*24*time.Hour, 10, 0, .5)
	near := resetPrioritySubscription(t, f.userID, f.groupID, time.Hour, 10*24*time.Hour, 10, 0, 2)
	task := f.reserve(f.task(1, 1, 1))
	require.Len(t, task.Hold.SubscriptionHoldAllocations, 1)
	require.Equal(t, near.ID, *task.Hold.SubscriptionHoldAllocations[0].SubscriptionID)
	require.InDelta(t, 2, resetPriorityUsage(t, near.ID), 1e-8)
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE user_subscriptions SET weekly_window_start=NOW()-INTERVAL '7 days'+INTERVAL '30 minutes' WHERE id=$1`, far.ID)
	require.NoError(t, err)
	cmd := f.command(task, "completed", .5)
	result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
	require.NoError(t, err)
	require.InDelta(t, 1, result.SubscriptionAmountUSD, 1e-8)
	require.InDelta(t, 1, resetPriorityUsage(t, near.ID), 1e-8)
	require.Zero(t, resetPriorityUsage(t, far.ID))
	f.money(1000, 0, 1, .5)
}

// Token 预扣与延后结算沿用终态重新分配规则，并在当时按最近刷新套餐选择。
func TestVideoTokenBillingResetPriorityAtCompletion(t *testing.T) {
	for _, prepay := range []bool{false, true} {
		name := "deferred"
		if prepay {
			name = "prepay"
		}
		t.Run(name, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			far := resetPrioritySubscription(t, f.userID, f.groupID, 12*time.Hour, 2*24*time.Hour, 10, 0, .5)
			near := resetPrioritySubscription(t, f.userID, f.groupID, time.Hour, 10*24*time.Hour, 10, 0, 2)
			var task *service.VideoTaskRecord
			if prepay {
				task = f.reserve(tokenPrepayTestTask(t, f, 1, 0))
				require.Equal(t, near.ID, *task.Hold.SubscriptionHoldAllocations[0].SubscriptionID)
				require.InDelta(t, 2.4, resetPriorityUsage(t, near.ID), 1e-8)
			} else {
				task = f.reserve(newDeferredVideoTestTask(t, f, nil))
				require.Zero(t, resetPriorityUsage(t, near.ID))
			}
			// 终态时另一个套餐即将刷新，应按完成时顺序及其倍率重新分配。
			_, err := integrationDB.ExecContext(context.Background(), `UPDATE user_subscriptions SET weekly_window_start=NOW()-INTERVAL '7 days'+INTERVAL '30 minutes' WHERE id=$1`, far.ID)
			require.NoError(t, err)
			cmd := f.command(task, "completed", 1)
			result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.NoError(t, err)
			require.InDelta(t, .5, result.SubscriptionAmountUSD, 1e-8)
			require.InDelta(t, .5, resetPriorityUsage(t, far.ID), 1e-8)
			require.Zero(t, resetPriorityUsage(t, near.ID))
			f.money(1000, 0, .5, 1)
		})
	}
}

// Gemini 显式缓存复用预占账本，选择和部分捕获同样不能回到到期优先顺序。
func TestGeminiCacheBillingResetPriority(t *testing.T) {
	ctx := context.Background()
	repo, cmd, _ := newImageBillingIntegrationFixture(t, 10, 0)
	cmd.Quote = json.RawMessage(`{"kind":"gemini_cache"}`)
	cmd.Hold.RequestID = "gcache:create:reset-" + uuid.NewString()
	userID, groupID := cmd.Hold.UserID, *cmd.Hold.GroupID
	far := resetPrioritySubscription(t, userID, groupID, 12*time.Hour, 2*24*time.Hour, 10, 0, .5)
	near := resetPrioritySubscription(t, userID, groupID, time.Hour, 10*24*time.Hour, 10, 0, 2)
	hold, err := repo.ReserveImageBilling(ctx, cmd)
	require.NoError(t, err)
	require.Equal(t, near.ID, *hold.Hold.SubscriptionHoldAllocations[0].SubscriptionID)
	capture := imageBillingCaptureCommand(hold)
	capture.ImageCount, capture.Model = 0, "gemini-2.5-pro"
	result, err := repo.CaptureImageBilling(ctx, hold.ID, capture, .06)
	require.NoError(t, err)
	require.InDelta(t, .12, result.SubscriptionAmountUSD, 1e-8)
	require.InDelta(t, .12, resetPriorityUsage(t, near.ID), 1e-8)
	require.Zero(t, resetPriorityUsage(t, far.ID))
}

// 并发请求必须在用户锁内耗完最近刷新套餐，再使用后续套餐，且不得丢费或超额。
func TestUsageBillingResetPriorityConcurrent(t *testing.T) {
	repo, reserve, _ := newImageBillingIntegrationFixture(t, 10, 0)
	userID, groupID := reserve.Hold.UserID, *reserve.Hold.GroupID
	far := resetPrioritySubscription(t, userID, groupID, 12*time.Hour, 2*24*time.Hour, 10, 0, 2)
	near := resetPrioritySubscription(t, userID, groupID, time.Hour, 10*24*time.Hour, 1, 0, .5)
	const requests = 8
	results := make(chan usageBillingApplyOutcome, requests)
	var workers sync.WaitGroup
	for i := 0; i < requests; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			result, err := repo.Apply(context.Background(), &service.UsageBillingCommand{
				RequestID: "reset-concurrent-" + uuid.NewString(), APIKeyID: reserve.Hold.APIKeyID, UserID: userID, GroupID: &groupID,
				APIKeyBillingMode: service.APIKeyBillingModeAuto, BaseAmountUSD: .5,
				SubscriptionRateMultiplier: 1, SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: 1,
			})
			results <- usageBillingApplyOutcome{result: result, err: err}
		}()
	}
	workers.Wait()
	close(results)
	var charged float64
	for outcome := range results {
		require.NoError(t, outcome.err)
		require.True(t, outcome.result.Applied)
		require.Zero(t, outcome.result.BalanceAmountUSD)
		charged += outcome.result.SubscriptionAmountUSD
	}
	require.InDelta(t, 1, resetPriorityUsage(t, near.ID), 1e-8)
	require.InDelta(t, 4, resetPriorityUsage(t, far.ID), 1e-8)
	require.InDelta(t, 5, charged, 1e-8)
}
