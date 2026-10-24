//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 所有样本与全局开关只写测试数据库，清理保留测试前值。
func chanshuiFixture(t *testing.T) (service.ChanshuiRepository, *service.ChanshuiConfig, *service.Account) {
	t.Helper()
	ctx := context.Background()
	a := mustCreateAccount(t, integrationEntClient, &service.Account{Name: "chanshui-" + uuid.NewString(), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true})
	// PostgreSQL 将时间戳保存为微秒精度，生产服务同样通过仓储回读账号版本。
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT updated_at FROM accounts WHERE id=$1`, a.ID).Scan(&a.UpdatedAt))
	var previous string
	err := integrationDB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='chanshui_enabled'`).Scan(&previous)
	wasMissing := err == sql.ErrNoRows
	if !wasMissing {
		require.NoError(t, err)
	}
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES('chanshui_enabled','true',NOW()) ON CONFLICT(key) DO UPDATE SET value='true'`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM chanshui_test_configs WHERE account_id=$1`, a.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE account_id=$1`, a.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, a.ID)
		if wasMissing {
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM settings WHERE key='chanshui_enabled'`)
		} else {
			_, _ = integrationDB.ExecContext(ctx, `UPDATE settings SET value=$1 WHERE key='chanshui_enabled'`, previous)
		}
	})
	r := NewChanshuiRepository(integrationDB)
	c := &service.ChanshuiConfig{AccountID: a.ID, Model: "gpt-test", Protocol: "openai", Sections: []string{"fingerprint", "iq"}, TimeoutSeconds: 120, Enabled: true, IntervalMinutes: 60, DisableOnAdulteration: true}
	require.NoError(t, r.SaveConfig(ctx, c))
	return r, c, a
}

func claimChanshui(t *testing.T, repo service.ChanshuiRepository, c *service.ChanshuiConfig, a *service.Account) *service.ChanshuiRun {
	t.Helper()
	ctx := context.Background()
	_, err := repo.CreateRun(ctx, c.ID, time.Now().UTC())
	require.NoError(t, err)
	run, err := repo.ClaimRun(ctx, time.Now().UTC(), time.Now().UTC().Add(time.Minute), uuid.NewString())
	require.NoError(t, err)
	require.NotNil(t, run)
	require.Equal(t, c.ID, run.ConfigID)
	run.AccountSnapshot = "safe-sha256-identity"
	run.AccountRevision, err = repo.GetAccountRevision(ctx, a.ID)
	require.NoError(t, err)
	run.AccountUpdatedAt = &a.UpdatedAt
	run.Status = "running"
	run.RemoteID = "private-remote-id"
	expires := time.Now().UTC().Add(time.Hour)
	run.RemoteExpiresAt = &expires
	ok, err := repo.SaveRun(ctx, run, time.Now().UTC(), true)
	require.NoError(t, err)
	require.True(t, ok)
	return run
}

func TestChanshuiPostgresConcurrentCreateClaimAndStaleLease(t *testing.T) {
	repo, c, _ := chanshuiFixture(t)
	ctx := context.Background()
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.CreateRun(ctx, c.ID, time.Now().UTC())
			if err == nil {
				winners.Add(1)
			} else {
				require.ErrorIs(t, err, service.ErrChanshuiBusy)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), winners.Load())
	require.ErrorIs(t, repo.SaveConfig(ctx, c), service.ErrChanshuiBusy)
	require.ErrorIs(t, repo.DeleteConfig(ctx, c.ID), service.ErrChanshuiBusy)
	claimed := make(chan *service.ChanshuiRun, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			now := time.Now().UTC()
			run, err := repo.ClaimRun(ctx, now, now.Add(time.Minute), uuid.NewString())
			require.NoError(t, err)
			if run != nil {
				claimed <- run
			}
		}()
	}
	wg.Wait()
	close(claimed)
	var old *service.ChanshuiRun
	for run := range claimed {
		require.Nil(t, old)
		old = run
	}
	require.NotNil(t, old)
	_, err := integrationDB.ExecContext(ctx, `UPDATE chanshui_test_runs SET lease_until=NOW()-INTERVAL '1 second' WHERE id=$1`, old.ID)
	require.NoError(t, err)
	fresh, err := repo.ClaimRun(ctx, time.Now().UTC(), time.Now().UTC().Add(time.Minute), "fresh")
	require.NoError(t, err)
	require.NotNil(t, fresh)
	old.Status = "completed"
	old.Verdict = "adulterated"
	ok, err := repo.FinalizeRun(ctx, old, &service.ChanshuiDisableDecision{AccountUpdatedAt: time.Now().UTC()})
	require.NoError(t, err)
	require.False(t, ok)
	fresh.Status = "submitting"
	ok, err = repo.SaveRun(ctx, fresh, time.Now().UTC(), true)
	require.NoError(t, err)
	require.True(t, ok)
	old.Status = "queued"
	ok, err = repo.SaveRun(ctx, old, time.Now().UTC(), false)
	require.NoError(t, err)
	require.False(t, ok)
	got, err := repo.GetRun(ctx, fresh.ID)
	require.NoError(t, err)
	require.Equal(t, "submitting", got.Status)
}

