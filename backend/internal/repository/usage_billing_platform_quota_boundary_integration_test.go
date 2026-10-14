//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 每个平台必须经过真实资金入口；Video 已有独立预留账本，通用 Apply 不能重复累计。
func TestUsageBillingPlatformQuotaAllAllowedPlatforms(t *testing.T) {
	for _, platform := range service.AllowedQuotaPlatforms {
		t.Run(platform, func(t *testing.T) {
			f := newLegacyVideoFixture(t)
			ctx := context.Background()
			insertSettlementPlatformQuota(t, f.user, platform, float64Ptr(100))
			_, err := integrationDB.ExecContext(ctx, `UPDATE groups SET platform=$2 WHERE id=$1`, f.group, platform)
			require.NoError(t, err)
			cmd := settlementQuotaCommand(f, "quota-all-platforms-"+uuid.NewString())
			cmd.QuotaPlatform = platform
			result, err := NewUsageBillingRepository(integrationEntClient, integrationDB).Apply(ctx, &cmd)
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.InDelta(t, .25, result.BalanceAmountUSD, 1e-9)
			want := .25
			if platform == service.PlatformVideo {
				want = 0
			}
			assertSettlementPlatformQuota(t, f.user, platform, want)
			billingFocusAssertDecimal(t, "99.75", `SELECT balance::text FROM users WHERE id=$1`, f.user)
			result, err = NewUsageBillingRepository(integrationEntClient, integrationDB).Apply(ctx, &cmd)
			require.NoError(t, err)
			require.False(t, result.Applied)
			assertSettlementPlatformQuota(t, f.user, platform, want)
			billingFocusAssertDecimal(t, "99.75", `SELECT balance::text FROM users WHERE id=$1`, f.user)
		})
	}
}

// quotaBoundaryWaiter 等待数据库确认真实锁等待，不依赖 goroutine 调度或固定睡眠。
func quotaBoundaryWaiter(t *testing.T, ctx context.Context, blockerPID int, queryPart string) int {
	t.Helper()
	var waiterPID int
	require.Eventually(t, func() bool {
		return integrationDB.QueryRowContext(ctx, `SELECT pid FROM pg_stat_activity
 WHERE wait_event_type='Lock' AND $1::int=ANY(pg_blocking_pids(pid))
 AND strpos(query,$2)>0 ORDER BY pid LIMIT 1`, blockerPID, queryPart).Scan(&waiterPID) == nil
	}, 5*time.Second, 10*time.Millisecond, "未进入预期的数据库锁等待")
	return waiterPID
}

