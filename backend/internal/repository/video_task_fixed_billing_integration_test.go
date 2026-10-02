//go:build integration

package repository

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// fixedVideoTestTask 仅在隔离库中补充固定价快照，所有测试都不访问上游。
func fixedVideoTestTask(t *testing.T, f *videoBillingFixture, base, rate, fixed float64, deferred bool) *service.VideoTaskRecord {
	t.Helper()
	var task *service.VideoTaskRecord
	if deferred {
		task = newDeferredVideoTestTask(t, f, nil)
	} else {
		task = f.task(base, rate, rate)
	}
	count := 1
	if task.Quote == nil {
		task.Quote = &service.VideoPriceQuote{Version: 1, Mode: service.BillingModeVideo, UnitPrice: 1, RateMultiplier: rate}
	}
	task.Quote.ReferenceImageCount = &count
	task.Quote.ImageInputPricing = &service.VideoImageInputPricing{Price: &fixed}
	task.Hold.VideoFixedAmountUSD = fixed
	task.Hold.SubscriptionRateMultiplier, task.Hold.BalanceRateMultiplier = rate, rate
	require.NoError(t, f.tasks.Save(context.Background(), task, false))
	return task
}

func fixedVideoSubscription(t *testing.T, f *videoBillingFixture, limit, rate float64) *service.UserSubscription {
	t.Helper()
	sub := videoResetTestSubscription(t, f)
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE user_subscriptions SET daily_limit_usd=$2,weekly_limit_usd=$2,monthly_limit_usd=$2 WHERE id=$1`, sub.ID, limit)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(context.Background(), `UPDATE subscription_plan_groups SET rate_multiplier=$2 WHERE plan_id=$1`, sub.PlanID, rate)
	require.NoError(t, err)
	return sub
}

func fixedVideoAssertSubscription(t *testing.T, subID int64, want float64) {
	t.Helper()
	var daily, weekly, monthly float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE id=$1`, subID).Scan(&daily, &weekly, &monthly))
	require.InDelta(t, want, daily, 1e-8)
	require.InDelta(t, want, weekly, 1e-8)
	require.InDelta(t, want, monthly, 1e-8)
}

// 视频倍率为零、余额倍率及免费视频都不能吞掉固定图片附加费。
func TestVideoTaskFixedBillingBalanceAndFreeVideo(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		base, rate, actual, hold, due float64
	}{
		{name: "balance-rate", base: 10, rate: 3, actual: 4, hold: 32, due: 14},
		{name: "zero-group-rate", base: 10, rate: 0, actual: 4, hold: 2, due: 2},
		{name: "free-video", base: 0, rate: 3, actual: 0, hold: 2, due: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(100)
			task := f.reserve(fixedVideoTestTask(t, f, tc.base, tc.rate, 2, false))
			f.money(1000-tc.hold, tc.hold, tc.hold, 0)
			cmd := f.command(task, "completed", tc.actual)
			cmd.VideoActualFixedAmountUSD = 2
			result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.NoError(t, err)
			require.Equal(t, tc.due, result.ActualAmountUSD)
			fixed := result.BillingAllocations[len(result.BillingAllocations)-1]
			require.Equal(t, service.VideoImageInputBillingComponent, fixed.Component)
			require.Equal(t, 1.0, fixed.RateMultiplier)
			require.Equal(t, 2.0, fixed.AmountUSD)
			replayed, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.NoError(t, err)
			require.False(t, replayed.Applied)
			require.Equal(t, result.BillingAllocations, replayed.BillingAllocations)
			f.money(1000-tc.due, 0, tc.due, tc.actual)
			f.quotaUsage(tc.due, tc.due, tc.due)
		})
	}
}

