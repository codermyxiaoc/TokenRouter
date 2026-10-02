//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// videoBillingFixture 仅使用隔离 PostgreSQL 中的虚构账号，不访问视频上游。
type videoBillingFixture struct {
	t                                 *testing.T
	repo                              service.UsageBillingRepository
	tasks                             service.VideoTaskRepository
	userID, keyID, groupID, accountID int64
}

func newVideoBillingFixture(t *testing.T) *videoBillingFixture {
	t.Helper()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: "video-billing-" + uuid.NewString() + "@example.com", Balance: 1000})
	group := mustCreateGroup(t, client, &service.Group{Name: "video-" + uuid.NewString(), Platform: service.PlatformVideo, RateMultiplier: 1})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-video-" + uuid.NewString(), Name: "video-billing", GroupID: &group.ID, Quota: 1000, RateLimit5h: 1000, RateLimit1d: 1000, RateLimit7d: 1000})
	account := mustCreateAccount(t, client, &service.Account{Name: "video-" + uuid.NewString(), Platform: service.PlatformVideo, Type: service.AccountTypeAPIKey})
	return &videoBillingFixture{t: t, repo: NewUsageBillingRepository(client, integrationDB), tasks: NewVideoTaskRepository(integrationDB), userID: user.ID, keyID: key.ID, groupID: group.ID, accountID: account.ID}
}

func (f *videoBillingFixture) task(base, subRate, balanceRate float64) *service.VideoTaskRecord {
	f.t.Helper()
	id := "video_" + uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	record := &service.VideoTaskRecord{ID: id, UserID: f.userID, APIKeyID: f.keyID, GroupID: f.groupID, AccountID: f.accountID, PayloadHash: uuid.NewString(), Status: "prepared", BillingStatus: "pending", CreatedAt: now, LeaseToken: uuid.NewString(), LeaseUntil: now.Add(time.Minute), Target: service.VideoUpstreamTarget{Endpoint: service.VideoEndpointSeedance}}
	record.Hold = service.BatchImageBalanceHoldCommand{RequestID: "video_hold:" + id, BatchID: id, UserID: f.userID, ActorUserID: f.userID, APIKeyID: f.keyID, GroupID: &f.groupID, APIKeyBillingMode: service.APIKeyBillingModeAuto, PricingSnapshotVersion: 3, BaseAmountUSD: base, SubscriptionRateMultiplier: subRate, SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: balanceRate, SettlementRateScale: 1, ReservedAt: now, VideoEntity: true, VideoAccountID: f.accountID}
	record.Hold.VideoLeaseToken = record.LeaseToken
	_, created, err := f.tasks.Create(context.Background(), record, 1000)
	require.NoError(f.t, err)
	require.True(f.t, created)
	return record
}

func (f *videoBillingFixture) reserve(record *service.VideoTaskRecord) *service.VideoTaskRecord {
	f.t.Helper()
	result, err := f.repo.ReserveBatchImageBalance(context.Background(), &record.Hold)
	require.NoError(f.t, err)
	require.True(f.t, result.Applied)
	fresh, err := f.tasks.Get(context.Background(), record.ID)
	require.NoError(f.t, err)
	return fresh
}

// command 模拟工作进程已提交终态后，从权威持久快照构建财务命令。
func (f *videoBillingFixture) command(record *service.VideoTaskRecord, status string, actualBase float64) service.BatchImageBalanceHoldCommand {
	f.t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), "UPDATE video_tasks SET status=$2 WHERE id=$1", record.ID, status)
	require.NoError(f.t, err)
	cmd := record.Hold
	cmd.RequestFingerprint = ""
	cmd.ActualBaseAmountUSD = actualBase
	cmd.VideoAccountQuotaCost = actualBase
	if status == "completed" {
		cmd.RequestID = "video_capture:" + record.ID
	} else {
		cmd.RequestID = "video_release:" + record.ID
	}
	return cmd
}

func (f *videoBillingFixture) money(balance, frozen, keyUsage, accountCost float64) {
	f.t.Helper()
	var b, h, q, a float64
	require.NoError(f.t, integrationDB.QueryRowContext(context.Background(), "SELECT balance,frozen_balance FROM users WHERE id=$1", f.userID).Scan(&b, &h))
	require.NoError(f.t, integrationDB.QueryRowContext(context.Background(), "SELECT quota_used FROM api_keys WHERE id=$1", f.keyID).Scan(&q))
	require.NoError(f.t, integrationDB.QueryRowContext(context.Background(), "SELECT COALESCE((extra->>'quota_used')::numeric,0) FROM accounts WHERE id=$1", f.accountID).Scan(&a))
	require.InDelta(f.t, balance, b, 1e-8)
	require.InDelta(f.t, frozen, h, 1e-8)
	require.InDelta(f.t, keyUsage, q, 1e-8)
	require.InDelta(f.t, accountCost, a, 1e-8)
}

