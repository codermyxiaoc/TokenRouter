//go:build integration

package repository

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// newDeferredVideoTestTask 只创建隔离数据库中的零预算 Token 任务，不调用任何视频上游。
func newDeferredVideoTestTask(t *testing.T, f *videoBillingFixture, configure func(*service.VideoTaskRecord)) *service.VideoTaskRecord {
	t.Helper()
	id, lease := "video_deferred_"+uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	record := &service.VideoTaskRecord{ID: id, UserID: f.userID, APIKeyID: f.keyID, GroupID: f.groupID, AccountID: f.accountID,
		PayloadHash: uuid.NewString(), Status: "prepared", BillingStatus: "pending", CreatedAt: now, LeaseToken: lease, LeaseUntil: now.Add(time.Minute),
		Target: service.VideoUpstreamTarget{Endpoint: service.VideoEndpointSeedance}, Quote: &service.VideoPriceQuote{Version: 1, Mode: service.BillingModeVideoToken, UnitPrice: 1, RateMultiplier: 1}}
	record.Hold = service.BatchImageBalanceHoldCommand{RequestID: "video_hold:" + id, BatchID: id, UserID: f.userID, ActorUserID: f.userID,
		APIKeyID: f.keyID, GroupID: &f.groupID, APIKeyBillingMode: service.APIKeyBillingModeAuto, PricingSnapshotVersion: 3,
		SubscriptionRateMultiplier: 1, SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: 1, SettlementRateScale: 1,
		ReservedAt: now, VideoEntity: true, VideoAccountID: f.accountID, VideoLeaseToken: lease, VideoDeferredBilling: true}
	if configure != nil {
		configure(record)
	}
	_, created, err := f.tasks.Create(context.Background(), record, 1000)
	require.NoError(t, err)
	require.True(t, created)
	return record
}

