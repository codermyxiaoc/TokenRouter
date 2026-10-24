//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 恢复测试只使用隔离数据库与人工构造证据，绝不向外部检测站发送凭据。
func chanshuiRecoveryFixture(t *testing.T) (service.ChanshuiRepository, *service.ChanshuiConfig, *service.Account) {
	t.Helper()
	r, c, a := chanshuiFixture(t)
	c.AutoRecover, c.ScheduleEnabled = true, true
	c.MinimumScore = chanshuiScore(70)
	require.NoError(t, r.SaveConfig(context.Background(), c))
	return r, c, a
}

// 显式重复停用也撤销自动恢复，后台同值写入则保持资格；修改与代次同事务回滚。
func TestChanshuiRecoveryPostgresExplicitManualNoopState(t *testing.T) {
	for _, mode := range []string{"single", "bulk", "schedulable", "runtime", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			r, c, a := chanshuiRecoveryFixture(t)
			chanshuiSuspendAccount(t, r, c, a)
			accountRepo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
			ctx := context.Background()
			manualCtx := service.WithChanshuiManualAccountState(ctx)
			before, err := r.GetAccountRevision(ctx, a.ID)
			require.NoError(t, err)
			status, schedulable := service.StatusDisabled, false
			switch mode {
			case "single":
				current, e := accountRepo.GetByID(ctx, a.ID)
				require.NoError(t, e)
				require.NoError(t, accountRepo.Update(manualCtx, current))
			case "bulk":
				_, err = accountRepo.BulkUpdate(manualCtx, []int64{a.ID}, service.AccountBulkUpdate{Status: &status, Schedulable: &schedulable})
				require.NoError(t, err)
			case "schedulable":
				require.NoError(t, accountRepo.SetSchedulable(manualCtx, a.ID, false))
			case "runtime":
				require.NoError(t, accountRepo.SetSchedulable(ctx, a.ID, false))
			case "rollback":
				tx, e := integrationEntClient.Tx(ctx)
				require.NoError(t, e)
				_, e = accountRepo.BulkUpdate(dbent.NewTxContext(manualCtx, tx), []int64{a.ID}, service.AccountBulkUpdate{Status: &status})
				require.NoError(t, e)
				require.NoError(t, tx.Rollback())
			}
			after, err := r.GetAccountRevision(ctx, a.ID)
			require.NoError(t, err)
			got, err := r.GetConfig(ctx, c.ID)
			require.NoError(t, err)
			if mode == "runtime" || mode == "rollback" {
				require.Equal(t, before, after)
				require.True(t, got.RecoveryPending)
			} else {
				require.Greater(t, after, before)
				require.False(t, got.RecoveryPending)
			}
		})
	}
}

func TestChanshuiRecoveryPostgresConcurrentManualStopWins(t *testing.T) {
	r, c, a := chanshuiRecoveryFixture(t)
	chanshuiSuspendAccount(t, r, c, a)
	pass := chanshuiPassingRun(t, r, c, a)
	accountRepo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	status, schedulable := service.StatusDisabled, false
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := r.FinalizeRun(context.Background(), pass, &service.ChanshuiDisableDecision{AccountUpdatedAt: a.UpdatedAt, Recover: true})
		require.NoError(t, err)
	}()
	go func() {
		defer wg.Done()
		_, err := accountRepo.BulkUpdate(service.WithChanshuiManualAccountState(context.Background()), []int64{a.ID}, service.AccountBulkUpdate{Status: &status, Schedulable: &schedulable})
		require.NoError(t, err)
	}()
	wg.Wait()
	var actualStatus string
	var actualSchedulable bool
	require.NoError(t, integrationDB.QueryRow(`SELECT status,schedulable FROM accounts WHERE id=$1`, a.ID).Scan(&actualStatus, &actualSchedulable))
	require.Equal(t, service.StatusDisabled, actualStatus)
	require.False(t, actualSchedulable)
	got, err := r.GetConfig(context.Background(), c.ID)
	require.NoError(t, err)
	require.False(t, got.RecoveryPending)
}

func chanshuiSuspendAccount(t *testing.T, r service.ChanshuiRepository, c *service.ChanshuiConfig, a *service.Account) *service.ChanshuiRun {
	t.Helper()
	run := claimChanshui(t, r, c, a)
	run.Status, run.Verdict = "completed", "adulterated"
	ok, err := r.FinalizeRun(context.Background(), run, &service.ChanshuiDisableDecision{AccountUpdatedAt: a.UpdatedAt})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "disabled", run.ActionStatus)
	require.NoError(t, integrationDB.QueryRow(`SELECT updated_at FROM accounts WHERE id=$1`, a.ID).Scan(&a.UpdatedAt))
	return run
}

