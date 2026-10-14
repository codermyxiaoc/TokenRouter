//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 多实例即使同时走到扣款，数据库幂等与同事务回执也只能产生一次资金和额度变动。
func TestLegacyVideoRepositoryConcurrentSettlementAndArchivedReplay(t *testing.T) {
	f := newLegacyVideoFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	task := f.task()
	require.NoError(t, f.repo.Save(ctx, task))
	cmd := service.UsageBillingCommand{LegacyVideoTaskID: task.ID, RequestID: task.RequestID,
		APIKeyID: f.key, UserID: f.user, ActorUserID: f.user, AccountID: f.account, GroupID: &f.group,
		APIKeyBillingMode: service.APIKeyBillingModeBalance, AccountType: service.AccountTypeAPIKey,
		BillableAmountUSD: 0.25, BaseAmountUSD: 0.5, BalanceRateMultiplier: 0.5,
		APIKeyQuotaCost: 0.25, APIKeyRateLimitCost: 0.25, AccountQuotaCost: 0.75}
	type outcome struct {
		result *service.UsageBillingApplyResult
		err    error
	}
	const workers = 24
	outcomes := make(chan outcome, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			copy := cmd
			result, err := NewUsageBillingRepository(integrationEntClient, integrationDB).Apply(ctx, &copy)
			outcomes <- outcome{result, err}
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)
	applied := 0
	for out := range outcomes {
		require.NoError(t, out.err)
		require.NotNil(t, out.result)
		if out.result.Applied {
			applied++
		}
		require.InDelta(t, 0.25, out.result.BalanceAmountUSD, 1e-10)
		require.Zero(t, out.result.SubscriptionAmountUSD)
	}
	require.Equal(t, 1, applied)
	assertTotals := func() {
		t.Helper()
		var balance, quota, usage5h, accountUsage float64
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, f.user).Scan(&balance))
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used,usage_5h FROM api_keys WHERE id=$1`, f.key).Scan(&quota, &usage5h))
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT (extra->>'quota_used')::numeric FROM accounts WHERE id=$1`, f.account).Scan(&accountUsage))
		require.InDelta(t, 99.75, balance, 1e-8)
		require.InDelta(t, 0.25, quota, 1e-8)
		require.InDelta(t, 0.25, usage5h, 1e-8)
		require.InDelta(t, 0.75, accountUsage, 1e-8)
	}
	assertTotals()
	// 模拟长期归档后重启恢复：不能因为热去重表清理而再次扣费。
	_, err := integrationDB.ExecContext(ctx, `WITH moved AS (
 DELETE FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2
 RETURNING request_id,api_key_id,request_fingerprint,created_at)
 INSERT INTO usage_billing_dedup_archive(request_id,api_key_id,request_fingerprint,created_at)
 SELECT request_id,api_key_id,request_fingerprint,created_at FROM moved`, task.RequestID, f.key)
	require.NoError(t, err)
	stored, err := NewLegacyVideoTaskRepository(integrationDB).Get(ctx, task.Source, task.TaskID, f.user, f.key)
	require.NoError(t, err)
	require.Equal(t, task.ID, stored.ID)
	billing := NewUsageBillingRepository(integrationEntClient, integrationDB)
	replay, err := billing.Apply(ctx, &cmd)
	require.NoError(t, err)
	require.False(t, replay.Applied)
	require.InDelta(t, 0.25, replay.BalanceAmountUSD, 1e-10)
	conflict := cmd
	conflict.RequestFingerprint = ""
	conflict.BalanceRateMultiplier = 1
	_, err = billing.Apply(ctx, &conflict)
	require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)
	assertTotals()
}
