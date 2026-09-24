//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// resetCountFixture 仅使用隔离 PostgreSQL，为维护与实际账务共用同一个订阅提供夹具。
type resetCountFixture struct {
	sub     *service.UserSubscription
	key     *service.APIKey
	repo    service.UserSubscriptionRepository
	svc     *service.SubscriptionService
	billing service.UsageBillingRepository
}

func newResetCountFixture(t *testing.T, activeWindow bool) resetCountFixture {
	t.Helper()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: "reset-count-" + uuid.NewString() + "@example.invalid", Balance: 100})
	plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "自动重置计数", Price: 10, ValidityDays: 90,
		DailyLimitUSD: float64Ptr(100), WeeklyLimitUSD: float64Ptr(100), MonthlyLimitUSD: float64Ptr(100)})
	today := timezone.StartOfDay(time.Now())
	sub := &service.UserSubscription{UserID: user.ID, PlanID: plan.ID, StartsAt: today.AddDate(0, 0, -60), ExpiresAt: today.AddDate(0, 0, 90),
		DailyLimitUSD: float64Ptr(100), WeeklyLimitUSD: float64Ptr(100), MonthlyLimitUSD: float64Ptr(100)}
	if activeWindow {
		old := today.AddDate(0, 0, -35)
		sub.DailyWindowStart, sub.WeeklyWindowStart, sub.MonthlyWindowStart = &old, &old, &old
	}
	mustCreateSubscription(t, client, sub)
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-reset-count-" + uuid.NewString(), Name: "自动计数"})
	repo := NewUserSubscriptionRepository(client)
	return resetCountFixture{sub: sub, key: key, repo: repo,
		svc: service.NewSubscriptionService(nil, repo, nil, client, nil), billing: NewUsageBillingRepository(client, integrationDB)}
}

func resetCountValues(sub *service.UserSubscription) []int64 {
	return []int64{sub.DailyResetCount, sub.WeeklyResetCount, sub.MonthlyResetCount}
}

func resetCountCommand(f resetCountFixture, id string, amount float64) *service.UsageBillingCommand {
	return &service.UsageBillingCommand{RequestID: id, APIKeyID: f.key.ID, UserID: f.sub.UserID,
		APIKeyBillingMode: service.APIKeyBillingModeSubscription, PreferredSubscriptionID: &f.sub.ID, BillableAmountUSD: amount}
}

func seedResetCounts(t *testing.T, id int64) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE user_subscriptions SET daily_reset_count=2,weekly_reset_count=3,monthly_reset_count=4 WHERE id=$1`, id)
	require.NoError(t, err)
}

// 以持久化水位模拟停机或未使用，已经到达的每个合法周期均应计次。
func seedElapsedResetCounts(t *testing.T, id int64) {
	t.Helper()
	seedResetCounts(t, id)
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE user_subscriptions SET reset_counted_at=$1 WHERE id=$2`, timezone.StartOfDay(time.Now()).AddDate(0, 0, -35), id)
	require.NoError(t, err)
}

