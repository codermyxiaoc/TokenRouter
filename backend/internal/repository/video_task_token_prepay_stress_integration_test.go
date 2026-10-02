//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 通过有界连接池运行真实并发事务，避免把 PostgreSQL 默认连接上限当成账务失败。
func videoStressConnectionPool(t *testing.T) {
	t.Helper()
	previous := integrationDB.Stats().MaxOpenConnections
	integrationDB.SetMaxOpenConns(64)
	t.Cleanup(func() { integrationDB.SetMaxOpenConns(previous) })
}

// videoStressMember 仅创建本轮隔离测试所需的团队成员，不使用真实业务账户。
func videoStressMember(t *testing.T, f *videoBillingFixture) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	repo := NewTeamRepository(integrationDB)
	member := mustCreateUser(t, integrationEntClient, &service.User{Email: uniqueTeamTestEmail("video-stress")})
	team, err := repo.Create(ctx, "视频并发额度测试", f.userID, 5)
	require.NoError(t, err)
	token := uuid.NewString()
	_, err = repo.CreateInvitation(ctx, team.Team.ID, f.userID, member.Email, token, time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = repo.ResolveInvitation(ctx, token, member.ID, member.Email, "accepted", time.Now())
	require.NoError(t, err)
	require.NoError(t, repo.UpdateMemberLimits(ctx, team.Team.ID, member.ID, 1000, 1000, 1000))
	return team.Team.ID, member.ID
}