// assertDeferredVideoUncharged 同时确认捕获去重键回滚，避免补足余额后的重试被误认成已扣费。
func assertDeferredVideoUncharged(t *testing.T, f *videoBillingFixture, task *service.VideoTaskRecord) {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT count(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2", "video_capture:"+task.ID, f.keyID).Scan(&count))
	require.Zero(t, count)
	fresh, err := f.tasks.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.NotEqual(t, "settled", fresh.BillingStatus)
	require.Nil(t, fresh.BillingResult)
	require.Zero(t, fresh.Hold.HoldAmount)
	require.Zero(t, fresh.Hold.BalanceHoldAmount)
	require.False(t, fresh.Hold.AllowanceReserved)
}

func TestVideoTaskDeferredBillingConcurrentCaptureAndReplay(t *testing.T) {
	// 隔离 PostgreSQL 默认最多 100 个连接；120 个逻辑调用通过有界池竞争，避免测成连接耗尽。
	previousLimit := integrationDB.Stats().MaxOpenConnections
	integrationDB.SetMaxOpenConns(64)
	defer integrationDB.SetMaxOpenConns(previousLimit)
	f := newVideoBillingFixture(t)
	f.quota(1000)
	const count = 60
	commands := make([]service.BatchImageBalanceHoldCommand, count)
	for i := range commands {
		task := f.reserve(newDeferredVideoTestTask(t, f, nil))
		commands[i] = f.command(task, "completed", 2)
	}
	f.money(1000, 0, 0, 0)
	f.quotaUsage(0, 0, 0)
	var wg sync.WaitGroup
	var applied atomic.Int64
	errs := make(chan error, count*2)
	for _, cmd := range commands {
		// 同任务并发捕获只应用一次，所有重放必须返回持久实际金额而不是零预算推导值。
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(cmd service.BatchImageBalanceHoldCommand) {
				defer wg.Done()
				result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
				if err == nil && (result.ActualAmountUSD != 2 || result.BalanceAmountUSD != 2 || result.HoldAmountUSD != 0) {
					err = errors.New("延后计费重放返回了错误金额")
				}
				if err == nil && result.Applied {
					applied.Add(1)
				}
				errs <- err
			}(cmd)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, count, applied.Load())
	f.money(880, 0, 120, 120)
	f.quotaUsage(120, 120, 120)
}

func TestVideoTaskDeferredBillingConcurrentLimitedFunds(t *testing.T) {
	for _, kind := range []string{"balance", "key", "platform"} {
		t.Run(kind, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(1000)
			wantErr := service.ErrBatchImageInsufficientBalance
			switch kind {
			case "balance":
				_, err := integrationDB.ExecContext(context.Background(), "UPDATE users SET balance=10 WHERE id=$1", f.userID)
				require.NoError(t, err)
			case "key":
				_, err := integrationDB.ExecContext(context.Background(), "UPDATE api_keys SET quota=10 WHERE id=$1", f.keyID)
				require.NoError(t, err)
				wantErr = service.ErrAPIKeyQuotaExhausted
			case "platform":
				_, err := integrationDB.ExecContext(context.Background(), "UPDATE user_platform_quotas SET daily_limit_usd=10 WHERE user_id=$1 AND platform='video'", f.userID)
				require.NoError(t, err)
				wantErr = service.ErrUserPlatformDailyQuotaExhausted
			}
			const count = 60
			commands := make([]service.BatchImageBalanceHoldCommand, count)
			for i := range commands {
				task := f.reserve(newDeferredVideoTestTask(t, f, nil))
				commands[i] = f.command(task, "completed", 2)
			}
			start, errs := make(chan struct{}), make(chan error, count)
			var wg sync.WaitGroup
			for _, cmd := range commands {
				wg.Add(1)
				go func(cmd service.BatchImageBalanceHoldCommand) {
					defer wg.Done()
					<-start
					_, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
					errs <- err
				}(cmd)
			}
			close(start)
			wg.Wait()
			close(errs)
			var success, limited int
			for err := range errs {
				if err == nil {
					success++
				} else {
					require.ErrorIs(t, err, wantErr)
					limited++
				}
			}
			require.Equal(t, 5, success)
			require.Equal(t, 55, limited)
			balance := 990.0
			if kind == "balance" {
				balance = 0
			}
			f.money(balance, 0, 10, 10)
			f.quotaUsage(10, 10, 10)
			var settled, unpaid, captures int
			require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT count(*) FILTER (WHERE billing_status='settled'),
 count(*) FILTER (WHERE billing_status='reserved' AND billing_result IS NULL AND hold_amount=0 AND allowance_reserved=FALSE)
 FROM video_tasks WHERE api_key_id=$1`, f.keyID).Scan(&settled, &unpaid))
			// 测试夹具会重置 Key 序列但保留去重历史，因此必须联结本轮实际任务而非只按 Key 计数。
			require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT count(*) FROM usage_billing_dedup d
 JOIN video_tasks t ON t.api_key_id=d.api_key_id AND d.request_id='video_capture:'||t.id WHERE t.api_key_id=$1`, f.keyID).Scan(&captures))
			require.Equal(t, 5, settled)
			require.Equal(t, 55, unpaid)
			require.Equal(t, 5, captures, "失败捕获不能留下幂等键或部分扣款")
		})
	}
}