// 即使用量为零也补齐跨过的所有周期，手动重置保留已到点次数。
func TestSubscriptionResetCountPostgres_AutomaticAndManual(t *testing.T) {
	for _, used := range []float64{0, 9} {
		t.Run(fmt.Sprint(used), func(t *testing.T) {
			ctx := context.Background()
			f := newResetCountFixture(t, true)
			seedElapsedResetCounts(t, f.sub.ID)
			_, err := integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET daily_usage_usd=$1,weekly_usage_usd=$1,monthly_usage_usd=$1 WHERE id=$2`, used, f.sub.ID)
			require.NoError(t, err)
			before, err := f.repo.GetByID(ctx, f.sub.ID)
			require.NoError(t, err)
			oldSnapshot := *before
			got, err := f.svc.EnsureWindowMaintenance(ctx, before)
			require.NoError(t, err)
			require.Equal(t, []int64{37, 8, 5}, resetCountValues(got))
			require.Zero(t, got.DailyUsageUSD)
			require.Zero(t, got.WeeklyUsageUSD)
			require.Zero(t, got.MonthlyUsageUSD)
			// 再次提交旧窗口快照模拟 CAS 竞争失败，不能重复计数或清掉新扣费。
			require.NoError(t, f.svc.RecordUsage(ctx, f.sub.ID, 1.25))
			stale, err := f.svc.EnsureWindowMaintenance(ctx, &oldSnapshot)
			require.NoError(t, err)
			require.Equal(t, []int64{37, 8, 5}, resetCountValues(stale))
			require.Equal(t, 1.25, stale.MonthlyUsageUSD)
			manual, err := f.svc.AdminResetQuota(ctx, f.sub.ID, true, true, true)
			require.NoError(t, err)
			require.Equal(t, []int64{37, 8, 5}, resetCountValues(manual))
			require.Zero(t, manual.MonthlyUsageUSD)
		})
	}
}

// 服务首次激活和扣费事务首次初始化窗口均不算自动重置。
func TestSubscriptionResetCountPostgres_FirstActivation(t *testing.T) {
	for _, path := range []string{"service", "billing"} {
		t.Run(path, func(t *testing.T) {
			ctx := context.Background()
			f := newResetCountFixture(t, false)
			if path == "service" {
				_, err := f.svc.EnsureWindowMaintenance(ctx, f.sub)
				require.NoError(t, err)
			} else {
				result, err := f.billing.Apply(ctx, resetCountCommand(f, uuid.NewString(), .25))
				require.NoError(t, err)
				require.Equal(t, .25, result.SubscriptionAmountUSD)
			}
			got, err := f.repo.GetByID(ctx, f.sub.ID)
			require.NoError(t, err)
			require.Equal(t, []int64{0, 0, 0}, resetCountValues(got))
			require.NotNil(t, got.DailyWindowStart)
			require.NotNil(t, got.WeeklyWindowStart)
			require.NotNil(t, got.MonthlyWindowStart)
		})
	}
}

// 临期尾段仍按新规则推进，延期保留已经建立的窗口及累计次数。
func TestSubscriptionResetCountPostgres_ExtensionBoundary(t *testing.T) {
	for _, window := range []struct {
		kind string
		days int
	}{{"daily", 1}, {"weekly", 7}, {"monthly", 30}} {
		for _, operation := range []string{"extend", "set_validity", "bulk_extend"} {
			for _, stale := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stale_%v", window.kind, operation, stale), func(t *testing.T) {
					ctx := context.Background()
					f := newResetCountFixture(t, stale)
					today := timezone.StartOfDay(time.Now())
					// 只保留本窗口额度，确认尾段无需更高层额度也能启动。
					_, err := integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET daily_limit_usd=NULL,weekly_limit_usd=NULL,monthly_limit_usd=NULL,
						daily_window_start=NULL,weekly_window_start=NULL,monthly_window_start=NULL,expires_at=$1 WHERE id=$2`, today.AddDate(0, 0, 1).Add(-time.Microsecond), f.sub.ID)
					require.NoError(t, err)
					_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`UPDATE user_subscriptions SET %s_limit_usd=100 WHERE id=$1`, window.kind), f.sub.ID)
					require.NoError(t, err)
					if stale {
						_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`UPDATE user_subscriptions SET %s_window_start=$1 WHERE id=$2`, window.kind), today.AddDate(0, 0, -35), f.sub.ID)
						require.NoError(t, err)
					}
					seedResetCounts(t, f.sub.ID)
					before, err := f.repo.GetByID(ctx, f.sub.ID)
					require.NoError(t, err)
					before, err = f.svc.EnsureWindowMaintenance(ctx, before)
					require.NoError(t, err)
					require.Equal(t, []int64{2, 3, 4}, resetCountValues(before))
					beforeAnchor, _ := focusPersistedWindow(before, window.kind)
					require.NotNil(t, beforeAnchor)
					switch operation {
					case "extend":
						_, err = f.svc.ExtendSubscription(ctx, f.sub.ID, window.days*2+2)
					case "set_validity":
						_, err = f.svc.SetSubscriptionValidityDays(ctx, f.sub.ID, window.days*2+2)
					default:
						_, err = f.svc.BulkExtendSubscriptions(ctx, []int64{f.sub.ID, f.sub.ID}, window.days*2+2)
					}
					require.NoError(t, err)
					got, err := f.repo.GetByID(ctx, f.sub.ID)
					require.NoError(t, err)
					expected := []int64{2, 3, 4}
					// 延期不会重建已生效窗口，也不会清零或重复增加次数。
					require.Equal(t, expected, resetCountValues(got))
					anchor, _ := focusPersistedWindow(got, window.kind)
					require.NotNil(t, anchor)
					require.True(t, anchor.Equal(*beforeAnchor))
				})
			}
		}
	}
}

