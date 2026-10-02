//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// tokenPrepayTestTask 创建固定 8 秒预扣的隔离任务，不访问上游或业务数据库。
func tokenPrepayTestTask(t *testing.T, f *videoBillingFixture, rate, fixed float64) *service.VideoTaskRecord {
	t.Helper()
	task := fixedVideoTestTask(t, f, 2.4, rate, fixed, false)
	price := .3
	task.Quote.Mode = service.BillingModeVideoToken
	task.Quote.TokenPrepay = &service.VideoTokenPrepayConfig{PricePerSecond: &price}
	task.Hold.VideoTokenPrepay = true
	task.Hold.VideoPrepayDurationSeconds = 8
	require.NoError(t, f.tasks.Save(context.Background(), task, false))
	return task
}

// 预扣固定 2.70，不乘视频倍率；实际用量较多可补扣，较少或免费则退差额。
func TestVideoTaskTokenPrepayBalanceCapture(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		rate, actualBase, want float64
	}{
		{name: "refund", rate: 2, actualBase: .5, want: 1.3},
		{name: "top-up", rate: 2, actualBase: 3, want: 6.3},
		{name: "zero-video-rate", rate: 0, actualBase: 3, want: .3},
		{name: "zero-video-usage", rate: 3, actualBase: 0, want: .3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(100)
			task := f.reserve(tokenPrepayTestTask(t, f, tc.rate, .3))
			require.InDelta(t, 2.7, task.Hold.HoldAmount, 1e-8)
			require.Equal(t, tc.rate, task.Hold.BalanceRateMultiplier)
			f.money(997.3, 2.7, 2.7, 0)
			f.quotaUsage(2.7, 2.7, 2.7)
			repeatReserve, err := f.repo.ReserveBatchImageBalance(context.Background(), &task.Hold)
			require.NoError(t, err)
			require.False(t, repeatReserve.Applied)
			require.InDelta(t, 2.7, repeatReserve.EstimatedAmountUSD, 1e-8)
			if tc.name == "top-up" {
				// 实际费用恰好达到 Key 上限时，状态变化不能破坏后续幂等重放。
				_, err := integrationDB.ExecContext(context.Background(), `UPDATE api_keys SET quota=$2 WHERE id=$1`, f.keyID, tc.want)
				require.NoError(t, err)
			}
			cmd := f.command(task, "completed", tc.actualBase)
			cmd.VideoActualFixedAmountUSD = .3
			result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.NoError(t, err)
			require.InDelta(t, tc.want, result.ActualAmountUSD, 1e-8)
			require.InDelta(t, 2.7, result.HoldAmountUSD, 1e-8)
			replayed, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.NoError(t, err)
			require.False(t, replayed.Applied)
			require.Equal(t, result.BillingAllocations, replayed.BillingAllocations)
			f.money(1000-tc.want, 0, tc.want, tc.actualBase)
			f.quotaUsage(tc.want, tc.want, tc.want)
			fresh, err := f.tasks.Get(context.Background(), task.ID)
			require.NoError(t, err)
			require.False(t, fresh.Hold.AllowanceReserved)
			require.InDelta(t, 2.7, fresh.Hold.HoldAmount, 1e-8)
			if tc.name == "top-up" {
				var status string
				require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT status FROM api_keys WHERE id=$1`, f.keyID).Scan(&status))
				require.Equal(t, service.StatusAPIKeyQuotaExhausted, status)
			}
		})
	}
}

// 固定预扣不使用套餐覆盖倍率，终态重新选取可用套餐及其倍率，并可混合使用余额。
func TestVideoTaskTokenPrepaySubscriptionAllocation(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		limit, finalRate, sub, balance float64
	}{
		{name: "plan-rate-at-completion", limit: 100, finalRate: .25, sub: 1.3},
		{name: "mixed", limit: 1, finalRate: .5, sub: 1, balance: 6.3},
		{name: "free-subscription", limit: 100, finalRate: 0, sub: .3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(100)
			sub := fixedVideoSubscription(t, f, tc.limit, 9)
			rate := 3.0
			if tc.finalRate == 0 {
				rate = 0
			}
			task := f.reserve(tokenPrepayTestTask(t, f, rate, .3))
			require.InDelta(t, 2.7, task.Hold.HoldAmount, 1e-8)
			for _, a := range task.Hold.SubscriptionHoldAllocations {
				require.Equal(t, 1.0, a.RateMultiplier)
			}
			_, err := integrationDB.ExecContext(context.Background(), `UPDATE subscription_plan_groups SET rate_multiplier=$2 WHERE plan_id=$1`, sub.PlanID, tc.finalRate)
			require.NoError(t, err)
			cmd := f.command(task, "completed", 4)
			cmd.VideoActualFixedAmountUSD = .3
			result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.NoError(t, err)
			require.InDelta(t, tc.sub, result.SubscriptionAmountUSD, 1e-8)
			require.InDelta(t, tc.balance, result.BalanceAmountUSD, 1e-8)
			fixedVideoAssertSubscription(t, sub.ID, tc.sub)
			f.money(1000-tc.balance, 0, tc.sub+tc.balance, 4)
			f.quotaUsage(tc.balance, tc.balance, tc.balance)
		})
	}
}

// 补扣拒绝时原冻结、订阅/平台/Key 预占及捕获去重一起回滚，补足后可再次成功结算。
func TestVideoTaskTokenPrepayTopUpRollbackAndRetry(t *testing.T) {
	for _, kind := range []string{"balance", "key", "platform", "subscription"} {
		t.Run(kind, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(100)
			task := tokenPrepayTestTask(t, f, 2, .3)
			var sub *service.UserSubscription
			if kind == "subscription" {
				sub = fixedVideoSubscription(t, f, 3, 1)
				task.Hold.APIKeyBillingMode = service.APIKeyBillingModeSubscription
				task.Hold.PreferredSubscriptionID = &sub.ID
				require.NoError(t, f.tasks.Save(context.Background(), task, false))
			}
			task = f.reserve(task)
			wantErr := service.ErrBatchImageInsufficientBalance
			var restore string
			var target int64
			switch kind {
			case "balance":
				_, err := integrationDB.ExecContext(context.Background(), `UPDATE users SET balance=0 WHERE id=$1`, f.userID)
				require.NoError(t, err)
				restore, target = `UPDATE users SET balance=10 WHERE id=$1`, f.userID
			case "key":
				_, err := integrationDB.ExecContext(context.Background(), `UPDATE api_keys SET quota=4 WHERE id=$1`, f.keyID)
				require.NoError(t, err)
				wantErr, restore, target = service.ErrAPIKeyQuotaExhausted, `UPDATE api_keys SET quota=100 WHERE id=$1`, f.keyID
			case "platform":
				_, err := integrationDB.ExecContext(context.Background(), `UPDATE user_platform_quotas SET daily_limit_usd=4 WHERE user_id=$1`, f.userID)
				require.NoError(t, err)
				wantErr, restore, target = service.ErrUserPlatformDailyQuotaExhausted, `UPDATE user_platform_quotas SET daily_limit_usd=100 WHERE user_id=$1`, f.userID
			case "subscription":
				wantErr, restore, target = service.ErrPreferredSubscriptionInsufficient, `UPDATE user_subscriptions SET daily_limit_usd=100,weekly_limit_usd=100,monthly_limit_usd=100 WHERE id=$1`, sub.ID
			}
			cmd := f.command(task, "completed", 4)
			cmd.VideoActualFixedAmountUSD = .3
			_, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.ErrorIs(t, err, wantErr)
			fresh, err := f.tasks.Get(context.Background(), task.ID)
			require.NoError(t, err)
			require.Equal(t, "reserved", fresh.BillingStatus)
			require.True(t, fresh.Hold.AllowanceReserved)
			require.Nil(t, fresh.BillingResult)
			var count int
			require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT count(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2`, cmd.RequestID, f.keyID).Scan(&count))
			require.Zero(t, count)
			if sub != nil {
				fixedVideoAssertSubscription(t, sub.ID, 2.7)
				f.money(1000, 0, 2.7, 0)
			} else {
				balance := 997.3
				if kind == "balance" {
					balance = 0
				}
				f.money(balance, 2.7, 2.7, 0)
				f.quotaUsage(2.7, 2.7, 2.7)
			}
			_, err = integrationDB.ExecContext(context.Background(), restore, target)
			require.NoError(t, err)
			result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.NoError(t, err)
			require.True(t, result.Applied)
			want := 8.3
			if sub != nil {
				want = 4.3
				fixedVideoAssertSubscription(t, sub.ID, want)
			}
			require.InDelta(t, want, result.ActualAmountUSD, 1e-8)
		})
	}
}