// 套餐覆盖倍率只影响视频，固定费可以在同一套餐和余额之间分摊并独立退款。
func TestVideoTaskFixedBillingSubscriptionAndMixed(t *testing.T) {
	for _, tc := range []struct {
		name                                     string
		limit, actualFixed, wantSub, wantBalance float64
	}{
		{name: "subscription-override", limit: 100, actualFixed: 2, wantSub: 4},
		{name: "mixed", limit: 6, actualFixed: 2, wantSub: 3, wantBalance: 1},
		{name: "fixed-difference-refund", limit: 6, actualFixed: 1, wantSub: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(100)
			sub := fixedVideoSubscription(t, f, tc.limit, .5)
			task := f.reserve(fixedVideoTestTask(t, f, 10, 3, 2, false))
			for _, a := range task.Hold.SubscriptionHoldAllocations {
				if a.Component == service.VideoImageInputBillingComponent {
					require.Equal(t, 1.0, a.RateMultiplier)
				} else {
					require.Equal(t, .5, a.RateMultiplier)
				}
			}
			// 后续套餐编辑不影响已冻结的两个组件。
			_, err := integrationDB.ExecContext(context.Background(), `UPDATE subscription_plan_groups SET rate_multiplier=9 WHERE plan_id=$1`, sub.PlanID)
			require.NoError(t, err)
			cmd := f.command(task, "completed", 4)
			cmd.VideoActualFixedAmountUSD = tc.actualFixed
			result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.NoError(t, err)
			require.Equal(t, tc.wantSub, result.SubscriptionAmountUSD)
			require.Equal(t, tc.wantBalance, result.BalanceAmountUSD)
			fixedVideoAssertSubscription(t, sub.ID, tc.wantSub)
			f.money(1000-tc.wantBalance, 0, tc.wantSub+tc.wantBalance, 4)
			f.quotaUsage(tc.wantBalance, tc.wantBalance, tc.wantBalance)
		})
	}
}

// 失败任务退回两种组件；指定订阅不能在固定费不足时悄悄改扣余额。
func TestVideoTaskFixedBillingReleaseAndPreferredRollback(t *testing.T) {
	for _, preferred := range []bool{false, true} {
		t.Run(map[bool]string{false: "release", true: "preferred-insufficient"}[preferred], func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(100)
			sub := fixedVideoSubscription(t, f, 6, .5)
			task := fixedVideoTestTask(t, f, 10, 3, 2, false)
			if preferred {
				task.Hold.APIKeyBillingMode = service.APIKeyBillingModeSubscription
				task.Hold.PreferredSubscriptionID = &sub.ID
				require.NoError(t, f.tasks.Save(context.Background(), task, false))
				_, err := f.repo.ReserveBatchImageBalance(context.Background(), &task.Hold)
				require.ErrorIs(t, err, service.ErrPreferredSubscriptionInsufficient)
			} else {
				task = f.reserve(task)
				cmd := f.command(task, "failed", 0)
				first, err := f.repo.ReleaseBatchImageBalance(context.Background(), &cmd)
				require.NoError(t, err)
				require.True(t, first.Applied)
				repeat, err := f.repo.ReleaseBatchImageBalance(context.Background(), &cmd)
				require.NoError(t, err)
				require.False(t, repeat.Applied)
			}
			fixedVideoAssertSubscription(t, sub.ID, 0)
			f.money(1000, 0, 0, 0)
			f.quotaUsage(0, 0, 0)
		})
	}
}

