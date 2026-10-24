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

// 真实事务中切换 SQL 提示不改变请求指纹；不同调用方并发重放也只能扣款与累计额度一次。
func TestUsageBillingVertexQuotaConcurrentHintVariantsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	user := mustCreateUser(t, client, &service.User{Email: "vertex-concurrent-" + uuid.NewString() + "@example.com", Balance: 100})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-vertex-" + uuid.NewString(), Name: "vertex", Quota: 1000})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "vertex-quota-" + uuid.NewString(), Platform: service.PlatformGemini, Type: service.AccountTypeServiceAccount,
		Extra: map[string]any{"quota_limit": 1000.0, "quota_daily_limit": 1.0, "quota_weekly_limit": 10.0},
	})
	var revisionBefore int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT chanshui_config_revision FROM accounts WHERE id=$1", account.ID).Scan(&revisionBefore))
	requestID := uuid.NewString()
	const workers = 32
	outcomes := make(chan usageBillingApplyOutcome, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			result, err := repo.Apply(ctx, &service.UsageBillingCommand{
				RequestID: requestID, APIKeyID: key.ID, UserID: user.ID, AccountID: account.ID,
				AccountType: service.AccountTypeServiceAccount, BillableAmountUSD: 1.5,
				APIKeyQuotaCost: 1.5, AccountQuotaCost: 2,
				AccountQuotaHasDailyLimit: index&1 != 0, AccountQuotaHasWeeklyLimit: index&2 != 0,
			})
			outcomes <- usageBillingApplyOutcome{result: result, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(outcomes)
	applied := 0
	for outcome := range outcomes {
		require.NoError(t, outcome.err)
		require.NotNil(t, outcome.result)
		if outcome.result.Applied {
			applied++
		}
	}
	require.Equal(t, 1, applied)
	var balance, keyUsed, total, daily, weekly float64
	var revisionAfter, dedup, outbox int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id=$1", user.ID).Scan(&balance))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT quota_used FROM api_keys WHERE id=$1", key.ID).Scan(&keyUsed))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT (extra->>'quota_used')::numeric, (extra->>'quota_daily_used')::numeric, (extra->>'quota_weekly_used')::numeric, chanshui_config_revision FROM accounts WHERE id=$1", account.ID).Scan(&total, &daily, &weekly, &revisionAfter))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2", requestID, key.ID).Scan(&dedup))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1 AND event_type=$2", account.ID, service.SchedulerOutboxEventAccountChanged).Scan(&outbox))
	require.InDelta(t, 98.5, balance, 1e-9)
	require.InDelta(t, 1.5, keyUsed, 1e-9)
	require.InDelta(t, 2, total, 1e-9)
	require.InDelta(t, 2, daily, 1e-9)
	require.InDelta(t, 2, weekly, 1e-9)
	require.EqualValues(t, 1, dedup)
	require.EqualValues(t, 1, outbox)
	// 额度计数不能被掺水检测误认为账号配置变更，否则活跃检测会被作废。
	require.Equal(t, revisionBefore, revisionAfter)
}