// 用户续期创建新记录；管理员编辑或延期现有记录均不能将旧次数覆盖为零。
func TestSubscriptionResetCountPostgres_RenewalAndAdminEdits(t *testing.T) {
	ctx := context.Background()
	f := newResetCountFixture(t, false)
	seedResetCounts(t, f.sub.ID)
	current, err := f.svc.EnsureWindowMaintenance(ctx, f.sub)
	require.NoError(t, err)
	renewed, queued, err := f.svc.AssignOrExtendSubscription(ctx, &service.AssignSubscriptionInput{UserID: f.sub.UserID, PlanID: f.sub.PlanID})
	require.NoError(t, err)
	require.True(t, queued)
	require.NotEqual(t, current.ID, renewed.ID)
	require.True(t, renewed.StartsAt.Equal(current.ExpiresAt))
	require.Equal(t, []int64{0, 0, 0}, resetCountValues(renewed))
	// 模拟编辑器持有的旧计数，通用更新只能保留数据库已累计的值。
	current.DailyResetCount, current.WeeklyResetCount, current.MonthlyResetCount = 0, 0, 0
	current.Notes = "管理员修改备注"
	require.NoError(t, f.repo.Update(ctx, current))
	require.Equal(t, []int64{2, 3, 4}, resetCountValues(current))
	extended, err := f.svc.ExtendSubscription(ctx, current.ID, 2)
	require.NoError(t, err)
	require.Equal(t, []int64{2, 3, 4}, resetCountValues(extended))
	next, err := f.repo.GetByID(ctx, renewed.ID)
	require.NoError(t, err)
	require.True(t, next.StartsAt.Equal(extended.ExpiresAt))
	require.Equal(t, []int64{0, 0, 0}, resetCountValues(next))
}

// 六十个并发操作混合网关维护、后台扫描和真实扣费，跨过的周期只统计一次，费用不得丢失。
func TestSubscriptionResetCountPostgres_ConcurrentMaintenanceAndBilling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newResetCountFixture(t, true)
	seedElapsedResetCounts(t, f.sub.ID)
	snapshot, err := f.repo.GetByID(ctx, f.sub.ID)
	require.NoError(t, err)
	const concurrency = 60
	// 去重账本不随测试夹具清理；每轮使用独立前缀，避免 -count 多轮重用主键后命中旧请求。
	requestPrefix := "reset-concurrent-" + uuid.NewString() + "-"
	start := make(chan struct{})
	errors := make(chan error, concurrency)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			if index%4 == 0 {
				errors <- f.repo.(service.ScheduledSubscriptionResetCounter).RefreshScheduledResetCounts(ctx, time.Now())
			} else if index%2 == 0 {
				copy := *snapshot
				_, err := f.svc.EnsureWindowMaintenance(ctx, &copy)
				errors <- err
			} else {
				result, err := f.billing.Apply(ctx, resetCountCommand(f, fmt.Sprintf("%s%d", requestPrefix, index), .125))
				if err == nil && (!result.Applied || result.SubscriptionAmountUSD != .125 || result.BalanceAmountUSD != 0) {
					err = fmt.Errorf("扣费结果异常: %+v", result)
				}
				errors <- err
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	got, err := f.repo.GetByID(ctx, f.sub.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{37, 8, 5}, resetCountValues(got))
	for _, usage := range []float64{got.DailyUsageUSD, got.WeeklyUsageUSD, got.MonthlyUsageUSD} {
		require.Equal(t, float64(concurrency/2)*.125, usage)
	}
	replay, err := f.billing.Apply(ctx, resetCountCommand(f, requestPrefix+"1", .125))
	require.NoError(t, err)
	require.False(t, replay.Applied)
	again, err := f.repo.GetByID(ctx, f.sub.ID)
	require.NoError(t, err)
	require.Equal(t, resetCountValues(got), resetCountValues(again))
	require.Equal(t, got.MonthlyUsageUSD, again.MonthlyUsageUSD)
}