// 跨窗口或手动重置只退原代次，实际金额进入终态当前窗口，不能冲掉新用量。
func TestVideoTaskTokenPrepayWindowIsolation(t *testing.T) {
	for _, subscription := range []bool{false, true} {
		t.Run(map[bool]string{false: "platform", true: "subscription"}[subscription], func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(100)
			var sub *service.UserSubscription
			if subscription {
				sub = fixedVideoSubscription(t, f, 100, .5)
			}
			task := f.reserve(tokenPrepayTestTask(t, f, 1, .3))
			if subscription {
				_, err := integrationDB.ExecContext(context.Background(), `UPDATE user_subscriptions SET daily_usage_usd=5,daily_reset_generation=daily_reset_generation+1,weekly_usage_usd=6,weekly_window_start=weekly_window_start+INTERVAL '1 second' WHERE id=$1`, sub.ID)
				require.NoError(t, err)
			} else {
				_, err := integrationDB.ExecContext(context.Background(), `UPDATE user_platform_quotas SET daily_usage_usd=5,daily_reset_generation=daily_reset_generation+1 WHERE user_id=$1`, f.userID)
				require.NoError(t, err)
			}
			cmd := f.command(task, "completed", 4)
			cmd.VideoActualFixedAmountUSD = .3
			_, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.NoError(t, err)
			if subscription {
				var d, w, m float64
				require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&d, &w, &m))
				require.InDelta(t, 7.3, d, 1e-8)
				require.InDelta(t, 8.3, w, 1e-8)
				require.InDelta(t, 2.3, m, 1e-8)
			} else {
				f.quotaUsage(9.3, 4.3, 4.3)
			}
		})
	}
}