// 六十份预扣共 162，只有 10 可补扣；每份实际 4.70 比预扣 2.70 多 2，严格只允许五份成功。
func TestVideoTaskTokenPrepayStressSixtyLimitedTopUps(t *testing.T) {
	videoStressConnectionPool(t)
	for _, tc := range []struct {
		name, restrict, restore string
		want                    error
	}{
		{"wallet", `UPDATE users SET balance=10 WHERE id=$1`, `UPDATE users SET balance=10 WHERE id=$1`, service.ErrBatchImageInsufficientBalance},
		{"key_total", `UPDATE api_keys SET quota=172 WHERE id=$1`, `UPDATE api_keys SET quota=1000 WHERE id=$1`, service.ErrAPIKeyQuotaExhausted},
		{"key_5h", `UPDATE api_keys SET rate_limit_5h=172 WHERE id=$1`, `UPDATE api_keys SET rate_limit_5h=1000 WHERE id=$1`, service.ErrAPIKeyRateLimit5hExceeded},
		{"key_1d", `UPDATE api_keys SET rate_limit_1d=172 WHERE id=$1`, `UPDATE api_keys SET rate_limit_1d=1000 WHERE id=$1`, service.ErrAPIKeyRateLimit1dExceeded},
		{"key_7d", `UPDATE api_keys SET rate_limit_7d=172 WHERE id=$1`, `UPDATE api_keys SET rate_limit_7d=1000 WHERE id=$1`, service.ErrAPIKeyRateLimit7dExceeded},
		{"platform_daily", `UPDATE user_platform_quotas SET daily_limit_usd=172 WHERE user_id=$1`, `UPDATE user_platform_quotas SET daily_limit_usd=1000 WHERE user_id=$1`, service.ErrUserPlatformDailyQuotaExhausted},
		{"platform_weekly", `UPDATE user_platform_quotas SET weekly_limit_usd=172 WHERE user_id=$1`, `UPDATE user_platform_quotas SET weekly_limit_usd=1000 WHERE user_id=$1`, service.ErrUserPlatformWeeklyQuotaExhausted},
		{"platform_monthly", `UPDATE user_platform_quotas SET monthly_limit_usd=172 WHERE user_id=$1`, `UPDATE user_platform_quotas SET monthly_limit_usd=1000 WHERE user_id=$1`, service.ErrUserPlatformMonthlyQuotaExhausted},
		{"team", `UPDATE team_memberships SET daily_limit_usd=172 WHERE user_id=$1`, `UPDATE team_memberships SET daily_limit_usd=1000 WHERE user_id=$1`, service.ErrTeamMemberDailyExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(1000)
			var teamID, memberID int64
			if tc.name == "team" {
				teamID, memberID = videoStressMember(t, f)
			}
			const count = 60
			commands := make([]service.BatchImageBalanceHoldCommand, count)
			for i := range commands {
				task := tokenPrepayTestTask(t, f, 1, .3)
				if teamID != 0 {
					task.Hold.TeamID, task.Hold.ActorUserID = &teamID, memberID
					require.NoError(t, f.tasks.Save(context.Background(), task, false))
				}
				task = f.reserve(task)
				commands[i] = f.command(task, "completed", 4.4)
				commands[i].VideoActualFixedAmountUSD = .3
			}
			target := f.userID
			if tc.name[:3] == "key" {
				target = f.keyID
			} else if tc.name == "team" {
				target = memberID
			}
			_, err := integrationDB.ExecContext(context.Background(), tc.restrict, target)
			require.NoError(t, err)
			type outcome struct {
				index   int
				applied bool
				err     error
			}
			start, out := make(chan struct{}), make(chan outcome, count)
			var wg sync.WaitGroup
			for i, cmd := range commands {
				wg.Add(1)
				go func(index int, cmd service.BatchImageBalanceHoldCommand) {
					defer wg.Done()
					<-start
					result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
					applied := err == nil && result.Applied
					if err == nil {
						repeat, repeatErr := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
						if repeatErr != nil || repeat.Applied || repeat.ActualAmountUSD != result.ActualAmountUSD {
							err = fmt.Errorf("预扣并发重放不一致: %v", repeatErr)
						}
					}
					out <- outcome{index, applied, err}
				}(i, cmd)
			}
			close(start)
			wg.Wait()
			close(out)
			var succeeded, rejected int
			var retryIndex int
			for result := range out {
				if result.err == nil {
					require.True(t, result.applied)
					succeeded++
				} else {
					require.ErrorIs(t, result.err, tc.want)
					rejected++
					retryIndex = result.index
				}
			}
			require.Equal(t, 5, succeeded)
			require.Equal(t, 55, rejected)
			balance := 828.0
			if tc.name == "wallet" {
				balance = 0
			}
			f.money(balance, 148.5, 172, 22)
			f.quotaUsage(172, 172, 172)
			var settled, reserved, captures int
			require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT count(*) FILTER(WHERE billing_status='settled'),count(*) FILTER(WHERE billing_status='reserved' AND allowance_reserved AND billing_result IS NULL AND hold_amount=2.7 AND balance_hold_amount=2.7) FROM video_tasks WHERE api_key_id=$1`, f.keyID).Scan(&settled, &reserved))
			require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT count(*) FROM usage_billing_dedup d JOIN video_tasks t ON t.api_key_id=d.api_key_id AND d.request_id='video_capture:'||t.id WHERE t.api_key_id=$1`, f.keyID).Scan(&captures))
			require.Equal(t, 5, settled)
			require.Equal(t, 55, reserved)
			require.Equal(t, 5, captures)
			if memberID != 0 {
				var used float64
				require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT daily_usage_usd FROM team_memberships WHERE team_id=$1 AND user_id=$2`, teamID, memberID).Scan(&used))
				require.InDelta(t, 172, used, 1e-8)
			}
			// 一份失败任务在补足资金或提高额度后恢复，证明失败没有遗留捕获去重键。
			_, err = integrationDB.ExecContext(context.Background(), tc.restore, target)
			require.NoError(t, err)
			retry := commands[retryIndex]
			result, err := f.repo.CaptureBatchImageBalance(context.Background(), &retry)
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.InDelta(t, 4.7, result.ActualAmountUSD, 1e-8)
			balance -= 2
			if tc.name == "wallet" {
				balance = 8
			}
			f.money(balance, 145.8, 174, 26.4)
			t.Logf("60 并发：成功 %d，限额回滚 %d；冻结 148.50、额度 172；补足后第 6 单成功，冻结 145.80、额度 174", succeeded, rejected)
		})
	}
}

// 对同一预扣任务同时发起六十次捕获与六十次释放，只有合法终态的单一资金操作可提交。
func TestVideoTaskTokenPrepayStressCaptureReleaseRace(t *testing.T) {
	videoStressConnectionPool(t)
	for _, terminal := range []string{"completed", "cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(100)
			task := f.reserve(tokenPrepayTestTask(t, f, 2, .3))
			capture := f.command(task, terminal, 3)
			capture.RequestID, capture.VideoActualFixedAmountUSD = "video_capture:"+task.ID, .3
			release := capture
			release.RequestID, release.ActualBaseAmountUSD, release.VideoActualFixedAmountUSD = "video_release:"+task.ID, 0, 0
			type outcome struct {
				capture, applied bool
				err              error
			}
			out, start := make(chan outcome, 120), make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < 60; i++ {
				for _, isCapture := range []bool{true, false} {
					wg.Add(1)
					go func(isCapture bool) {
						defer wg.Done()
						<-start
						cmd, apply := release, f.repo.ReleaseBatchImageBalance
						if isCapture {
							cmd, apply = capture, f.repo.CaptureBatchImageBalance
						}
						result, err := apply(context.Background(), &cmd)
						out <- outcome{isCapture, err == nil && result.Applied, err}
					}(isCapture)
				}
			}
			close(start)
			wg.Wait()
			close(out)
			var applied, replayed, conflicts int
			for result := range out {
				if result.capture == (terminal == "completed") {
					require.NoError(t, result.err)
					if result.applied {
						applied++
					} else {
						replayed++
					}
				} else {
					require.ErrorIs(t, result.err, service.ErrVideoTaskConflict)
					conflicts++
				}
			}
			require.Equal(t, 1, applied)
			require.Equal(t, 59, replayed)
			require.Equal(t, 60, conflicts)
			if terminal == "completed" {
				f.money(993.7, 0, 6.3, 3)
				f.quotaUsage(6.3, 6.3, 6.3)
			} else {
				f.money(1000, 0, 0, 0)
				f.quotaUsage(0, 0, 0)
			}
			t.Logf("120 并发：提交 %d，合法重放 %d，反向操作拒绝 %d", applied, replayed, conflicts)
		})
	}
}

// 过期指定套餐不能消耗旧额度或改扣余额，恢复有效期后仍可结算；独立倍率不受套餐改价影响。
func TestVideoTaskTokenPrepayStressExpiredSubscriptionAndIndependentRate(t *testing.T) {
	for _, independent := range []bool{false, true} {
		t.Run(fmt.Sprintf("independent_%t", independent), func(t *testing.T) {
			f := newVideoBillingFixture(t)
			sub := fixedVideoSubscription(t, f, 100, .5)
			task := tokenPrepayTestTask(t, f, 2, .3)
			task.Hold.APIKeyBillingMode, task.Hold.PreferredSubscriptionID = service.APIKeyBillingModeSubscription, &sub.ID
			task.Hold.DisablePlanGroupRateMultiplier = independent
			require.NoError(t, f.tasks.Save(context.Background(), task, false))
			task = f.reserve(task)
			_, err := integrationDB.ExecContext(context.Background(), `UPDATE user_subscriptions SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, sub.ID)
			require.NoError(t, err)
			cmd := f.command(task, "completed", 4)
			cmd.VideoActualFixedAmountUSD = .3
			_, err = f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.ErrorIs(t, err, service.ErrPreferredSubscriptionInsufficient)
			fixedVideoAssertSubscription(t, sub.ID, 2.7)
			f.money(1000, 0, 2.7, 0)
			// 日/周/月旧窗口全部过期，由真实分配过程自然重置；旧预扣退款不带入新窗口。
			_, err = integrationDB.ExecContext(context.Background(), `UPDATE user_subscriptions SET starts_at=NOW()-INTERVAL '40 days',expires_at=NOW()+INTERVAL '40 days',daily_window_start=NOW()-INTERVAL '40 days',weekly_window_start=NOW()-INTERVAL '40 days',monthly_window_start=NOW()-INTERVAL '40 days' WHERE id=$1`, sub.ID)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(context.Background(), `UPDATE subscription_plan_groups SET rate_multiplier=0.25 WHERE plan_id=$1`, sub.PlanID)
			require.NoError(t, err)
			result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.NoError(t, err)
			want := 1.3
			if independent {
				want = 8.3
			}
			require.InDelta(t, want, result.SubscriptionAmountUSD, 1e-8)
			require.Zero(t, result.BalanceAmountUSD)
			fixedVideoAssertSubscription(t, sub.ID, want)
			f.money(1000, 0, want, 4)
			fresh, err := f.tasks.Get(context.Background(), task.ID)
			require.NoError(t, err)
			require.Equal(t, "settled", fresh.BillingStatus)
			require.False(t, fresh.Hold.AllowanceReserved)
			t.Logf("过期套餐拒绝且保留 2.70 预扣；恢复并跨三个窗口后实收 %.2f，余额不变", want)
		})
	}
}