func chanshuiPassingRun(t *testing.T, r service.ChanshuiRepository, c *service.ChanshuiConfig, a *service.Account) *service.ChanshuiRun {
	t.Helper()
	run := claimChanshui(t, r, c, a)
	run.Status, run.Verdict, run.ScoreComplete, run.Score = "completed", "clean", true, chanshuiScore(90)
	return run
}

func TestChanshuiRecoveryPostgresRoundTripAndReplay(t *testing.T) {
	r, c, a := chanshuiRecoveryFixture(t)
	ctx := context.Background()
	chanshuiSuspendAccount(t, r, c, a)
	current, err := r.GetConfig(ctx, c.ID)
	require.NoError(t, err)
	require.True(t, current.RecoveryPending)
	// 模拟停用事件已被调度消费者读取，恢复必须发出新的通知。
	_, err = integrationDB.Exec(`UPDATE scheduler_outbox SET dedup_key=NULL WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	pass := chanshuiPassingRun(t, r, c, a)
	ok, err := r.FinalizeRun(ctx, pass, &service.ChanshuiDisableDecision{AccountUpdatedAt: a.UpdatedAt, Recover: true})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "recovered", pass.ActionStatus)
	var status string
	var schedulable bool
	require.NoError(t, integrationDB.QueryRow(`SELECT status,schedulable FROM accounts WHERE id=$1`, a.ID).Scan(&status, &schedulable))
	require.Equal(t, "active", status)
	require.True(t, schedulable)
	current, err = r.GetConfig(ctx, c.ID)
	require.NoError(t, err)
	require.False(t, current.RecoveryPending)
	var outbox int
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&outbox))
	require.Equal(t, 2, outbox)
	_, err = integrationDB.Exec(`UPDATE accounts SET status='disabled',schedulable=false WHERE id=$1`, a.ID)
	require.NoError(t, err)
	ok, err = r.FinalizeRun(ctx, pass, &service.ChanshuiDisableDecision{AccountUpdatedAt: a.UpdatedAt, Recover: true})
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, integrationDB.QueryRow(`SELECT status FROM accounts WHERE id=$1`, a.ID).Scan(&status))
	require.Equal(t, "disabled", status)
}

func TestChanshuiRecoveryPostgresGuards(t *testing.T) {
	for _, kind := range []string{"partial", "unknown", "low_score", "no_score", "error", "switch_off", "credentials", "manual_aba", "expired", "rule_edit", "old_result", "no_owner"} {
		t.Run(kind, func(t *testing.T) {
			r, c, a := chanshuiRecoveryFixture(t)
			ctx := context.Background()
			chanshuiSuspendAccount(t, r, c, a)
			pass := chanshuiPassingRun(t, r, c, a)
			switch kind {
			case "partial":
				pass.ScoreComplete = false
			case "unknown":
				pass.Verdict = "unknown"
			case "low_score":
				pass.Score = chanshuiScore(1)
			case "no_score":
				pass.Score = nil
			case "error":
				pass.Status = "error"
			case "switch_off":
				_, err := integrationDB.Exec(`UPDATE settings SET value='false' WHERE key='chanshui_enabled'`)
				require.NoError(t, err)
			case "credentials":
				_, err := integrationDB.Exec(`UPDATE accounts SET credentials='{"api_key":"changed"}'::jsonb WHERE id=$1`, a.ID)
				require.NoError(t, err)
			case "manual_aba":
				_, err := integrationDB.Exec(`UPDATE accounts SET status='active',schedulable=true WHERE id=$1`, a.ID)
				require.NoError(t, err)
				_, err = integrationDB.Exec(`UPDATE accounts SET status='disabled',schedulable=false WHERE id=$1`, a.ID)
				require.NoError(t, err)
			case "expired":
				_, err := integrationDB.Exec(`UPDATE accounts SET expires_at=NOW()-INTERVAL '1 minute' WHERE id=$1`, a.ID)
				require.NoError(t, err)
			case "rule_edit":
				_, err := integrationDB.Exec(`UPDATE chanshui_test_configs SET updated_at=clock_timestamp() WHERE id=$1`, c.ID)
				require.NoError(t, err)
			case "old_result":
				_, err := integrationDB.Exec(`UPDATE chanshui_test_runs SET account_revision=account_revision-1 WHERE id=$1`, pass.ID)
				require.NoError(t, err)
			case "no_owner":
				_, err := integrationDB.Exec(`DELETE FROM chanshui_account_holds WHERE account_id=$1`, a.ID)
				require.NoError(t, err)
			}
			ok, err := r.FinalizeRun(ctx, pass, &service.ChanshuiDisableDecision{AccountUpdatedAt: a.UpdatedAt, Recover: true})
			require.NoError(t, err)
			require.True(t, ok)
			require.NotEqual(t, "recovered", pass.ActionStatus)
			var status string
			require.NoError(t, integrationDB.QueryRow(`SELECT status FROM accounts WHERE id=$1`, a.ID).Scan(&status))
			require.Equal(t, "disabled", status)
		})
	}
}

func TestChanshuiRecoveryPostgresAllModelsAndInvalidation(t *testing.T) {
	for _, mode := range []string{"serial", "parallel", "new_failed_run", "edit", "delete", "not_opted_in"} {
		t.Run(mode, func(t *testing.T) {
			r, c, a := chanshuiRecoveryFixture(t)
			ctx := context.Background()
			other := *c
			other.ID, other.Model = 0, "other-model"
			if mode == "not_opted_in" {
				other.AutoRecover = false
			}
			require.NoError(t, r.SaveConfig(ctx, &other))
			disabled := chanshuiSuspendAccount(t, r, c, a)
			if mode == "not_opted_in" {
				require.Contains(t, disabled.ActionReason, "需人工恢复")
				got, err := r.GetConfig(ctx, c.ID)
				require.NoError(t, err)
				require.False(t, got.RecoveryPending)
				_, err = integrationDB.Exec(`UPDATE chanshui_test_configs SET next_run_at=NOW()-INTERVAL '1 minute' WHERE account_id=$1`, a.ID)
				require.NoError(t, err)
				require.NoError(t, r.ScheduleDue(ctx, time.Now().UTC(), 10))
				runs, err := r.ListRuns(ctx, other.ID)
				require.NoError(t, err)
				require.Empty(t, runs)
				return
			}
			if mode == "edit" || mode == "delete" {
				if mode == "edit" {
					other.IntervalMinutes = 30
					require.NoError(t, r.SaveConfig(ctx, &other))
				} else {
					require.NoError(t, r.DeleteConfig(ctx, other.ID))
				}
				got, err := r.GetConfig(ctx, c.ID)
				require.NoError(t, err)
				require.False(t, got.RecoveryPending)
				return
			}
			first := chanshuiPassingRun(t, r, c, a)
			if mode == "parallel" {
				// 第一条完成但未提交，第二条领取时先延后第一条轮询，模拟两个工作者并行完成。
				_, err := integrationDB.Exec(`UPDATE chanshui_test_runs SET next_poll_at=NOW()+INTERVAL '1 hour' WHERE id=$1`, first.ID)
				require.NoError(t, err)
				second := chanshuiPassingRun(t, r, &other, a)
				var wg sync.WaitGroup
				for _, run := range []*service.ChanshuiRun{first, second} {
					wg.Add(1)
					go func(run *service.ChanshuiRun) {
						defer wg.Done()
						ok, err := r.FinalizeRun(ctx, run, &service.ChanshuiDisableDecision{AccountUpdatedAt: a.UpdatedAt, Recover: true})
						require.NoError(t, err)
						require.True(t, ok)
					}(run)
				}
				wg.Wait()
				require.ElementsMatch(t, []string{"recovery_pending", "recovered"}, []string{first.ActionStatus, second.ActionStatus})
				return
			}
			ok, err := r.FinalizeRun(ctx, first, &service.ChanshuiDisableDecision{AccountUpdatedAt: a.UpdatedAt, Recover: true})
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, "recovery_pending", first.ActionStatus)
			if mode == "new_failed_run" {
				failed := claimChanshui(t, r, c, a)
				failed.Status, failed.Verdict = "error", "unknown"
				ok, err = r.FinalizeRun(ctx, failed, nil)
				require.NoError(t, err)
				require.True(t, ok)
			}
			second := chanshuiPassingRun(t, r, &other, a)
			ok, err = r.FinalizeRun(ctx, second, &service.ChanshuiDisableDecision{AccountUpdatedAt: a.UpdatedAt, Recover: true})
			require.NoError(t, err)
			require.True(t, ok)
			if mode == "new_failed_run" {
				require.Equal(t, "recovery_pending", second.ActionStatus)
			} else {
				require.Equal(t, "recovered", second.ActionStatus)
			}
		})
	}
}

func TestChanshuiRecoveryPostgresScheduleAndExpiredLeaseRollback(t *testing.T) {
	r, c, a := chanshuiRecoveryFixture(t)
	ctx := context.Background()
	chanshuiSuspendAccount(t, r, c, a)
	_, err := integrationDB.Exec(`UPDATE chanshui_test_configs SET next_run_at=NOW()-INTERVAL '1 minute' WHERE id=$1`, c.ID)
	require.NoError(t, err)
	require.NoError(t, r.ScheduleDue(ctx, time.Now().UTC(), 10))
	runs, err := r.ListRuns(ctx, c.ID)
	require.NoError(t, err)
	require.Len(t, runs, 2)
	require.Equal(t, "schedule", runs[0].Trigger)
	run, err := r.ClaimRun(ctx, time.Now().UTC(), time.Now().UTC().Add(time.Minute), "recovery-lease")
	require.NoError(t, err)
	require.NotNil(t, run)
	run.Status, run.AccountSnapshot, run.AccountUpdatedAt = "running", "safe-snapshot", &a.UpdatedAt
	run.AccountRevision, err = r.GetAccountRevision(ctx, a.ID)
	require.NoError(t, err)
	ok, err := r.SaveRun(ctx, run, time.Now().UTC(), true)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = integrationDB.Exec(`UPDATE chanshui_test_runs SET lease_until=NOW()+INTERVAL '1 second' WHERE id=$1`, run.ID)
	require.NoError(t, err)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.Exec(`SELECT id FROM accounts WHERE id=$1 FOR UPDATE`, a.ID)
	require.NoError(t, err)
	run.Status, run.Verdict, run.Score, run.ScoreComplete = "completed", "clean", chanshuiScore(90), true
	done := make(chan bool, 1)
	go func() {
		ok, e := r.FinalizeRun(ctx, run, &service.ChanshuiDisableDecision{AccountUpdatedAt: a.UpdatedAt, Recover: true})
		require.NoError(t, e)
		done <- ok
	}()
	time.Sleep(1500 * time.Millisecond)
	require.NoError(t, tx.Commit())
	require.False(t, <-done)
	var status string
	require.NoError(t, integrationDB.QueryRow(`SELECT status FROM accounts WHERE id=$1`, a.ID).Scan(&status))
	require.Equal(t, "disabled", status)
}

// 定时器已开始新一轮时，恢复事务必须看到清空的通过记录，不能抢先解除停用。
func TestChanshuiRecoveryPostgresSchedulingSerializesWithRecovery(t *testing.T) {
	r, c, a := chanshuiRecoveryFixture(t)
	ctx := context.Background()
	other := *c
	other.ID, other.Model = 0, "second-protected-model"
	require.NoError(t, r.SaveConfig(ctx, &other))
	chanshuiSuspendAccount(t, r, c, a)
	first := chanshuiPassingRun(t, r, c, a)
	ok, err := r.FinalizeRun(ctx, first, &service.ChanshuiDisableDecision{AccountUpdatedAt: a.UpdatedAt, Recover: true})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "recovery_pending", first.ActionStatus)
	second := chanshuiPassingRun(t, r, &other, a)
	_, err = integrationDB.Exec(`UPDATE chanshui_test_configs SET next_run_at=NOW()-INTERVAL '1 minute' WHERE id=$1`, c.ID)
	require.NoError(t, err)
	blocker, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer blocker.Rollback()
	_, err = blocker.Exec(`SELECT id FROM chanshui_test_configs WHERE id=$1 FOR UPDATE`, c.ID)
	require.NoError(t, err)
	scheduled := make(chan error, 1)
	go func() { scheduled <- r.ScheduleDue(ctx, time.Now().UTC(), 10) }()
	require.Eventually(t, func() bool {
		var n int
		e := integrationDB.QueryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT c.id,c.account_id,a.name%'`).Scan(&n)
		return e == nil && n == 1
	}, 3*time.Second, 10*time.Millisecond)
	finalized := make(chan error, 1)
	go func() {
		_, e := r.FinalizeRun(ctx, second, &service.ChanshuiDisableDecision{AccountUpdatedAt: a.UpdatedAt, Recover: true})
		finalized <- e
	}()
	require.Eventually(t, func() bool {
		var n int
		e := integrationDB.QueryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT pg_advisory_xact_lock(hashtextextended%'`).Scan(&n)
		return e == nil && n >= 1
	}, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, blocker.Commit())
	require.NoError(t, <-scheduled)
	require.NoError(t, <-finalized)
	require.Equal(t, "recovery_pending", second.ActionStatus)
	var status string
	require.NoError(t, integrationDB.QueryRow(`SELECT status FROM accounts WHERE id=$1`, a.ID).Scan(&status))
	require.Equal(t, "disabled", status)
	runs, err := r.ListRuns(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, "queued", runs[0].Status)
}