// 只读倍率候选解析不持久化计数，事务回滚也不能留下已刷新次数。
func TestSubscriptionResetCountPostgres_BillingReadAndRollback(t *testing.T) {
	ctx := context.Background()
	f := newResetCountFixture(t, true)
	seedElapsedResetCounts(t, f.sub.ID)
	group := mustCreateGroup(t, integrationEntClient, &service.Group{Name: "计数查询-" + uuid.NewString(), Platform: service.PlatformOpenAI})
	resolved, err := f.billing.(*usageBillingRepository).ResolveUsableSubscriptionForGroup(ctx, f.sub.UserID, group.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{37, 8, 5}, resetCountValues(resolved))
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	remaining, billed, _, err := allocateUsageBillingSubscriptions(ctx, tx, resetCountCommand(f, "rollback", .25))
	require.NoError(t, err)
	require.Zero(t, remaining)
	require.Equal(t, .25, billed)
	var count int64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT monthly_reset_count FROM user_subscriptions WHERE id=$1`, f.sub.ID).Scan(&count))
	require.Equal(t, int64(5), count)
	require.NoError(t, tx.Rollback())
	got, err := f.repo.GetByID(ctx, f.sub.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{2, 3, 4}, resetCountValues(got))
	require.Equal(t, f.sub.MonthlyWindowStart, got.MonthlyWindowStart)
	require.Zero(t, got.MonthlyUsageUSD)
}

// 批量预占不足时整笔回滚，成功预占的自动刷新则不随任务取消而撤销计数。
func TestSubscriptionResetCountPostgres_BatchReservationAndRelease(t *testing.T) {
	ctx := context.Background()
	f := newResetCountFixture(t, true)
	seedElapsedResetCounts(t, f.sub.ID)
	reservedAt := time.Now().UTC()
	batchID := "imgbatch_" + uuid.NewString()
	insertBatchImageAllowanceTestJob(t, batchID, f.sub.UserID, f.sub.UserID, f.key.ID, nil, reservedAt)
	cmd := &service.BatchImageBalanceHoldCommand{RequestID: service.BatchImageHoldRequestID(batchID), BatchID: batchID,
		APIKeyID: f.key.ID, UserID: f.sub.UserID, ActorUserID: f.sub.UserID,
		APIKeyBillingMode: service.APIKeyBillingModeSubscription, PreferredSubscriptionID: &f.sub.ID, HoldAmount: 101, ReservedAt: reservedAt}
	_, err := f.billing.ReserveBatchImageBalance(ctx, cmd)
	require.ErrorIs(t, err, service.ErrPreferredSubscriptionInsufficient)
	failed, err := f.repo.GetByID(ctx, f.sub.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{2, 3, 4}, resetCountValues(failed))
	require.Zero(t, failed.MonthlyUsageUSD)
	cmd.HoldAmount, cmd.RequestFingerprint = .75, ""
	reserved, err := f.billing.ReserveBatchImageBalance(ctx, cmd)
	require.NoError(t, err)
	require.Equal(t, .75, reserved.SubscriptionAmountUSD)
	current, err := f.repo.GetByID(ctx, f.sub.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{37, 8, 5}, resetCountValues(current))
	require.Equal(t, .75, current.MonthlyUsageUSD)
	release := *cmd
	release.RequestID, release.RequestFingerprint = service.BatchImageReleaseRequestID(batchID), ""
	release.AllowanceReserved = true
	release.SubscriptionHoldAllocations = reserved.BillingAllocations
	_, err = f.billing.ReleaseBatchImageBalance(ctx, &release)
	require.NoError(t, err)
	final, err := f.repo.GetByID(ctx, f.sub.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{37, 8, 5}, resetCountValues(final))
	require.Zero(t, final.MonthlyUsageUSD)
}