// 无预算任务创建不预留固定费，终态按实时可用套餐分配，但固定费仍保持 1 倍。
func TestVideoTaskFixedBillingDeferred(t *testing.T) {
	for _, withSubscription := range []bool{false, true} {
		t.Run(map[bool]string{false: "balance", true: "subscription"}[withSubscription], func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(100)
			var sub *service.UserSubscription
			if withSubscription {
				sub = fixedVideoSubscription(t, f, 100, .5)
			}
			task := f.reserve(fixedVideoTestTask(t, f, 0, 3, 2, true))
			f.money(1000, 0, 0, 0)
			require.Equal(t, 2.0, task.Hold.VideoFixedAmountUSD)
			require.Zero(t, task.Hold.HoldAmount)
			require.False(t, task.Hold.AllowanceReserved)
			cmd := f.command(task, "completed", 4)
			cmd.VideoActualFixedAmountUSD = 2
			result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.NoError(t, err)
			require.Zero(t, result.HoldAmountUSD)
			want := 14.0
			if sub != nil {
				want = 4
				fixedVideoAssertSubscription(t, sub.ID, want)
				f.money(1000, 0, want, 4)
				f.quotaUsage(0, 0, 0)
			} else {
				f.money(986, 0, want, 4)
				f.quotaUsage(want, want, want)
			}
			require.Equal(t, want, result.ActualAmountUSD)
			repeat, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.NoError(t, err)
			require.False(t, repeat.Applied)
			require.Equal(t, want, repeat.ActualAmountUSD)
		})
	}
}

// 固定费不能绕过钱包、密钥与平台限额；失败必须回滚终态和捕获去重。
func TestVideoTaskFixedBillingLimitsAndGuard(t *testing.T) {
	for _, kind := range []string{"balance", "key", "platform", "fixed-over-snapshot", "fixed-tiny-over-snapshot"} {
		t.Run(kind, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(100)
			task := f.reserve(fixedVideoTestTask(t, f, 0, 0, 2, true))
			cmd := f.command(task, "completed", 4)
			cmd.VideoActualFixedAmountUSD = 2
			wantErr := service.ErrVideoTaskConflict
			switch kind {
			case "balance":
				_, err := integrationDB.ExecContext(context.Background(), `UPDATE users SET balance=1 WHERE id=$1`, f.userID)
				require.NoError(t, err)
				wantErr = service.ErrBatchImageInsufficientBalance
			case "key":
				_, err := integrationDB.ExecContext(context.Background(), `UPDATE api_keys SET quota=1 WHERE id=$1`, f.keyID)
				require.NoError(t, err)
				wantErr = service.ErrAPIKeyQuotaExhausted
			case "platform":
				_, err := integrationDB.ExecContext(context.Background(), `UPDATE user_platform_quotas SET daily_limit_usd=1 WHERE user_id=$1`, f.userID)
				require.NoError(t, err)
				wantErr = service.ErrUserPlatformDailyQuotaExhausted
			case "fixed-over-snapshot":
				cmd.VideoActualFixedAmountUSD = 3
			case "fixed-tiny-over-snapshot":
				cmd.VideoActualFixedAmountUSD = 2.000000001
			}
			_, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.ErrorIs(t, err, wantErr)
			assertDeferredVideoUncharged(t, f, task)
			balance := 1000.0
			if kind == "balance" {
				balance = 1
			}
			f.money(balance, 0, 0, 0)
			f.quotaUsage(0, 0, 0)
		})
	}
}

// 同一任务并发重放仍只捕获一次，持久结果须含固定费来源及准确金额。
func TestVideoTaskFixedBillingConcurrentReplay(t *testing.T) {
	f := newVideoBillingFixture(t)
	task := f.reserve(fixedVideoTestTask(t, f, 10, 3, 2, false))
	command := f.command(task, "completed", 4)
	command.VideoActualFixedAmountUSD = 2
	var wg sync.WaitGroup
	var applied atomic.Int64
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := command
			result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			if err == nil {
				if result.Applied {
					applied.Add(1)
				}
				if result.ActualAmountUSD != 14 || len(result.BillingAllocations) != 2 || result.BillingAllocations[1].Component != service.VideoImageInputBillingComponent || result.BillingAllocations[1].Type != domain.BillingAllocationTypeBalance {
					err = service.ErrVideoTaskConflict
				}
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, applied.Load())
	f.money(986, 0, 14, 4)
}