func TestVideoTaskDeferredBillingInsufficientBalanceRetryAndOwner(t *testing.T) {
	f := newVideoBillingFixture(t)
	f.quota(100)
	task := f.reserve(newDeferredVideoTestTask(t, f, nil))
	_, err := integrationDB.ExecContext(context.Background(), "UPDATE users SET balance=1 WHERE id=$1", f.userID)
	require.NoError(t, err)
	cmd := f.command(task, "completed", 3)
	_, err = f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
	require.ErrorIs(t, err, service.ErrBatchImageInsufficientBalance)
	assertDeferredVideoUncharged(t, f, task)
	f.money(1, 0, 0, 0)
	f.quotaUsage(0, 0, 0)
	// 模拟补款后恢复器重试；零初始预留快照保持原样。
	_, err = integrationDB.ExecContext(context.Background(), "UPDATE users SET balance=10 WHERE id=$1", f.userID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(context.Background(), "UPDATE video_tasks SET billing_status='reconciliation' WHERE id=$1", task.ID)
	require.NoError(t, err)
	result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.Equal(t, 3.0, result.ActualAmountUSD)
	f.money(7, 0, 3, 3)
	f.quotaUsage(3, 3, 3)
	// 即使命中现有幂等键，仍校验原任务付款人和账号，不能向错误归属返回账务结果。
	for _, change := range []func(*service.BatchImageBalanceHoldCommand){
		func(c *service.BatchImageBalanceHoldCommand) { c.UserID++ },
		func(c *service.BatchImageBalanceHoldCommand) { c.VideoAccountID++ },
	} {
		forged := cmd
		change(&forged)
		_, err = f.repo.CaptureBatchImageBalance(context.Background(), &forged)
		require.ErrorIs(t, err, service.ErrVideoTaskConflict)
	}
	f.money(7, 0, 3, 3)
}

func TestVideoTaskDeferredBillingCompletionWindowsAndExactLimit(t *testing.T) {
	f := newVideoBillingFixture(t)
	old := time.Now().UTC().Add(-8 * 24 * time.Hour)
	task := f.reserve(newDeferredVideoTestTask(t, f, func(r *service.VideoTaskRecord) { r.Hold.ReservedAt = old }))
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE api_keys SET quota=3,rate_limit_5h=3,rate_limit_1d=3,rate_limit_7d=3,
 usage_5h=3,usage_1d=3,usage_7d=3,window_5h_start=$2,window_1d_start=$2,window_7d_start=$2 WHERE id=$1`, f.keyID, old)
	require.NoError(t, err)
	cmd := f.command(task, "completed", 3)
	result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	f.money(997, 0, 3, 3)
	var status string
	var usage5h, usage1d, usage7d float64
	var start5h, start1d, start7d time.Time
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT status,usage_5h,usage_1d,usage_7d,window_5h_start,window_1d_start,window_7d_start FROM api_keys WHERE id=$1`, f.keyID).
		Scan(&status, &usage5h, &usage1d, &usage7d, &start5h, &start1d, &start7d))
	require.Equal(t, service.StatusAPIKeyQuotaExhausted, status)
	require.Equal(t, [3]float64{3, 3, 3}, [3]float64{usage5h, usage1d, usage7d})
	for _, start := range []time.Time{start5h, start1d, start7d} {
		require.True(t, start.After(old.Add(7*24*time.Hour)), "按真实扣款时刻进入新窗口")
	}
	// 达到精确上限改变 Key 状态后，已提交捕获仍必须返回真实结果，不能再次做额度准入。
	result, err = f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
	require.NoError(t, err)
	require.False(t, result.Applied)
	require.Equal(t, 3.0, result.ActualAmountUSD)
	f.money(997, 0, 3, 3)
}

