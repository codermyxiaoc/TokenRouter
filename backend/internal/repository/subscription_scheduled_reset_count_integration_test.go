//go:build integration

package repository

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 从未消费的订阅也累计计划次数，但后台不得激活真实窗口或改变余额、已用额度。
func TestSubscriptionScheduledResetCountPostgres_NeverUsed(t *testing.T) {
	ctx := context.Background()
	f := newResetCountFixture(t, false)
	// 使用实际时刻作为周/月备用锚点，避免断言依赖测试运行时是上午还是下午。
	start := time.Now().AddDate(0, 0, -35).Truncate(time.Microsecond)
	_, err := integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET starts_at=$1,reset_counted_at=$1 WHERE id=$2`, start, f.sub.ID)
	require.NoError(t, err)
	counter := f.repo.(service.ScheduledSubscriptionResetCounter)
	require.NoError(t, counter.RefreshScheduledResetCounts(ctx, time.Now()))
	got, err := f.repo.GetByID(ctx, f.sub.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{35, 5, 1}, resetCountValues(got))
	require.Nil(t, got.DailyWindowStart)
	require.Nil(t, got.WeeklyWindowStart)
	require.Nil(t, got.MonthlyWindowStart)
	require.Zero(t, got.DailyUsageUSD)
	require.Zero(t, got.WeeklyUsageUSD)
	require.Zero(t, got.MonthlyUsageUSD)
	require.NoError(t, counter.RefreshScheduledResetCounts(ctx, time.Now()))
	again, err := f.repo.GetByID(ctx, f.sub.ID)
	require.NoError(t, err)
	require.Equal(t, resetCountValues(got), resetCountValues(again))
}

// 过期后启动服务也能结清到期前已经到达的周期，包括最后一个不完整尾段。
func TestSubscriptionScheduledResetCountPostgres_ExpiredCatchup(t *testing.T) {
	ctx := context.Background()
	f := newResetCountFixture(t, true)
	today := timezone.StartOfDay(time.Now())
	_, err := integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET daily_limit_usd=NULL,monthly_limit_usd=NULL,
		reset_counted_at=$1,expires_at=$2,status='expired' WHERE id=$3`, today.AddDate(0, 0, -35), today.AddDate(0, 0, -1).Add(12*time.Hour), f.sub.ID)
	require.NoError(t, err)
	require.NoError(t, f.repo.(service.ScheduledSubscriptionResetCounter).RefreshScheduledResetCounts(ctx, time.Now()))
	got, err := f.repo.GetByID(ctx, f.sub.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{0, 4, 0}, resetCountValues(got))
	require.True(t, got.ResetCountedAt.After(got.ExpiresAt))
}