func TestChanshuiPostgresSharedSubmitGate(t *testing.T) {
	ctx := context.Background()
	var previous time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT next_allowed_at FROM chanshui_submit_gate WHERE id=1`).Scan(&previous))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `UPDATE chanshui_submit_gate SET next_allowed_at=$1 WHERE id=1`, previous)
	})
	_, err := integrationDB.ExecContext(ctx, `UPDATE chanshui_submit_gate SET next_allowed_at='epoch' WHERE id=1`)
	require.NoError(t, err)
	now := time.Now().UTC()
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slot, e := NewChanshuiRepository(integrationDB).AcquireSubmitSlot(ctx, now)
			require.NoError(t, e)
			if slot.Equal(now) {
				winners.Add(1)
			} else {
				require.True(t, slot.After(now.Add(59*time.Second)))
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), winners.Load())
	var next time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT next_allowed_at FROM chanshui_submit_gate WHERE id=1`).Scan(&next))
	// 忙请求不能预占后续时间，错误的应用时钟也不能绕过数据库闸门。
	slot, err := NewChanshuiRepository(integrationDB).AcquireSubmitSlot(ctx, now.Add(24*time.Hour))
	require.NoError(t, err)
	require.True(t, slot.After(now.Add(24*time.Hour+59*time.Second)))
	var unchanged time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT next_allowed_at FROM chanshui_submit_gate WHERE id=1`).Scan(&unchanged))
	require.True(t, next.Equal(unchanged))
}

func TestChanshuiPostgresAtomicDisableAndReplay(t *testing.T) {
	repo, c, a := chanshuiFixture(t)
	ctx := context.Background()
	run := claimChanshui(t, repo, c, a)
	run.Status = "completed"
	run.Verdict = "adulterated"
	run.Report = json.RawMessage(`{"findings":["redacted result"]}`)
	var before int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&before))
	ok, err := repo.FinalizeRun(ctx, run, &service.ChanshuiDisableDecision{AccountUpdatedAt: a.UpdatedAt, Reason: "untrusted upstream value"})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "disabled", run.ActionStatus)
	var status, reason string
	var schedulable bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status,schedulable,error_message FROM accounts WHERE id=$1`, a.ID).Scan(&status, &schedulable, &reason))
	require.Equal(t, service.StatusDisabled, status)
	require.False(t, schedulable)
	require.NotContains(t, reason, "untrusted")
	var after int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&after))
	require.Equal(t, before+1, after)
	got, err := repo.GetRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "disabled", got.ActionStatus)
	require.JSONEq(t, string(run.Report), string(got.Report))
	require.NotNil(t, got.RemoteExpiresAt)
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	for _, secret := range []string{run.RemoteID, run.AccountSnapshot, run.LeaseToken, "remote_expires_at", "account_updated_at", "config_updated_at", "account_revision"} {
		require.NotContains(t, string(raw), secret)
	}
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET status='active',schedulable=true,updated_at=NOW() WHERE id=$1`, a.ID)
	require.NoError(t, err)
	ok, err = repo.FinalizeRun(ctx, run, &service.ChanshuiDisableDecision{AccountUpdatedAt: a.UpdatedAt})
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM accounts WHERE id=$1`, a.ID).Scan(&status))
	require.Equal(t, "active", status)
}