func TestVideoTaskDeferredBillingQuotaFailureRollsBackSubscriptions(t *testing.T) {
	cases := []struct {
		name, update, restore string
		want                  error
	}{
		{"key_total", "UPDATE api_keys SET quota=1 WHERE id=$1", "UPDATE api_keys SET quota=1000 WHERE id=$1", service.ErrAPIKeyQuotaExhausted},
		{"key_5h", "UPDATE api_keys SET rate_limit_5h=1 WHERE id=$1", "UPDATE api_keys SET rate_limit_5h=1000 WHERE id=$1", service.ErrAPIKeyRateLimit5hExceeded},
		{"key_1d", "UPDATE api_keys SET rate_limit_1d=1 WHERE id=$1", "UPDATE api_keys SET rate_limit_1d=1000 WHERE id=$1", service.ErrAPIKeyRateLimit1dExceeded},
		{"key_7d", "UPDATE api_keys SET rate_limit_7d=1 WHERE id=$1", "UPDATE api_keys SET rate_limit_7d=1000 WHERE id=$1", service.ErrAPIKeyRateLimit7dExceeded},
		{"platform_daily", "UPDATE user_platform_quotas SET daily_limit_usd=0.5 WHERE user_id=$1", "UPDATE user_platform_quotas SET daily_limit_usd=100 WHERE user_id=$1", service.ErrUserPlatformDailyQuotaExhausted},
		{"platform_weekly", "UPDATE user_platform_quotas SET weekly_limit_usd=0.5 WHERE user_id=$1", "UPDATE user_platform_quotas SET weekly_limit_usd=100 WHERE user_id=$1", service.ErrUserPlatformWeeklyQuotaExhausted},
		{"platform_monthly", "UPDATE user_platform_quotas SET monthly_limit_usd=0.5 WHERE user_id=$1", "UPDATE user_platform_quotas SET monthly_limit_usd=100 WHERE user_id=$1", service.ErrUserPlatformMonthlyQuotaExhausted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(100)
			sub := videoResetTestSubscription(t, f)
			_, err := integrationDB.ExecContext(context.Background(), "UPDATE user_subscriptions SET daily_limit_usd=1 WHERE id=$1", sub.ID)
			require.NoError(t, err)
			task := f.reserve(newDeferredVideoTestTask(t, f, nil))
			id := f.keyID
			if tc.name[:3] != "key" {
				id = f.userID
			}
			_, err = integrationDB.ExecContext(context.Background(), tc.update, id)
			require.NoError(t, err)
			cmd := f.command(task, "completed", 2)
			_, err = f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.ErrorIs(t, err, tc.want)
			assertDeferredVideoUncharged(t, f, task)
			f.money(1000, 0, 0, 0)
			f.quotaUsage(0, 0, 0)
			var daily, weekly, monthly float64
			require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE id=$1", sub.ID).Scan(&daily, &weekly, &monthly))
			require.Equal(t, [3]float64{}, [3]float64{daily, weekly, monthly})
			_, err = integrationDB.ExecContext(context.Background(), tc.restore, id)
			require.NoError(t, err)
			result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.NoError(t, err)
			require.Equal(t, 1.0, result.SubscriptionAmountUSD)
			require.Equal(t, 1.0, result.BalanceAmountUSD)
			f.money(999, 0, 2, 2)
			f.quotaUsage(1, 1, 1)
		})
	}
}

func TestVideoTaskDeferredBillingPreferredSubscriptionAndCompletionRate(t *testing.T) {
	for _, independent := range []bool{false, true} {
		t.Run(map[bool]string{false: "plan_rate_at_completion", true: "independent_rate_snapshot"}[independent], func(t *testing.T) {
			f := newVideoBillingFixture(t)
			sub := videoResetTestSubscription(t, f)
			task := f.reserve(newDeferredVideoTestTask(t, f, func(r *service.VideoTaskRecord) {
				r.Hold.APIKeyBillingMode = service.APIKeyBillingModeSubscription
				r.Hold.PreferredSubscriptionID = &sub.ID
				r.Hold.SubscriptionRateMultiplier = 2
				r.Hold.DisablePlanGroupRateMultiplier = independent
			}))
			_, err := integrationDB.ExecContext(context.Background(), "UPDATE subscription_plan_groups SET rate_multiplier=0.5 WHERE plan_id=$1 AND group_id=$2", sub.PlanID, f.groupID)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(context.Background(), "UPDATE user_subscriptions SET daily_limit_usd=0.5 WHERE id=$1", sub.ID)
			require.NoError(t, err)
			cmd := f.command(task, "completed", 2)
			_, err = f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.ErrorIs(t, err, service.ErrPreferredSubscriptionInsufficient)
			assertDeferredVideoUncharged(t, f, task)
			f.money(1000, 0, 0, 0)
			_, err = integrationDB.ExecContext(context.Background(), "UPDATE user_subscriptions SET daily_limit_usd=100 WHERE id=$1", sub.ID)
			require.NoError(t, err)
			result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.NoError(t, err)
			want := 1.0
			if independent {
				want = 4
			}
			require.Equal(t, want, result.SubscriptionAmountUSD)
			require.Zero(t, result.BalanceAmountUSD)
			f.money(1000, 0, want, 2)
		})
	}
}

