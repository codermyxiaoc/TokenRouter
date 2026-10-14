//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 真实连接在资金及额度已执行、回执尚未写入时中断，整个事务必须回滚且可由新仓储恰好恢复一次。
func TestUsageBillingPlatformQuotaConnectionTerminationBeforeCommitRecoversOnce(t *testing.T) {
	f := newLegacyVideoFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// 终止连接仅允许发生在集成框架创建的隔离数据库，禁止以外部配置替代生产连接。
	var database string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT current_database()`).Scan(&database))
	require.Equal(t, "sub2api_test", database)
	insertSettlementPlatformQuota(t, f.user, service.PlatformGrok, float64Ptr(100))
	task := f.task()
	require.NoError(t, f.repo.Save(ctx, task))
	plan := mustCreatePlan(t, integrationEntClient, &service.SubscriptionPlan{
		Name: "quota-crash-" + uuid.NewString(), Price: 1, GroupIDs: []int64{f.group},
		GroupRateMultipliers: map[int64]float64{f.group: 2},
	})
	sub := mustCreateSubscription(t, integrationEntClient, &service.UserSubscription{
		UserID: f.user, PlanID: plan.ID, DailyLimitUSD: float64Ptr(0.5),
	})
	cmd := settlementQuotaCommand(f, task.RequestID)
	cmd.LegacyVideoTaskID = task.ID
	cmd.APIKeyBillingMode, cmd.BaseAmountUSD, cmd.BalanceRateMultiplier = service.APIKeyBillingModeAuto, 1, 0.5
	cmd.SubscriptionRateMultiplier, cmd.SubscriptionRateMultiplierScale = 2, 1

	// 原任务回执在资金和平台计数之后更新，因此只锁这一行便可精确停在提交前。
	blocker, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer blocker.Rollback()
	var blockerPID int
	var lockedTaskID int64
	require.NoError(t, blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID))
	require.NoError(t, blocker.QueryRowContext(ctx, `SELECT id FROM legacy_video_tasks WHERE id=$1 FOR UPDATE`, task.ID).Scan(&lockedTaskID))
	require.Equal(t, task.ID, lockedTaskID)
	completed := make(chan quotaBoundaryApplyOutcome, 1)
	go func() {
		// 单独复制输入，观察连接退出前不与 Normalize 修改共享命令形成竞态。
		attempt := cmd
		result, err := NewUsageBillingRepository(integrationEntClient, integrationDB).Apply(ctx, &attempt)
		completed <- quotaBoundaryApplyOutcome{result: result, err: err}
	}()
	const receiptUpdate = "UPDATE legacy_video_tasks SET billing_result="
	applyPID := quotaBoundaryWaiter(t, ctx, blockerPID, receiptUpdate)
	require.NotEqual(t, blockerPID, applyPID)

	// 读取已提交视图：尚未提交的余额、额度及回执不能被另一连接提前观察到。
	assertRolledBack := func() {
		t.Helper()
		billingFocusAssertDecimal(t, "100", `SELECT balance::text FROM users WHERE id=$1`, f.user)
		billingFocusAssertDecimal(t, "0", `SELECT daily_usage_usd::text FROM user_subscriptions WHERE id=$1`, sub.ID)
		billingFocusAssertDecimal(t, "0", `SELECT quota_used::text FROM api_keys WHERE id=$1`, f.key)
		billingFocusAssertDecimal(t, "0", `SELECT COALESCE((extra->>'quota_used')::numeric,0)::text FROM accounts WHERE id=$1`, f.account)
		assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, 0)
		var receiptMissing bool
		var dedupCount int
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT billing_result IS NULL FROM legacy_video_tasks WHERE id=$1`, task.ID).Scan(&receiptMissing))
		require.True(t, receiptMissing)
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2`, cmd.RequestID, cmd.APIKeyID).Scan(&dedupCount))
		require.Zero(t, dedupCount)
	}
	assertRolledBack()

	// 再次限定数据库、阻塞关系和正在执行的 SQL，避免终止测试框架或其他用例的连接。
	var terminated bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
 WHERE pid=$1 AND datname=current_database() AND datname='sub2api_test'
 AND wait_event_type='Lock' AND $2::int=ANY(pg_blocking_pids(pid)) AND strpos(query,$3)>0`,
		applyPID, blockerPID, receiptUpdate).Scan(&terminated))
	require.True(t, terminated)
	select {
	case outcome := <-completed:
		require.Error(t, outcome.err)
		require.Nil(t, outcome.result)
	case <-ctx.Done():
		t.Fatalf("连接中断后扣费未退出：%v", ctx.Err())
	}
	assertRolledBack()
	require.NoError(t, blocker.Commit())

	// 模拟新进程重放原命令；订阅与余额按原倍率分摊，平台只累计真实余额部分。
	restarted := NewUsageBillingRepository(integrationEntClient, integrationDB)
	paid, err := restarted.Apply(ctx, &cmd)
	require.NoError(t, err)
	require.True(t, paid.Applied)
	require.InDelta(t, 0.5, paid.SubscriptionAmountUSD, 1e-9)
	require.InDelta(t, 0.375, paid.BalanceAmountUSD, 1e-9)
	assertCommitted := func() {
		t.Helper()
		billingFocusAssertDecimal(t, "99.625", `SELECT balance::text FROM users WHERE id=$1`, f.user)
		billingFocusAssertDecimal(t, "0.5", `SELECT daily_usage_usd::text FROM user_subscriptions WHERE id=$1`, sub.ID)
		billingFocusAssertDecimal(t, "0.875", `SELECT quota_used::text FROM api_keys WHERE id=$1`, f.key)
		billingFocusAssertDecimal(t, "0.25", `SELECT (extra->>'quota_used')::numeric::text FROM accounts WHERE id=$1`, f.account)
		assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, 0.375)
		var dedupCount int
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2`, cmd.RequestID, cmd.APIKeyID).Scan(&dedupCount))
		require.Equal(t, 1, dedupCount)
		var raw []byte
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT billing_result FROM legacy_video_tasks WHERE id=$1`, task.ID).Scan(&raw))
		var receipt service.UsageBillingApplyResult
		require.NoError(t, json.Unmarshal(raw, &receipt))
		require.Equal(t, *paid, receipt)
	}
	assertCommitted()
	replayed, err := NewUsageBillingRepository(integrationEntClient, integrationDB).Apply(ctx, &cmd)
	require.NoError(t, err)
	require.False(t, replayed.Applied)
	expected := *paid
	expected.Applied = false
	require.Equal(t, expected, *replayed)
	assertCommitted()
}