func (f *videoBillingFixture) quota(limit float64) {
	f.t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `INSERT INTO user_platform_quotas(user_id,platform,daily_limit_usd,weekly_limit_usd,monthly_limit_usd) VALUES($1,'video',$2,$2,$2)`, f.userID, limit)
	require.NoError(f.t, err)
}

func (f *videoBillingFixture) quotaUsage(daily, weekly, monthly float64) {
	f.t.Helper()
	var d, w, m float64
	require.NoError(f.t, integrationDB.QueryRowContext(context.Background(), `SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_platform_quotas WHERE user_id=$1 AND platform='video'`, f.userID).Scan(&d, &w, &m))
	require.InDelta(f.t, daily, d, 1e-8)
	require.InDelta(f.t, weekly, w, 1e-8)
	require.InDelta(f.t, monthly, m, 1e-8)
}

func TestVideoTaskBillingConcurrentDistinctTasks(t *testing.T) {
	f := newVideoBillingFixture(t)
	f.quota(1000)
	const count = 60
	tasks := make([]*service.VideoTaskRecord, count)
	for i := range tasks {
		tasks[i] = f.task(2, 1, 1)
	}
	var wg sync.WaitGroup
	errs := make(chan error, count)
	// 不同任务同付款人的预占和捕获交错执行，最终应只收实际费用。
	for _, record := range tasks {
		wg.Add(1)
		go func(record *service.VideoTaskRecord) {
			defer wg.Done()
			ctx := context.Background()
			if _, err := f.repo.ReserveBatchImageBalance(ctx, &record.Hold); err != nil {
				errs <- err
				return
			}
			fresh, err := f.tasks.Get(ctx, record.ID)
			if err != nil {
				errs <- err
				return
			}
			if _, err = integrationDB.ExecContext(ctx, "UPDATE video_tasks SET status='completed' WHERE id=$1", record.ID); err != nil {
				errs <- err
				return
			}
			cmd := fresh.Hold
			cmd.RequestID = "video_capture:" + record.ID
			cmd.RequestFingerprint = ""
			cmd.ActualBaseAmountUSD = 0.75
			cmd.VideoAccountQuotaCost = 0.5
			_, err = f.repo.CaptureBatchImageBalance(ctx, &cmd)
			errs <- err
		}(record)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	f.money(955, 0, 45, 30)
	f.quotaUsage(45, 45, 45)
	var settled int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT count(*) FROM video_tasks WHERE user_id=$1 AND billing_status='settled'", f.userID).Scan(&settled))
	require.Equal(t, count, settled)
}

func TestVideoTaskBillingConcurrentCaptureReleaseAndRetry(t *testing.T) {
	for _, status := range []string{"completed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(100)
			record := f.reserve(f.task(10, 1, 1))
			cmd := f.command(record, status, 4)
			capture, release := cmd, cmd
			capture.RequestID = "video_capture:" + record.ID
			release.RequestID = "video_release:" + record.ID
			var wg sync.WaitGroup
			var captures, releases atomic.Int64
			errs := make(chan error, 40)
			for i := 0; i < 20; i++ {
				wg.Add(2)
				go func() {
					defer wg.Done()
					local := capture
					result, err := f.repo.CaptureBatchImageBalance(context.Background(), &local)
					if err == nil && result.Applied {
						captures.Add(1)
					}
					if status == "completed" {
						errs <- err
					} else if err != service.ErrVideoTaskConflict {
						errs <- fmt.Errorf("capture must conflict: %v", err)
					}
				}()
				go func() {
					defer wg.Done()
					local := release
					result, err := f.repo.ReleaseBatchImageBalance(context.Background(), &local)
					if err == nil && result.Applied {
						releases.Add(1)
					}
					if status == "cancelled" {
						errs <- err
					} else if err != service.ErrVideoTaskConflict {
						errs <- fmt.Errorf("release must conflict: %v", err)
					}
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			if status == "completed" {
				require.EqualValues(t, 1, captures.Load())
				require.Zero(t, releases.Load())
				f.money(996, 0, 4, 4)
				f.quotaUsage(4, 4, 4)
			} else {
				require.EqualValues(t, 1, releases.Load())
				require.Zero(t, captures.Load())
				f.money(1000, 0, 0, 0)
				f.quotaUsage(0, 0, 0)
			}
		})
	}
}

func TestVideoTaskBillingQuotaAtomicLimitAndOriginalWindows(t *testing.T) {
	f := newVideoBillingFixture(t)
	f.quota(10)
	record := f.reserve(f.task(8, 1, 1))
	f.money(992, 8, 8, 0)
	f.quotaUsage(8, 8, 8)
	overflow := f.task(3, 1, 1)
	_, err := f.repo.ReserveBatchImageBalance(context.Background(), &overflow.Hold)
	require.ErrorIs(t, err, service.ErrUserPlatformDailyQuotaExhausted)
	f.money(992, 8, 8, 0)
	f.quotaUsage(8, 8, 8)
	// 管理员重置日额度，周/月仍为原窗口；迟到退款不能减少新日窗口用量。
	_, err = integrationDB.ExecContext(context.Background(), `UPDATE user_platform_quotas SET daily_window_start=daily_window_start+INTERVAL '1 day',daily_usage_usd=6 WHERE user_id=$1 AND platform='video'`, f.userID)
	require.NoError(t, err)
	cmd := f.command(record, "completed", 3)
	_, err = f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
	require.NoError(t, err)
	f.money(997, 0, 3, 3)
	f.quotaUsage(6, 3, 3)
}

func TestVideoTaskBillingSubscriptionRateSnapshot(t *testing.T) {
	f := newVideoBillingFixture(t)
	f.quota(100)
	client := integrationEntClient
	plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "video-plan-" + uuid.NewString(), ValidityDays: 90, ValidityUnit: "day", GroupIDs: []int64{f.groupID}, GroupRateMultipliers: map[int64]float64{f.groupID: 0.5}})
	start := time.Now().UTC().Add(-time.Hour)
	sub := mustCreateSubscription(t, client, &service.UserSubscription{UserID: f.userID, PlanID: plan.ID, StartsAt: start, ExpiresAt: start.Add(90 * 24 * time.Hour), DailyWindowStart: &start, WeeklyWindowStart: &start, MonthlyWindowStart: &start, DailyLimitUSD: float64Ptr(2), WeeklyLimitUSD: float64Ptr(2), MonthlyLimitUSD: float64Ptr(2)})
	record := f.reserve(f.task(10, 2, 3))
	require.InDelta(t, 18, record.Hold.BalanceHoldAmount, 1e-8)
	require.Len(t, record.Hold.SubscriptionHoldAllocations, 1)
	require.InDelta(t, 0.5, record.Hold.SubscriptionHoldAllocations[0].RateMultiplier, 1e-8)
	f.quotaUsage(18, 18, 18)
	// 任务运行期间修改套餐倍率；结算仍使用预占时的各来源倍率。
	_, err := integrationDB.ExecContext(context.Background(), "UPDATE subscription_plan_groups SET rate_multiplier=9 WHERE plan_id=$1", plan.ID)
	require.NoError(t, err)
	cmd := f.command(record, "completed", 6)
	result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
	require.NoError(t, err)
	require.InDelta(t, 2, result.SubscriptionAmountUSD, 1e-8)
	require.InDelta(t, 6, result.BalanceAmountUSD, 1e-8)
	f.money(994, 0, 8, 6)
	f.quotaUsage(6, 6, 6)
	var used float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT daily_usage_usd FROM user_subscriptions WHERE id=$1", sub.ID).Scan(&used))
	require.InDelta(t, 2, used, 1e-8)
}