func TestVideoTaskDeferredBillingTeamLimitAndRollback(t *testing.T) {
	f := newVideoBillingFixture(t)
	f.quota(100)
	ctx := context.Background()
	teamRepo := NewTeamRepository(integrationDB)
	member := mustCreateUser(t, integrationEntClient, &service.User{Email: uniqueTeamTestEmail("video-deferred-member")})
	team, err := teamRepo.Create(ctx, "无预算视频额度团队", f.userID, 5)
	require.NoError(t, err)
	token := uuid.NewString()
	_, err = teamRepo.CreateInvitation(ctx, team.Team.ID, f.userID, member.Email, token, time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = teamRepo.ResolveInvitation(ctx, token, member.ID, member.Email, "accepted", time.Now())
	require.NoError(t, err)
	require.NoError(t, teamRepo.UpdateMemberLimits(ctx, team.Team.ID, member.ID, 1, 10, 30))
	teamID := team.Team.ID
	key := mustCreateApiKey(t, integrationEntClient, &service.APIKey{UserID: member.ID, TeamID: &teamID, Key: "sk-video-deferred-" + uuid.NewString(), Name: "video-deferred", Quota: 1000})
	f.keyID = key.ID
	task := f.reserve(newDeferredVideoTestTask(t, f, func(r *service.VideoTaskRecord) {
		r.UserID, r.Hold.ActorUserID, r.Hold.TeamID = member.ID, member.ID, &teamID
	}))
	cmd := f.command(task, "completed", 2)
	_, err = f.repo.CaptureBatchImageBalance(ctx, &cmd)
	require.ErrorIs(t, err, service.ErrTeamMemberDailyExceeded)
	assertDeferredVideoUncharged(t, f, task)
	f.money(1000, 0, 0, 0)
	f.quotaUsage(0, 0, 0)
	var usage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT daily_usage_usd FROM team_memberships WHERE team_id=$1 AND user_id=$2 AND left_at IS NULL", teamID, member.ID).Scan(&usage))
	require.Zero(t, usage)
	require.NoError(t, teamRepo.UpdateMemberLimits(ctx, teamID, member.ID, 10, 10, 30))
	_, err = f.repo.CaptureBatchImageBalance(ctx, &cmd)
	require.NoError(t, err)
	f.money(998, 0, 2, 2)
	f.quotaUsage(2, 2, 2)
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT daily_usage_usd FROM team_memberships WHERE team_id=$1 AND user_id=$2 AND left_at IS NULL", teamID, member.ID).Scan(&usage))
	require.Equal(t, 2.0, usage)
}

func TestVideoTaskDeferredBillingZeroUsageReleaseAndForgedMode(t *testing.T) {
	for _, status := range []string{"completed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(0)
			task := f.reserve(newDeferredVideoTestTask(t, f, nil))
			cmd := f.command(task, status, 0)
			var result *service.BatchImageBalanceHoldResult
			var err error
			if status == "completed" {
				result, err = f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			} else {
				result, err = f.repo.ReleaseBatchImageBalance(context.Background(), &cmd)
			}
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.Zero(t, result.ActualAmountUSD)
			f.money(1000, 0, 0, 0)
			f.quotaUsage(0, 0, 0)
		})
	}
	f := newVideoBillingFixture(t)
	task := f.reserve(f.task(1, 1, 1))
	cmd := f.command(task, "completed", 2)
	cmd.VideoDeferredBilling = true
	_, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
	require.ErrorIs(t, err, service.ErrVideoTaskConflict)
	f.money(999, 1, 1, 0)
	// 标记不允许进入旧图片命令，也不能用于按秒任务。
	cmd.VideoEntity = false
	_, err = f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
	require.ErrorIs(t, err, service.ErrVideoTaskConflict)
	invalid := newDeferredVideoTestTask(t, f, func(r *service.VideoTaskRecord) { r.Quote.Mode = service.BillingModeVideo })
	_, err = f.repo.ReserveBatchImageBalance(context.Background(), &invalid.Hold)
	require.ErrorIs(t, err, service.ErrVideoTaskConflict)
}