// 全额退款与零实收都只能释放一次，不得重复减掉其他任务的平台用量。
func TestVideoTaskTokenPrepayReleaseAndZeroActual(t *testing.T) {
	for _, status := range []string{"failed", "cancelled", "completed"} {
		t.Run(status, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(100)
			other := f.reserve(f.task(1, 1, 1))
			otherCmd := f.command(other, "completed", 1)
			_, err := f.repo.CaptureBatchImageBalance(context.Background(), &otherCmd)
			require.NoError(t, err)
			task := f.reserve(tokenPrepayTestTask(t, f, 1, 0))
			cmd := f.command(task, status, 0)
			apply := f.repo.ReleaseBatchImageBalance
			if status == "completed" {
				apply = f.repo.CaptureBatchImageBalance
			}
			result, err := apply(context.Background(), &cmd)
			require.NoError(t, err)
			require.True(t, result.Applied)
			result, err = apply(context.Background(), &cmd)
			require.NoError(t, err)
			require.False(t, result.Applied)
			f.money(999, 0, 1, 1)
			f.quotaUsage(1, 1, 1)
		})
	}
}

// 新模式标记和时长必须来自持久任务，不能把旧 cap 任务临时升级为可补扣任务。
func TestVideoTaskTokenPrepayGuard(t *testing.T) {
	f := newVideoBillingFixture(t)
	task := f.reserve(f.task(2.4, 1, 1))
	cmd := f.command(task, "completed", 4)
	cmd.VideoTokenPrepay, cmd.VideoPrepayDurationSeconds = true, 8
	_, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
	require.ErrorIs(t, err, service.ErrVideoTaskConflict)
	f.money(997.6, 2.4, 2.4, 0)
}