func TestChanshuiPostgresDisableGuards(t *testing.T) {
	for _, tc := range []struct {
		name, status, verdict, mutate, action string
		complete                              bool
		score                                 *float64
	}{
		{name: "error", status: "error", verdict: "adulterated", complete: true, score: chanshuiScore(1), action: "none"},
		{name: "unknown", status: "unknown", verdict: "adulterated", complete: true, score: chanshuiScore(1), action: "none"},
		{name: "partial score", status: "completed", verdict: "unknown", score: chanshuiScore(1), action: "none"},
		{name: "complete low score without fingerprint", status: "completed", verdict: "unknown", complete: true, score: chanshuiScore(1), action: "disabled"},
		{name: "clean high score", status: "completed", verdict: "clean", complete: true, score: chanshuiScore(90), action: "none"},
		{name: "account edited", status: "completed", verdict: "adulterated", mutate: "account", action: "skipped_changed"},
		{name: "account stopped", status: "completed", verdict: "adulterated", mutate: "disabled", action: "skipped_disabled"},
		{name: "config edited", status: "completed", verdict: "adulterated", mutate: "config", action: "skipped_changed"},
		{name: "switch closed", status: "completed", verdict: "adulterated", mutate: "setting", action: "skipped_changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, c, a := chanshuiFixture(t)
			ctx := context.Background()
			c.MinimumScore = chanshuiScore(70)
			require.NoError(t, repo.SaveConfig(ctx, c))
			run := claimChanshui(t, repo, c, a)
			switch tc.mutate {
			case "account":
				_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET updated_at=NOW()+INTERVAL '1 second' WHERE id=$1`, a.ID)
				require.NoError(t, err)
			case "disabled":
				_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET status='disabled',schedulable=false WHERE id=$1`, a.ID)
				require.NoError(t, err)
			case "config":
				_, err := integrationDB.ExecContext(ctx, `UPDATE chanshui_test_configs SET updated_at=NOW()+INTERVAL '1 second' WHERE id=$1`, c.ID)
				require.NoError(t, err)
			case "setting":
				_, err := integrationDB.ExecContext(ctx, `UPDATE settings SET value='false' WHERE key='chanshui_enabled'`)
				require.NoError(t, err)
			}
			run.Status = tc.status
			run.Verdict = tc.verdict
			run.ScoreComplete = tc.complete
			run.Score = tc.score
			ok, err := repo.FinalizeRun(ctx, run, &service.ChanshuiDisableDecision{AccountUpdatedAt: a.UpdatedAt})
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, tc.action, run.ActionStatus)
			var status string
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM accounts WHERE id=$1`, a.ID).Scan(&status))
			if tc.action == "disabled" || tc.mutate == "disabled" {
				require.Equal(t, "disabled", status)
			} else {
				require.Equal(t, "active", status)
			}
		})
	}
}

func chanshuiScore(v float64) *float64 { return &v }

func TestChanshuiPostgresAccountRevisionABAAndRuntimeUpdates(t *testing.T) {
	for _, kind := range []string{"manual_disable_restore", "credentials_restore", "runtime_only"} {
		t.Run(kind, func(t *testing.T) {
			repo, c, a := chanshuiFixture(t)
			ctx := context.Background()
			run := claimChanshui(t, repo, c, a)
			switch kind {
			case "manual_disable_restore":
				_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET status='disabled',schedulable=false,updated_at=NOW() WHERE id=$1`, a.ID)
				require.NoError(t, err)
				_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET status='active',schedulable=true,updated_at=NOW() WHERE id=$1`, a.ID)
				require.NoError(t, err)
			case "credentials_restore":
				_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=credentials||'{"api_key":"replaced-test-key"}'::jsonb,updated_at=NOW() WHERE id=$1`, a.ID)
				require.NoError(t, err)
				_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=credentials-'api_key',updated_at=NOW() WHERE id=$1`, a.ID)
				require.NoError(t, err)
			case "runtime_only":
				_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET last_used_at=NOW(),updated_at=NOW(),extra=extra||'{"usage_test_snapshot":{"used":1}}'::jsonb WHERE id=$1`, a.ID)
				require.NoError(t, err)
			}
			revision, err := repo.GetAccountRevision(ctx, a.ID)
			require.NoError(t, err)
			if kind == "runtime_only" {
				require.Equal(t, run.AccountRevision, revision)
			} else {
				require.Equal(t, run.AccountRevision+2, revision)
			}
			// 即使服务重读了最新更新时间，持久代次仍需拒绝回到相同表面配置的旧结果。
			var current time.Time
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT updated_at FROM accounts WHERE id=$1`, a.ID).Scan(&current))
			run.Status, run.Verdict = "completed", "adulterated"
			ok, err := repo.FinalizeRun(ctx, run, &service.ChanshuiDisableDecision{AccountUpdatedAt: current})
			require.NoError(t, err)
			require.True(t, ok)
			var status string
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM accounts WHERE id=$1`, a.ID).Scan(&status))
			if kind == "runtime_only" {
				require.Equal(t, "disabled", run.ActionStatus)
				require.Equal(t, "disabled", status)
			} else {
				require.Equal(t, "skipped_changed", run.ActionStatus)
				require.Equal(t, "active", status)
			}
		})
	}
}