// 账号在结算排队期间新增周期额度时，快路径守卫必须按锁释放后的行重新判断。
func TestUsageBillingVertexQuotaConfigurationChangeDuringLockWait(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	user := mustCreateUser(t, client, &service.User{Email: "vertex-drift-" + uuid.NewString() + "@example.com", Balance: 20})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-vertex-drift-" + uuid.NewString(), Name: "vertex"})
	account := mustCreateAccount(t, client, &service.Account{Name: "vertex-drift-" + uuid.NewString(), Platform: service.PlatformGemini, Type: service.AccountTypeServiceAccount, Extra: map[string]any{"quota_limit": 100.0}})
	adminTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = adminTx.Rollback() }()
	var lockedID, blockerPID int64
	require.NoError(t, adminTx.QueryRowContext(ctx, "SELECT id, pg_backend_pid() FROM accounts WHERE id=$1 FOR UPDATE", account.ID).Scan(&lockedID, &blockerPID))
	outcome := make(chan usageBillingApplyOutcome, 1)
	go func() {
		result, err := repo.Apply(ctx, &service.UsageBillingCommand{
			RequestID: uuid.NewString(), APIKeyID: key.ID, UserID: user.ID, AccountID: account.ID,
			AccountType: service.AccountTypeServiceAccount, BillableAmountUSD: 1, AccountQuotaCost: 3,
		})
		outcome <- usageBillingApplyOutcome{result: result, err: err}
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		err := integrationDB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND $1=ANY(pg_blocking_pids(pid)))", blockerPID).Scan(&waiting)
		return err == nil && waiting
	}, 5*time.Second, 10*time.Millisecond)
	_, err = adminTx.ExecContext(ctx, `UPDATE accounts SET extra=extra || '{"quota_daily_limit":2,"quota_weekly_limit":9}'::jsonb WHERE id=$1`, account.ID)
	require.NoError(t, err)
	require.NoError(t, adminTx.Commit())
	select {
	case got := <-outcome:
		require.NoError(t, got.err)
		require.True(t, got.result.Applied)
		require.NotNil(t, got.result.QuotaState)
		require.InDelta(t, 3, got.result.QuotaState.DailyUsed, 1e-9)
		require.InDelta(t, 3, got.result.QuotaState.WeeklyUsed, 1e-9)
		require.InDelta(t, 2, got.result.QuotaState.DailyLimit, 1e-9)
	case <-time.After(5 * time.Second):
		t.Fatal("quota settlement did not resume after admin transaction")
	}
}

// 配额脏数据导致 SQL 失败时，已经执行的用户/Key 账务及幂等占位必须一起回滚。
func TestUsageBillingVertexQuotaSQLFailureRollsBackAndRetrySucceeds(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	user := mustCreateUser(t, client, &service.User{Email: "vertex-rollback-" + uuid.NewString() + "@example.com", Balance: 10})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-vertex-rollback-" + uuid.NewString(), Name: "vertex", Quota: 100})
	account := mustCreateAccount(t, client, &service.Account{Name: "vertex-rollback-" + uuid.NewString(), Platform: service.PlatformGemini, Type: service.AccountTypeServiceAccount, Extra: map[string]any{"quota_limit": 100.0}})
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=extra || '{"quota_daily_limit":"invalid"}'::jsonb WHERE id=$1`, account.ID)
	require.NoError(t, err)
	command := service.UsageBillingCommand{RequestID: uuid.NewString(), APIKeyID: key.ID, UserID: user.ID, AccountID: account.ID, AccountType: service.AccountTypeServiceAccount, BillableAmountUSD: 1, APIKeyQuotaCost: 1, AccountQuotaCost: 2}
	result, err := repo.Apply(ctx, &command)
	require.Error(t, err)
	require.Nil(t, result)
	var balance, keyUsed, quotaUsed float64
	var dedup int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id=$1", user.ID).Scan(&balance))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT quota_used FROM api_keys WHERE id=$1", key.ID).Scan(&keyUsed))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COALESCE((extra->>'quota_used')::numeric,0) FROM accounts WHERE id=$1", account.ID).Scan(&quotaUsed))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2", command.RequestID, key.ID).Scan(&dedup))
	require.InDelta(t, 10, balance, 1e-9)
	require.Zero(t, keyUsed)
	require.Zero(t, quotaUsed)
	require.Zero(t, dedup)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=extra || '{"quota_daily_limit":10}'::jsonb WHERE id=$1`, account.ID)
	require.NoError(t, err)
	result, err = repo.Apply(ctx, &command)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.InDelta(t, 1, result.BalanceAmountUSD, 1e-9, fmt.Sprintf("request %s", command.RequestID))
	require.InDelta(t, 2, result.QuotaState.DailyUsed, 1e-9)
}