// quotaBoundaryAdmin 使用真实管理仓储修改；传入事务时由测试控制其可见顺序。
func quotaBoundaryAdmin(ctx context.Context, repo UserPlatformQuotaRepository, userID int64, operation string) error {
	if operation == "disable" {
		return repo.UpsertForUser(ctx, userID, nil)
	}
	for _, window := range []string{"daily", "weekly", "monthly"} {
		if err := repo.ResetExpiredWindow(ctx, userID, service.PlatformGrok, window, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

type quotaBoundaryApplyOutcome struct {
	result *service.UsageBillingApplyResult
	err    error
}

// 管理修改与已完成上游的扣费必须串行；重置不复活旧消费，取消限制不删除既有用量。
func TestUsageBillingPlatformQuotaAdminAndSettlementOrdering(t *testing.T) {
	for _, operation := range []string{"reset", "disable"} {
		for _, order := range []string{"admin_first", "settlement_first"} {
			t.Run(operation+"/"+order, func(t *testing.T) {
				f := newLegacyVideoFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				insertSettlementPlatformQuota(t, f.user, service.PlatformGrok, float64Ptr(100))
				billing := NewUsageBillingRepository(integrationEntClient, integrationDB)
				quota := NewUserPlatformQuotaRepository(integrationEntClient)
				baseline := settlementQuotaCommand(f, "quota-boundary-baseline-"+uuid.NewString())
				_, err := billing.Apply(ctx, &baseline)
				require.NoError(t, err)
				assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, .25)
				cmd := settlementQuotaCommand(f, "quota-boundary-"+uuid.NewString())
				applied := make(chan quotaBoundaryApplyOutcome, 1)
				apply := func() {
					result, err := NewUsageBillingRepository(integrationEntClient, integrationDB).Apply(ctx, &cmd)
					applied <- quotaBoundaryApplyOutcome{result: result, err: err}
				}

				if order == "admin_first" {
					// 先在真实管理事务中修改并持有额度行，再让扣费等到管理提交。
					adminTx, err := integrationEntClient.Tx(ctx)
					require.NoError(t, err)
					defer adminTx.Rollback()
					adminCtx := dbent.NewTxContext(ctx, adminTx)
					require.NoError(t, quotaBoundaryAdmin(adminCtx, quota, f.user, operation))
					rows, err := adminTx.Client().QueryContext(ctx, `SELECT pg_backend_pid()`)
					require.NoError(t, err)
					require.True(t, rows.Next())
					var adminPID int
					require.NoError(t, rows.Scan(&adminPID))
					require.NoError(t, rows.Close())
					go apply()
					quotaBoundaryWaiter(t, ctx, adminPID, "user_platform_quotas")
					// 未提交的扣款不可对外可见，即使钱款更新已在同一事务执行。
					billingFocusAssertDecimal(t, "99.75", `SELECT balance::text FROM users WHERE id=$1`, f.user)
					require.NoError(t, adminTx.Commit())
				} else {
					// 锁住最后写入的原任务回执，使真实 Apply 在已更新额度后暂不能提交。
					task := f.task()
					require.NoError(t, f.repo.Save(ctx, task))
					cmd.RequestID, cmd.LegacyVideoTaskID = task.RequestID, task.ID
					blocker, err := integrationDB.BeginTx(ctx, nil)
					require.NoError(t, err)
					defer blocker.Rollback()
					var blockerPID int
					var taskID int64
					require.NoError(t, blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID))
					require.NoError(t, blocker.QueryRowContext(ctx, `SELECT id FROM legacy_video_tasks WHERE id=$1 FOR UPDATE`, task.ID).Scan(&taskID))
					go apply()
					applyPID := quotaBoundaryWaiter(t, ctx, blockerPID, "UPDATE legacy_video_tasks SET billing_result=")
					adminDone := make(chan error, 1)
					go func() { adminDone <- quotaBoundaryAdmin(ctx, quota, f.user, operation) }()
					quotaBoundaryWaiter(t, ctx, applyPID, "user_platform_quotas")
					assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, .25)
					billingFocusAssertDecimal(t, "99.75", `SELECT balance::text FROM users WHERE id=$1`, f.user)
					require.NoError(t, blocker.Commit())
					require.NoError(t, <-adminDone)
				}
				outcome := <-applied
				require.NoError(t, outcome.err)
				require.NotNil(t, outcome.result)
				require.True(t, outcome.result.Applied)
				want := .25
				if order == "settlement_first" {
					if operation == "reset" {
						want = 0
					} else {
						want = .5
					}
				}
				assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, want)
				billingFocusAssertDecimal(t, "99.5", `SELECT balance::text FROM users WHERE id=$1`, f.user)
				rec, err := quota.GetByUserPlatform(ctx, f.user, service.PlatformGrok)
				require.NoError(t, err)
				require.NotNil(t, rec)
				if operation == "disable" {
					require.False(t, rec.HasAnyLimit())
				} else {
					var daily, weekly, monthly int64
					require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_reset_generation,weekly_reset_generation,monthly_reset_generation FROM user_platform_quotas WHERE user_id=$1 AND platform=$2`, f.user, service.PlatformGrok).Scan(&daily, &weekly, &monthly))
					require.Equal(t, []int64{1, 1, 1}, []int64{daily, weekly, monthly})
				}
				// 再投递这次资金命令，不能把管理员清零的用量重新加回。
				replayed, err := billing.Apply(ctx, &cmd)
				require.NoError(t, err)
				require.False(t, replayed.Applied)
				assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, want)
				billingFocusAssertDecimal(t, "99.5", `SELECT balance::text FROM users WHERE id=$1`, f.user)
			})
		}
	}
}