func TestChanshuiPostgresExpiredLeaseDuringDisableRollsBack(t *testing.T) {
	repo, c, a := chanshuiFixture(t)
	ctx := context.Background()
	run := claimChanshui(t, repo, c, a)
	_, err := integrationDB.ExecContext(ctx, `UPDATE chanshui_test_runs SET lease_until=NOW()+INTERVAL '1 second' WHERE id=$1`, run.ID)
	require.NoError(t, err)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `SELECT id FROM accounts WHERE id=$1 FOR UPDATE`, a.ID)
	require.NoError(t, err)
	run.Status = "completed"
	run.Verdict = "adulterated"
	done := make(chan bool, 1)
	go func() {
		ok, e := repo.FinalizeRun(ctx, run, &service.ChanshuiDisableDecision{AccountUpdatedAt: a.UpdatedAt})
		require.NoError(t, e)
		done <- ok
	}()
	time.Sleep(1500 * time.Millisecond)
	require.NoError(t, tx.Commit())
	require.False(t, <-done)
	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM accounts WHERE id=$1`, a.ID).Scan(&status))
	require.Equal(t, "active", status)
	got, err := repo.GetRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "running", got.Status)
}

func TestChanshuiPostgresHistoryAndScheduledRuns(t *testing.T) {
	repo, c, _ := chanshuiFixture(t)
	ctx := context.Background()
	c.ScheduleEnabled = true
	require.NoError(t, repo.SaveConfig(ctx, c))
	_, err := integrationDB.ExecContext(ctx, `UPDATE chanshui_test_configs SET next_run_at=NOW()-INTERVAL '1 minute' WHERE id=$1`, c.ID)
	require.NoError(t, err)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); require.NoError(t, repo.ScheduleDue(ctx, time.Now().UTC(), 2)) }()
	}
	wg.Wait()
	runs, err := repo.ListRuns(ctx, c.ID)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, "schedule", runs[0].Trigger)
	for i := 0; i < 65; i++ {
		if i > 0 {
			_, err = repo.CreateRun(ctx, c.ID, time.Now().UTC())
			require.NoError(t, err)
		}
		run, e := repo.ClaimRun(ctx, time.Now().UTC(), time.Now().UTC().Add(time.Minute), uuid.NewString())
		require.NoError(t, e)
		require.NotNil(t, run)
		run.Status = "completed"
		run.Verdict = "clean"
		run.Report = json.RawMessage(`{"detail":"` + strings.Repeat("data", 10000) + `"}`)
		ok, e := repo.FinalizeRun(ctx, run, nil)
		require.NoError(t, e)
		require.True(t, ok)
	}
	runs, err = repo.ListRuns(ctx, c.ID)
	require.NoError(t, err)
	require.Len(t, runs, 60)
	for _, run := range runs {
		require.Empty(t, run.Report)
	}
	got, err := repo.GetRun(ctx, runs[0].ID)
	require.NoError(t, err)
	require.Greater(t, len(got.Report), 40000)
	var recordSize int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT octet_length(record::text) FROM chanshui_test_runs WHERE id=$1`, got.ID).Scan(&recordSize))
	require.Less(t, recordSize, 2048)
	oldAccount := c.AccountID
	c.AccountID++
	require.Error(t, repo.SaveConfig(ctx, c))
	c.AccountID = oldAccount
	require.NoError(t, repo.DeleteConfig(ctx, c.ID))
	_, err = repo.GetRun(ctx, got.ID)
	require.ErrorIs(t, err, service.ErrChanshuiNotFound)
}

// 停用、暂停调度和过期账号可以手动诊断，但定时器不得自动发出新的收费检测。
func TestChanshuiPostgresManualDiagnosisAndScheduledEligibility(t *testing.T) {
	for _, state := range []string{"disabled", "unschedulable", "expired"} {
		t.Run(state, func(t *testing.T) {
			repo, c, a := chanshuiFixture(t)
			ctx := context.Background()
			c.ScheduleEnabled = true
			require.NoError(t, repo.SaveConfig(ctx, c))
			_, err := integrationDB.ExecContext(ctx, `UPDATE chanshui_test_configs SET next_run_at=NOW()-INTERVAL '1 minute' WHERE id=$1`, c.ID)
			require.NoError(t, err)
			switch state {
			case "disabled":
				_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET status='disabled' WHERE id=$1`, a.ID)
			case "unschedulable":
				_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET schedulable=false WHERE id=$1`, a.ID)
			case "expired":
				_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET expires_at=NOW()-INTERVAL '1 minute' WHERE id=$1`, a.ID)
			}
			require.NoError(t, err)
			require.NoError(t, repo.ScheduleDue(ctx, time.Now().UTC(), 10))
			runs, err := repo.ListRuns(ctx, c.ID)
			require.NoError(t, err)
			require.Empty(t, runs)
			run, err := repo.CreateRun(ctx, c.ID, time.Now().UTC())
			require.NoError(t, err)
			require.Equal(t, "manual", run.Trigger)
			stored, err := repo.GetRun(ctx, run.ID)
			require.NoError(t, err)
			require.Equal(t, "manual", stored.Trigger)
			var status string
			var schedulable bool
			var expired bool
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status,schedulable,expires_at IS NOT NULL AND expires_at<=NOW() FROM accounts WHERE id=$1`, a.ID).Scan(&status, &schedulable, &expired))
			switch state {
			case "disabled":
				require.Equal(t, "disabled", status)
			case "unschedulable":
				require.False(t, schedulable)
			case "expired":
				require.True(t, expired)
			}
		})
	}
}