// 暂停/撤销期间不累计；恢复后不能追补被停用时已经跨过的周期。
func TestSubscriptionScheduledResetCountPostgres_SuspensionAndRestore(t *testing.T) {
	for _, state := range []string{"suspended", "revoked"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			f := newResetCountFixture(t, true)
			seedElapsedResetCounts(t, f.sub.ID)
			_, err := integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET status=$1 WHERE id=$2`, state, f.sub.ID)
			require.NoError(t, err)
			require.NoError(t, f.repo.(service.ScheduledSubscriptionResetCounter).RefreshScheduledResetCounts(ctx, time.Now()))
			got, err := f.repo.GetByID(ctx, f.sub.ID)
			require.NoError(t, err)
			require.Equal(t, []int64{2, 3, 4}, resetCountValues(got))
			require.NoError(t, f.repo.UpdateStatus(ctx, f.sub.ID, service.SubscriptionStatusActive))
			require.NoError(t, f.repo.(service.ScheduledSubscriptionResetCounter).RefreshScheduledResetCounts(ctx, time.Now()))
			got, err = f.repo.GetByID(ctx, f.sub.ID)
			require.NoError(t, err)
			require.Equal(t, []int64{2, 3, 4}, resetCountValues(got))
		})
	}
}

// 升级基线不重算历史；新查询和维护也不能覆盖既有计数。
func TestSubscriptionScheduledResetCountPostgres_CutoverPreservesHistory(t *testing.T) {
	ctx := context.Background()
	f := newResetCountFixture(t, true)
	seedResetCounts(t, f.sub.ID)
	before, err := f.repo.GetByID(ctx, f.sub.ID)
	require.NoError(t, err)
	require.False(t, before.ResetCountedAt.IsZero())
	require.NoError(t, f.repo.(service.ScheduledSubscriptionResetCounter).RefreshScheduledResetCounts(ctx, time.Now()))
	_, err = f.svc.EnsureWindowMaintenance(ctx, before)
	require.NoError(t, err)
	got, err := f.repo.GetByID(ctx, f.sub.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{2, 3, 4}, resetCountValues(got))
}

// 在真实 PostgreSQL 事务内模拟旧版升级，新增水位不能覆盖次数或其他订阅字段。
func TestSubscriptionScheduledResetCountPostgres_MigrationPreservesData(t *testing.T) {
	ctx := context.Background()
	f := newResetCountFixture(t, true)
	seedResetCounts(t, f.sub.ID)
	tx := testTx(t)
	var before, after string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT (to_jsonb(s)-'reset_counted_at')::text FROM user_subscriptions s WHERE id=$1`, f.sub.ID).Scan(&before))
	_, err := tx.ExecContext(ctx, `ALTER TABLE user_subscriptions DROP COLUMN reset_counted_at`)
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/285_subscription_scheduled_reset_counts.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	// 重复执行保持基线不变，不重新归零或累加。
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	var at, baseline time.Time
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT (to_jsonb(s)-'reset_counted_at')::text,reset_counted_at,transaction_timestamp() FROM user_subscriptions s WHERE id=$1`, f.sub.ID).Scan(&after, &at, &baseline))
	require.JSONEq(t, before, after)
	require.True(t, at.Equal(baseline))
	require.NoError(t, tx.Rollback())
}

// 一轮扫描超过一批时仍会处理全部记录，不能永远停留在第一批。
func TestSubscriptionScheduledResetCountPostgres_MultipleBatches(t *testing.T) {
	ctx := context.Background()
	f := newResetCountFixture(t, false)
	today := timezone.StartOfDay(time.Now())
	start := time.Now().AddDate(0, 0, -35).Truncate(time.Microsecond)
	const rows = subscriptionResetCountBatchSize + 5
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO user_subscriptions
		(user_id,plan_id,starts_at,expires_at,status,daily_limit_usd,weekly_limit_usd,monthly_limit_usd,reset_counted_at)
		SELECT $1,$2,$3,$4,'active',100,100,100,$3 FROM generate_series(1,$5)`, f.sub.UserID, f.sub.PlanID, start, today.AddDate(0, 0, 90), rows)
	require.NoError(t, err)
	require.NoError(t, f.repo.(service.ScheduledSubscriptionResetCounter).RefreshScheduledResetCounts(ctx, time.Now()))
	var updated int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM user_subscriptions WHERE user_id=$1
		AND daily_reset_count=35 AND weekly_reset_count=5 AND monthly_reset_count=1`, f.sub.UserID).Scan(&updated))
	require.Equal(t, rows, updated)
}

// 删除前结清次数，普通撤销仍为软删除；内部显式清理入口继续允许物理删除。
func TestSubscriptionScheduledResetCountPostgres_DeletePreservesSemantics(t *testing.T) {
	ctx := context.Background()
	f := newResetCountFixture(t, true)
	seedElapsedResetCounts(t, f.sub.ID)
	require.NoError(t, f.repo.Delete(ctx, f.sub.ID))
	deleted, err := f.repo.GetByIDIncludeDeleted(ctx, f.sub.ID)
	require.NoError(t, err)
	require.NotNil(t, deleted.DeletedAt)
	require.Equal(t, []int64{37, 8, 5}, resetCountValues(deleted))
	require.NoError(t, f.repo.Delete(ctx, f.sub.ID))
	require.NoError(t, f.repo.Delete(mixins.SkipSoftDelete(ctx), f.sub.ID))
	_, err = f.repo.GetByIDIncludeDeleted(ctx, f.sub.ID)
	require.ErrorIs(t, err, service.ErrSubscriptionNotFound)
}