// 未开启预扣的新 Token 任务即使视频免费，也将已知图片费留到完成时严格结算。
func TestVideoTaskTokenPrepayDisabledFreeVideoDeferred(t *testing.T) {
	f := newVideoBillingFixture(t)
	f.quota(100)
	task := fixedVideoTestTask(t, f, 0, 3, 2, true)
	task.Quote.UnitPrice, task.Quote.BaseUnitPrice = 0, 0
	require.NoError(t, f.tasks.Save(context.Background(), task, false))
	task = f.reserve(task)
	f.money(1000, 0, 0, 0)
	require.Zero(t, task.Hold.HoldAmount)
	require.False(t, task.Hold.AllowanceReserved)
	cmd := f.command(task, "completed", 0)
	cmd.VideoActualFixedAmountUSD = 2
	result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
	require.NoError(t, err)
	require.Equal(t, 2.0, result.ActualAmountUSD)
	require.Zero(t, result.HoldAmountUSD)
	require.Len(t, result.BillingAllocations, 1)
	require.Equal(t, service.VideoImageInputBillingComponent, result.BillingAllocations[0].Component)
	require.Equal(t, 1.0, result.BillingAllocations[0].RateMultiplier)
	f.money(998, 0, 2, 0)
	f.quotaUsage(2, 2, 2)
}

// 多任务竞争有限余额时，只有有能力补足差额的任务可提交；每个成功任务仍只结算一次。
func TestVideoTaskTokenPrepayConcurrentTopUps(t *testing.T) {
	f := newVideoBillingFixture(t)
	const count = 20
	commands := make([]service.BatchImageBalanceHoldCommand, count)
	for i := range commands {
		task := f.reserve(tokenPrepayTestTask(t, f, 1, 0))
		commands[i] = f.command(task, "completed", 4.4)
	}
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE users SET balance=10 WHERE id=$1`, f.userID)
	require.NoError(t, err)
	var wg sync.WaitGroup
	var applied atomic.Int64
	errs := make(chan error, count)
	for _, original := range commands {
		wg.Add(1)
		go func(cmd service.BatchImageBalanceHoldCommand) {
			defer wg.Done()
			result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			if err == nil {
				if result.Applied {
					applied.Add(1)
				}
				replay, repeatErr := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
				if repeatErr != nil || replay.Applied || replay.ActualAmountUSD != result.ActualAmountUSD {
					err = fmt.Errorf("预扣捕获重放不一致: %v", repeatErr)
				}
			} else if err == service.ErrBatchImageInsufficientBalance {
				err = nil
			}
			errs <- err
		}(original)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 5, applied.Load())
	f.money(0, 36, 58, 22)
}

// 团队额度补扣失败同样必须回滚原预记，不能把新模式变成额度绕过路径。
func TestVideoTaskTokenPrepayTeamQuotaRollback(t *testing.T) {
	f := newVideoBillingFixture(t)
	ctx := context.Background()
	teamRepo := NewTeamRepository(integrationDB)
	member := mustCreateUser(t, integrationEntClient, &service.User{Email: uniqueTeamTestEmail("prepay-member")})
	team, err := teamRepo.Create(ctx, "视频预扣团队", f.userID, 5)
	require.NoError(t, err)
	token := uuid.NewString()
	_, err = teamRepo.CreateInvitation(ctx, team.Team.ID, f.userID, member.Email, token, time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = teamRepo.ResolveInvitation(ctx, token, member.ID, member.Email, "accepted", time.Now())
	require.NoError(t, err)
	require.NoError(t, teamRepo.UpdateMemberLimits(ctx, team.Team.ID, member.ID, 4, 10, 30))
	task := tokenPrepayTestTask(t, f, 2, .3)
	task.Hold.TeamID, task.Hold.ActorUserID = &team.Team.ID, member.ID
	require.NoError(t, f.tasks.Save(ctx, task, false))
	task = f.reserve(task)
	cmd := f.command(task, "completed", 3)
	cmd.VideoActualFixedAmountUSD = .3
	_, err = f.repo.CaptureBatchImageBalance(ctx, &cmd)
	require.ErrorIs(t, err, service.ErrTeamMemberDailyExceeded)
	f.money(997.3, 2.7, 2.7, 0)
	var usage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM team_memberships WHERE team_id=$1 AND user_id=$2`, team.Team.ID, member.ID).Scan(&usage))
	require.InDelta(t, 2.7, usage, 1e-8)
	// 财务快照仍保持原预扣，没有保存部分捕获结果。
	var raw []byte
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT billing_result FROM video_tasks WHERE id=$1`, task.ID).Scan(&raw))
	require.True(t, len(raw) == 0 || string(raw) == "null")
}