// 免费订阅覆盖必须冻结基础用量，即使余额倍率较高，也不能在捕获时重新扣余额。
func TestVideoTaskFixedBillingZeroSubscriptionCoverage(t *testing.T) {
	for _, fixed := range []float64{0, 2} {
		t.Run(map[float64]string{0: "video-only", 2: "with-fixed-images"}[fixed], func(t *testing.T) {
			f := newVideoBillingFixture(t)
			sub := fixedVideoSubscription(t, f, 100, 0)
			task := fixedVideoTestTask(t, f, 10, 0, fixed, false)
			task.Hold.BalanceRateMultiplier = 3
			require.NoError(t, f.tasks.Save(context.Background(), task, false))
			task = f.reserve(task)
			require.NotEmpty(t, task.Hold.SubscriptionHoldAllocations)
			free := task.Hold.SubscriptionHoldAllocations[0]
			require.Zero(t, free.AmountUSD)
			require.Zero(t, free.RateMultiplier)
			require.Equal(t, 10.0, free.BaseAmountUSD)
			_, err := integrationDB.ExecContext(context.Background(), `UPDATE subscription_plan_groups SET rate_multiplier=9 WHERE plan_id=$1`, sub.PlanID)
			require.NoError(t, err)
			cmd := f.command(task, "completed", 4)
			cmd.VideoActualFixedAmountUSD = fixed
			result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.NoError(t, err)
			require.Equal(t, fixed, result.ActualAmountUSD)
			require.Equal(t, 4.0, result.BillingAllocations[0].BaseAmountUSD)
			fixedVideoAssertSubscription(t, sub.ID, fixed)
			f.money(1000, 0, fixed, 4)
		})
	}
}

// 第一份付费套餐用尽后，后续免费套餐的覆盖也不能在结算时丢失。
func TestVideoTaskFixedBillingPaidThenZeroSubscription(t *testing.T) {
	f := newVideoBillingFixture(t)
	paid := fixedVideoSubscription(t, f, 1, .5)
	free := fixedVideoSubscription(t, f, 100, 0)
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE user_subscriptions SET expires_at=NOW()+CASE WHEN id=$1 THEN INTERVAL '10 days' ELSE INTERVAL '20 days' END WHERE id IN($1,$2)`, paid.ID, free.ID)
	require.NoError(t, err)
	task := fixedVideoTestTask(t, f, 10, 0, 2, false)
	task.Hold.BalanceRateMultiplier = 3
	require.NoError(t, f.tasks.Save(context.Background(), task, false))
	task = f.reserve(task)
	require.Len(t, task.Hold.SubscriptionHoldAllocations, 3)
	require.Equal(t, 8.0, task.Hold.SubscriptionHoldAllocations[1].BaseAmountUSD)
	cmd := f.command(task, "completed", 4)
	cmd.VideoActualFixedAmountUSD = 2
	result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
	require.NoError(t, err)
	require.Equal(t, 3.0, result.ActualAmountUSD)
	fixedVideoAssertSubscription(t, paid.ID, 1)
	fixedVideoAssertSubscription(t, free.ID, 2)
	f.money(1000, 0, 3, 4)
}

// 固定图片费在金额入口量化到八位，半边界不能使余额与额度发生不同舍入。
func TestVideoTaskFixedBillingMonetaryPrecision(t *testing.T) {
	for _, fixed := range []float64{.000000015, .000000002} {
		t.Run(map[float64]string{.000000015: "half-boundary", .000000002: "below-ledger-unit"}[fixed], func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(100)
			task := f.reserve(fixedVideoTestTask(t, f, 0, 3, fixed, false))
			cmd := f.command(task, "completed", 0)
			cmd.VideoActualFixedAmountUSD = fixed
			result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.NoError(t, err)
			want := service.QuantizeUsageBillingAmount(fixed)
			require.Equal(t, want, result.ActualAmountUSD)
			f.money(1000-want, 0, want, 0)
			f.quotaUsage(want, want, want)
		})
	}
}