func TestVideoTaskBillingNewSubscriptionWindowsRefund(t *testing.T) {
	f := newVideoBillingFixture(t)
	plan := mustCreatePlan(t, integrationEntClient, &service.SubscriptionPlan{Name: "video-empty-window-" + uuid.NewString(), ValidityDays: 90, ValidityUnit: "day", GroupIDs: []int64{f.groupID}})
	sub := mustCreateSubscription(t, integrationEntClient, &service.UserSubscription{UserID: f.userID, PlanID: plan.ID, ExpiresAt: time.Now().Add(90 * 24 * time.Hour), DailyLimitUSD: float64Ptr(100), WeeklyLimitUSD: float64Ptr(100), MonthlyLimitUSD: float64Ptr(100)})
	record := f.reserve(f.task(8, 1, 1))
	cmd := f.command(record, "cancelled", 0)
	_, err := f.repo.ReleaseBatchImageBalance(context.Background(), &cmd)
	require.NoError(t, err)
	var d, w, m float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE id=$1", sub.ID).Scan(&d, &w, &m))
	require.Zero(t, d)
	require.Zero(t, w)
	require.Zero(t, m)
	f.money(1000, 0, 0, 0)
}

func TestVideoTaskBillingSubscriptionRefundPreservesNewWindows(t *testing.T) {
	f := newVideoBillingFixture(t)
	plan := mustCreatePlan(t, integrationEntClient, &service.SubscriptionPlan{Name: "video-reset-" + uuid.NewString(), ValidityDays: 90, ValidityUnit: "day", GroupIDs: []int64{f.groupID}})
	start := time.Now().UTC().Add(-time.Hour)
	sub := mustCreateSubscription(t, integrationEntClient, &service.UserSubscription{UserID: f.userID, PlanID: plan.ID, StartsAt: start, ExpiresAt: start.Add(90 * 24 * time.Hour), DailyWindowStart: &start, WeeklyWindowStart: &start, MonthlyWindowStart: &start, DailyLimitUSD: float64Ptr(100), WeeklyLimitUSD: float64Ptr(100), MonthlyLimitUSD: float64Ptr(100)})
	record := f.reserve(f.task(8, 1, 1))
	// 日/月已经刷新或被管理员重置，只有保持原窗口的周用量允许退款。
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE user_subscriptions SET daily_window_start=daily_window_start+INTERVAL '1 day',daily_usage_usd=5,monthly_window_start=monthly_window_start+INTERVAL '30 days',monthly_usage_usd=7 WHERE id=$1`, sub.ID)
	require.NoError(t, err)
	cmd := f.command(record, "cancelled", 0)
	_, err = f.repo.ReleaseBatchImageBalance(context.Background(), &cmd)
	require.NoError(t, err)
	var d, w, m float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE id=$1", sub.ID).Scan(&d, &w, &m))
	require.Equal(t, 5.0, d)
	require.Zero(t, w)
	require.Equal(t, 7.0, m)
	f.money(1000, 0, 0, 0)
}

func TestVideoTaskBillingReconciliationKeepsBudgetUntilReliableCapture(t *testing.T) {
	f := newVideoBillingFixture(t)
	f.quota(100)
	record := f.reserve(f.task(8, 1, 1))
	cmd := f.command(record, "completed", 9)
	_, err := integrationDB.ExecContext(context.Background(), "UPDATE video_tasks SET billing_status='reconciliation' WHERE id=$1", record.ID)
	require.NoError(t, err)
	_, err = f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
	require.ErrorIs(t, err, service.ErrBatchImageSettlementCostExceedsHold)
	f.money(992, 8, 8, 0)
	f.quotaUsage(8, 8, 8)
	// 超预留的失败不能占用幂等键；拿到可靠用量后可按原价正常结算。
	cmd.ActualBaseAmountUSD, cmd.VideoAccountQuotaCost, cmd.RequestFingerprint = 3, 3, ""
	result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	f.money(997, 0, 3, 3)
	f.quotaUsage(3, 3, 3)
	cmd.ActualBaseAmountUSD, cmd.RequestFingerprint = 2, ""
	_, err = f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
	require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)
	f.money(997, 0, 3, 3)
}

func TestVideoTaskBillingExpiredSubmitCannotReserveAfterTakeover(t *testing.T) {
	for _, takeover := range []bool{false, true} {
		t.Run(fmt.Sprintf("takeover=%t", takeover), func(t *testing.T) {
			f := newVideoBillingFixture(t)
			record := f.task(8, 1, 1)
			_, err := integrationDB.ExecContext(context.Background(), "UPDATE video_tasks SET lease_until=NOW()-INTERVAL '1 second' WHERE id=$1", record.ID)
			require.NoError(t, err)
			if takeover {
				claimed, err := f.tasks.Claim(context.Background(), record.ID, time.Minute)
				require.NoError(t, err)
				require.NotEqual(t, record.LeaseToken, claimed.LeaseToken)
			}
			// 原提交者即使还持有完整预算命令，也不能在租约失效或被接管后冻结资金。
			_, err = f.repo.ReserveBatchImageBalance(context.Background(), &record.Hold)
			require.ErrorIs(t, err, service.ErrVideoTaskConflict)
			f.money(1000, 0, 0, 0)
			fresh, err := f.tasks.Get(context.Background(), record.ID)
			require.NoError(t, err)
			require.Equal(t, "pending", fresh.BillingStatus)
			require.False(t, fresh.Hold.AllowanceReserved)
			// 原 token 也不能借 Save 延长失效租约或覆盖接管后的执行者。
			record.Status = "submitting"
			require.ErrorIs(t, f.tasks.Save(context.Background(), record, false), service.ErrVideoTaskConflict)
		})
	}
}

func TestVideoTaskBillingStalePendingSnapshotCannotHideReservedBudget(t *testing.T) {
	f := newVideoBillingFixture(t)
	record := f.task(8, 1, 1)
	stale := *record
	fresh := f.reserve(record)
	// 精确模拟读取 pending 后，另一事务完成 reserved，再持旧快照宣称已释放的时序。
	stale.Status, stale.BillingStatus, stale.EffectsDone = "failed", "released", true
	require.ErrorIs(t, f.tasks.Save(context.Background(), &stale, true), service.ErrVideoTaskConflict)
	f.money(992, 8, 8, 0)
	persisted, err := f.tasks.Get(context.Background(), record.ID)
	require.NoError(t, err)
	require.Equal(t, "reserved", persisted.BillingStatus)
	require.Equal(t, "prepared", persisted.Status)
	require.False(t, persisted.EffectsDone)
	// 真正的资金退款事务仍可安全执行；普通 Save 无法伪造账务终态。
	cmd := f.command(fresh, "cancelled", 0)
	_, err = f.repo.ReleaseBatchImageBalance(context.Background(), &cmd)
	require.NoError(t, err)
	f.money(1000, 0, 0, 0)
}

// videoResetTestSubscription 保证日锚点已经是当天零点，手动重置无法靠时间戳区分新旧用量。
func videoResetTestSubscription(t *testing.T, f *videoBillingFixture) *service.UserSubscription {
	t.Helper()
	plan := mustCreatePlan(t, integrationEntClient, &service.SubscriptionPlan{Name: "video-reset-generation-" + uuid.NewString(), ValidityDays: 90, ValidityUnit: "day", GroupIDs: []int64{f.groupID}})
	now := time.Now().UTC()
	day, previous := timezone.StartOfDay(now), now.Add(-time.Minute)
	return mustCreateSubscription(t, integrationEntClient, &service.UserSubscription{UserID: f.userID, PlanID: plan.ID, StartsAt: previous, ExpiresAt: now.Add(90 * 24 * time.Hour), DailyWindowStart: &day, WeeklyWindowStart: &previous, MonthlyWindowStart: &previous, DailyLimitUSD: float64Ptr(100), WeeklyLimitUSD: float64Ptr(100), MonthlyLimitUSD: float64Ptr(100)})
}

func TestVideoTaskBillingSubscriptionSameDayManualResetGeneration(t *testing.T) {
	for _, terminal := range []string{"cancelled", "completed"} {
		t.Run(terminal, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			sub := videoResetTestSubscription(t, f)
			old := f.reserve(f.task(8, 1, 1))
			var startBefore, startAfter time.Time
			var countBefore, countAfter, generation int64
			require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT daily_window_start,daily_reset_count FROM user_subscriptions WHERE id=$1", sub.ID).Scan(&startBefore, &countBefore))
			// 单条/批量管理员入口均调用真实 ResetUsageWindows，不用直接 SQL 模拟重置。
			repo := NewUserSubscriptionRepository(integrationEntClient)
			require.NoError(t, repo.ResetUsageWindows(context.Background(), sub.ID, true, false, false, time.Now()))
			require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT daily_window_start,daily_reset_count,daily_reset_generation FROM user_subscriptions WHERE id=$1", sub.ID).Scan(&startAfter, &countAfter, &generation))
			require.True(t, startBefore.Equal(startAfter), "手动日重置仍保留当天零点")
			require.Equal(t, countBefore, countAfter, "手动重置不增加既有自然重置次数")
			require.EqualValues(t, 1, generation)
			newer := f.reserve(f.task(5, 1, 1))
			actual := 0.0
			if terminal == "completed" {
				actual = 3
			}
			cmd := f.command(old, terminal, actual)
			var err error
			if terminal == "completed" {
				_, err = f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			} else {
				_, err = f.repo.ReleaseBatchImageBalance(context.Background(), &cmd)
			}
			require.NoError(t, err)
			var daily, weekly, monthly float64
			require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE id=$1", sub.ID).Scan(&daily, &weekly, &monthly))
			require.Equal(t, 5.0, daily, "旧预留不能抵扣重置后的日用量")
			require.Equal(t, 5+actual, weekly)
			require.Equal(t, 5+actual, monthly)
			// 新任务携带新代次，取消时仍应正常退款。
			newCmd := f.command(newer, "cancelled", 0)
			_, err = f.repo.ReleaseBatchImageBalance(context.Background(), &newCmd)
			require.NoError(t, err)
			require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE id=$1", sub.ID).Scan(&daily, &weekly, &monthly))
			require.Zero(t, daily)
			require.Equal(t, actual, weekly)
			require.Equal(t, actual, monthly)
		})
	}
}

func TestVideoTaskBillingPlatformManualResetGeneration(t *testing.T) {
	for windowIndex, window := range []string{"daily", "weekly", "monthly"} {
		for _, terminal := range []string{"cancelled", "completed"} {
			t.Run(window+"/"+terminal, func(t *testing.T) {
				f := newVideoBillingFixture(t)
				f.quota(100)
				old := f.reserve(f.task(8, 1, 1))
				repo := NewUserPlatformQuotaRepository(integrationEntClient)
				require.NoError(t, repo.ResetExpiredWindow(context.Background(), f.userID, service.PlatformVideo, window, time.Now()))
				// 新预留会将日/周起点重新规范化，代次不能因此退回旧值。
				newer := f.reserve(f.task(5, 1, 1))
				var generations [3]int64
				require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT daily_reset_generation,weekly_reset_generation,monthly_reset_generation FROM user_platform_quotas WHERE user_id=$1 AND platform='video'", f.userID).Scan(&generations[0], &generations[1], &generations[2]))
				expectedGenerations := [3]int64{}
				expectedGenerations[windowIndex] = 1
				require.Equal(t, expectedGenerations, generations)
				actual := 0.0
				if terminal == "completed" {
					actual = 3
				}
				cmd := f.command(old, terminal, actual)
				var err error
				if terminal == "completed" {
					_, err = f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
				} else {
					_, err = f.repo.ReleaseBatchImageBalance(context.Background(), &cmd)
				}
				require.NoError(t, err)
				expected := [3]float64{5 + actual, 5 + actual, 5 + actual}
				expected[windowIndex] = 5
				f.quotaUsage(expected[0], expected[1], expected[2])
				newCmd := f.command(newer, "cancelled", 0)
				_, err = f.repo.ReleaseBatchImageBalance(context.Background(), &newCmd)
				require.NoError(t, err)
				for i := range expected {
					expected[i] -= 5
				}
				f.quotaUsage(expected[0], expected[1], expected[2])
				f.money(1000-actual, 0, actual, actual)
			})
		}
	}
}

func TestVideoTaskBillingManualResetGenerationsRollback(t *testing.T) {
	f := newVideoBillingFixture(t)
	f.quota(100)
	sub := videoResetTestSubscription(t, f)
	old := f.reserve(f.task(8, 1, 1))
	_, err := integrationDB.ExecContext(context.Background(), "UPDATE user_platform_quotas SET daily_usage_usd=8 WHERE user_id=$1 AND platform='video'", f.userID)
	require.NoError(t, err)
	tx, err := integrationEntClient.Tx(context.Background())
	require.NoError(t, err)
	txCtx := dbent.NewTxContext(context.Background(), tx)
	require.NoError(t, NewUserSubscriptionRepository(integrationEntClient).ResetUsageWindows(txCtx, sub.ID, true, true, true, time.Now()))
	require.NoError(t, NewUserPlatformQuotaRepository(integrationEntClient).ResetExpiredWindow(txCtx, f.userID, service.PlatformVideo, "daily", time.Now()))
	require.NoError(t, tx.Rollback())
	var daily, weekly, monthly float64
	var generations [3]int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd,daily_reset_generation,weekly_reset_generation,monthly_reset_generation FROM user_subscriptions WHERE id=$1", sub.ID).Scan(&daily, &weekly, &monthly, &generations[0], &generations[1], &generations[2]))
	require.Equal(t, [3]float64{8, 8, 8}, [3]float64{daily, weekly, monthly})
	require.Equal(t, [3]int64{}, generations)
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT daily_usage_usd,daily_reset_generation FROM user_platform_quotas WHERE user_id=$1 AND platform='video'", f.userID).Scan(&daily, &generations[0]))
	require.Equal(t, 8.0, daily)
	require.Zero(t, generations[0])
	cmd := f.command(old, "cancelled", 0)
	_, err = f.repo.ReleaseBatchImageBalance(context.Background(), &cmd)
	require.NoError(t, err)
}
