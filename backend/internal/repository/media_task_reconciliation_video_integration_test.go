//go:build integration

package repository

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 使用真实媒体投影进入聚合仓储，验证决策凭证、原视频账本与余额一起提交。
func videoMediaAggregateReviewFixture(t *testing.T, failedAt time.Time) (*mediaTaskRepository, *videoBillingFixture, *service.VideoTaskRecord, *service.MediaTask) {
	t.Helper()
	f := newVideoBillingFixture(t)
	f.quota(100)
	task := f.reserve(tokenPrepayTestTask(t, f, 9, .3))
	videoMediaReviewTask(t, f, task, "failed")
	repo := &mediaTaskRepository{db: integrationDB}
	observation := service.MediaTaskObservation{Source: "video", TaskID: task.ID, MediaType: "video", Platform: service.PlatformVideo,
		Model: "video-token-prepay", Status: "failed", UserID: task.UserID, APIKeyID: task.APIKeyID, GroupID: &task.GroupID,
		AccountID: &task.AccountID, RequestID: "video_capture:" + task.ID, CreatedAt: failedAt.Add(-time.Minute), CompletedAt: &failedAt}
	require.NoError(t, repo.Observe(context.Background(), observation))
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT id FROM media_tasks WHERE source='video' AND task_id=$1 AND api_key_id=$2`, task.ID, task.APIKeyID).Scan(&id))
	media, err := repo.Get(context.Background(), service.MediaTaskActor{UserID: task.UserID}, id)
	require.NoError(t, err)
	return repo, f, task, media
}

func TestVideoMediaAggregateReconciliationDecisionAndReplay(t *testing.T) {
	for _, action := range []string{"charge", "release", "expire"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			repo, f, task, media := videoMediaAggregateReviewFixture(t, time.Now().Add(-2*time.Hour))
			command := service.MediaTaskSettlementCommand{TaskID: media.ID, ActorID: f.userID, Action: action, Reason: "集成测试核对原预留"}
			if action == "expire" {
				cutoff := time.Now().Add(-time.Hour)
				command.ActorID, command.FailedBefore = 0, &cutoff
			}
			for range 2 {
				result, err := repo.ResolveMediaTaskBilling(ctx, command)
				require.NoError(t, err)
				require.Equal(t, task.Hold.UserID, result.UserID)
				require.Equal(t, task.UserID, result.ActorUserID)
				require.Equal(t, task.APIKeyID, result.APIKeyID)
			}
			var count int
			var recordedAction string
			var actor sql.NullInt64
			var held, charged, balance, subscription float64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM media_task_billing_resolutions WHERE media_task_id=$1`, media.ID).Scan(&count))
			require.Equal(t, 1, count)
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT action,actor_id,reserved_amount,amount,balance_amount,subscription_amount FROM media_task_billing_resolutions WHERE media_task_id=$1`, media.ID).Scan(&recordedAction, &actor, &held, &charged, &balance, &subscription))
			require.Equal(t, action, recordedAction)
			require.InDelta(t, 2.7, held, 1e-10)
			require.Equal(t, action != "expire", actor.Valid)
			if actor.Valid {
				require.Equal(t, f.userID, actor.Int64)
			}
			wantStatus, wantBilling, wantReview := "failed", "released", "released"
			if action == "charge" {
				wantBilling, wantReview = "settled", "charged"
				require.InDelta(t, 2.7, charged, 1e-10)
				require.InDelta(t, 2.7, balance, 1e-10)
				f.money(997.3, 0, 2.7, 0)
				f.quotaUsage(2.7, 2.7, 2.7)
			} else {
				require.Zero(t, charged)
				require.Zero(t, balance)
				f.money(1000, 0, 0, 0)
				f.quotaUsage(0, 0, 0)
				if action == "expire" {
					wantStatus, wantReview = "expired", "expired"
				}
			}
			require.Zero(t, subscription)
			fresh, err := f.tasks.Get(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, wantStatus, fresh.Status)
			require.Equal(t, wantBilling, fresh.BillingStatus)
			require.True(t, fresh.EffectsDone)
			require.Empty(t, fresh.LeaseToken)
			// 查询方即便持有处理前的旧快照，也不能在永久决定后重新领取任务并写供应商用量。
			_, err = f.tasks.Claim(ctx, task.ID, time.Minute)
			require.ErrorIs(t, err, service.ErrVideoTaskNotFound)
			projection, err := repo.Get(ctx, service.MediaTaskActor{UserID: f.userID}, media.ID)
			require.NoError(t, err)
			require.Equal(t, wantStatus, projection.Status)
			review, err := repo.GetMediaTaskBillingReview(ctx, projection)
			require.NoError(t, err)
			require.Equal(t, wantReview, review.Status)
			require.False(t, review.CanCharge)
			require.False(t, review.CanRelease)
			require.NotNil(t, review.ActualAmount)
			require.InDelta(t, charged, *review.ActualAmount, 1e-10)
			// 不同动作不能改写第一次的永久决定，也不能对已核定金额二次扣退。
			command.ActorID, command.Action, command.FailedBefore = f.userID, "charge", nil
			if action == "charge" {
				command.Action = "release"
			}
			_, err = repo.ResolveMediaTaskBilling(ctx, command)
			require.ErrorIs(t, err, service.ErrMediaTaskSettlementConflict)
		})
	}
}

func TestVideoMediaAggregateConcurrentClaimCannotRestartFinalDecision(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	repo, f, task, media := videoMediaAggregateReviewFixture(t, time.Now().Add(-2*time.Hour))
	// 模拟自动查询已经读到尚未完成的任务，然后与管理员最终决定争用同一任务行。
	_, err := integrationDB.ExecContext(ctx, `UPDATE video_tasks SET lease_token='',lease_until=NULL WHERE id=$1`, task.ID)
	require.NoError(t, err)
	blocker, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer blocker.Rollback()
	var blockerPID int
	require.NoError(t, blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID))
	var sourceID string
	require.NoError(t, blocker.QueryRowContext(ctx, `SELECT id FROM video_tasks WHERE id=$1 FOR UPDATE`, task.ID).Scan(&sourceID))
	settled := make(chan error, 1)
	go func() {
		_, err := repo.ResolveMediaTaskBilling(ctx, service.MediaTaskSettlementCommand{TaskID: media.ID, ActorID: f.userID, Action: "charge", Reason: "竞争领取时核定"})
		settled <- err
	}()
	settlementPID := quotaBoundaryWaiter(t, ctx, blockerPID, "FROM video_tasks")
	claimed := make(chan error, 1)
	go func() {
		_, err := f.tasks.Claim(ctx, task.ID, time.Minute)
		claimed <- err
	}()
	// PostgreSQL 的等待队列可能直接依赖前一个等待者，因此同时检查阻塞者与核定事务。
	require.Eventually(t, func() bool {
		var waiting bool
		return integrationDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE wait_event_type='Lock' AND strpos(query,'UPDATE video_tasks SET lease_token')>0
 AND ($1::int=ANY(pg_blocking_pids(pid)) OR $2::int=ANY(pg_blocking_pids(pid))))`, blockerPID, settlementPID).Scan(&waiting) == nil && waiting
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, blocker.Commit())
	require.NoError(t, <-settled)
	require.ErrorIs(t, <-claimed, service.ErrVideoTaskNotFound)
	f.money(997.3, 0, 2.7, 0)
	fresh, err := f.tasks.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "settled", fresh.BillingStatus)
	require.True(t, fresh.EffectsDone)
	require.Empty(t, fresh.LeaseToken)
}

func TestVideoMediaAggregateReconciliationDecisionFailureRollsBackFunds(t *testing.T) {
	ctx := context.Background()
	repo, f, task, media := videoMediaAggregateReviewFixture(t, time.Now().Add(-2*time.Hour))
	// 不存在的管理员触发凭证外键失败，发生在原视频账本和余额更新之后。
	_, err := repo.ResolveMediaTaskBilling(ctx, service.MediaTaskSettlementCommand{TaskID: media.ID, ActorID: 9223372036854775807, Action: "charge", Reason: "必须全事务回滚"})
	require.Error(t, err)
	f.money(997.3, 2.7, 2.7, 0)
	f.quotaUsage(2.7, 2.7, 2.7)
	fresh, err := f.tasks.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", fresh.Status)
	require.Equal(t, "reconciliation", fresh.BillingStatus)
	require.False(t, fresh.EffectsDone)
	require.Equal(t, task.LeaseToken, fresh.LeaseToken)
	require.Nil(t, fresh.BillingResult)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM media_task_billing_resolutions WHERE media_task_id=$1`, media.ID).Scan(&count))
	require.Zero(t, count)
	projection, err := repo.Get(ctx, service.MediaTaskActor{UserID: f.userID}, media.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", projection.Status)
	// 失败没有留下半条决定，随后合法释放仍能处理同一份冻结。
	_, err = repo.ResolveMediaTaskBilling(ctx, service.MediaTaskSettlementCommand{TaskID: media.ID, ActorID: f.userID, Action: "release", Reason: "回滚后正常释放"})
	require.NoError(t, err)
	f.money(1000, 0, 0, 0)
}

func TestVideoMediaAggregateExpiryRechecksFailureTimeAfterUserLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	repo, f, task, media := videoMediaAggregateReviewFixture(t, time.Now().Add(-2*time.Hour))
	cutoff := time.Now().Add(-time.Hour)
	ids, err := repo.ListExpiredMediaTaskBilling(ctx, cutoff, 100)
	require.NoError(t, err)
	require.Contains(t, ids, media.ID)
	blocker, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer blocker.Rollback()
	var blockerPID int
	require.NoError(t, blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID))
	require.NoError(t, lockUsageBillingUser(ctx, blocker, task.Hold.UserID))
	result := make(chan error, 1)
	go func() {
		_, err := repo.ResolveMediaTaskBilling(ctx, service.MediaTaskSettlementCommand{TaskID: media.ID, Action: "expire", Reason: "超时核对", FailedBefore: &cutoff})
		result <- err
	}()
	quotaBoundaryWaiter(t, ctx, blockerPID, "FROM users")
	// 扫描及事务首次读取后修正失败时间；最终释放必须在拿到资金锁后再次读取。
	_, err = blocker.ExecContext(ctx, `UPDATE media_tasks SET completed_at=NOW() WHERE id=$1`, media.ID)
	require.NoError(t, err)
	require.NoError(t, blocker.Commit())
	require.ErrorIs(t, <-result, service.ErrMediaTaskSettlementConflict)
	f.money(997.3, 2.7, 2.7, 0)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM media_task_billing_resolutions WHERE media_task_id=$1`, media.ID).Scan(&count))
	require.Zero(t, count)
	fresh, err := f.tasks.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "reconciliation", fresh.BillingStatus)
	require.Equal(t, "failed", fresh.Status)
}

// 测试仅使用隔离 PostgreSQL 资金快照，不调用视频供应商。
func videoMediaReviewTask(t *testing.T, f *videoBillingFixture, task *service.VideoTaskRecord, status string) *service.MediaTask {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE video_tasks SET status=$2,billing_status='reconciliation' WHERE id=$1`, task.ID, status)
	require.NoError(t, err)
	return &service.MediaTask{ID: f.keyID, Source: "video", TaskID: task.ID, UserID: task.UserID, APIKeyID: task.APIKeyID,
		GroupID: &task.GroupID, AccountID: &task.AccountID, Status: status}
}

func videoMediaReviewTransaction(ctx context.Context, payer int64, task *service.MediaTask, action string, rollback bool) (*service.BatchImageBalanceHoldResult, error) {
	tx, err := integrationDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := lockUsageBillingUser(ctx, tx, payer); err != nil {
		return nil, err
	}
	result, err := resolveVideoMediaTaskBilling(ctx, tx, task, action)
	if err != nil || rollback {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func TestVideoMediaReconciliationTokenPrepayAndLateWorker(t *testing.T) {
	for _, action := range []string{"charge", "release", "expire"} {
		t.Run(action, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			configureDailyActivitySpendingTest(t, true)
			f.quota(100)
			task := f.reserve(tokenPrepayTestTask(t, f, 9, .3))
			media := videoMediaReviewTask(t, f, task, "failed")
			result, err := videoMediaReviewTransaction(context.Background(), task.Hold.UserID, media, action, false)
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.InDelta(t, 2.7, result.HoldAmountUSD, 1e-10)
			if action == "charge" {
				require.InDelta(t, 2.7, result.ActualAmountUSD, 1e-10)
				f.money(997.3, 0, 2.7, 0)
				f.quotaUsage(2.7, 2.7, 2.7)
				assertDailyActivitySpending(t, f.userID, 1, 2.7)
			} else {
				require.Zero(t, result.ActualAmountUSD)
				f.money(1000, 0, 0, 0)
				f.quotaUsage(0, 0, 0)
				assertDailyActivitySpending(t, f.userID, 0, 0)
			}
			fresh, err := f.tasks.Get(context.Background(), task.ID)
			require.NoError(t, err)
			require.True(t, fresh.EffectsDone)
			require.Empty(t, fresh.LeaseToken)
			require.Nil(t, fresh.Metadata.Tokens)
			if action == "expire" {
				require.Equal(t, "expired", fresh.Status)
			}
			_, err = videoMediaReviewTransaction(context.Background(), task.Hold.UserID, media, action, false)
			require.ErrorIs(t, err, service.ErrVideoTaskConflict)
			// 旧执行者晚到成功既不能覆盖任务，也不能通过普通账务入口再次计费。
			task.Status = "completed"
			require.ErrorIs(t, f.tasks.Save(context.Background(), task, false), service.ErrVideoTaskConflict)
			command := task.Hold
			command.RequestFingerprint, command.RequestID = "", "video_capture:"+task.ID
			command.ActualBaseAmountUSD = 10
			_, err = f.repo.CaptureBatchImageBalance(context.Background(), &command)
			require.ErrorIs(t, err, service.ErrVideoTaskConflict)
		})
	}
}

func TestVideoMediaReconciliationMixedFundsKeepsOriginalAllocation(t *testing.T) {
	for _, action := range []string{"charge", "release"} {
		t.Run(action, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(100)
			sub := fixedVideoSubscription(t, f, 1, 9)
			task := f.reserve(tokenPrepayTestTask(t, f, 3, .3))
			f.money(998.3, 1.7, 2.7, 0)
			fixedVideoAssertSubscription(t, sub.ID, 1)
			// 后续套餐价卡改变，管理员处理仍保留原分配和预留金额。
			_, err := integrationDB.ExecContext(context.Background(), `UPDATE subscription_plan_groups SET rate_multiplier=.01 WHERE plan_id=$1`, sub.PlanID)
			require.NoError(t, err)
			media := videoMediaReviewTask(t, f, task, "cancelled")
			result, err := videoMediaReviewTransaction(context.Background(), task.Hold.UserID, media, action, false)
			require.NoError(t, err)
			if action == "charge" {
				require.InDelta(t, 1, result.SubscriptionAmountUSD, 1e-10)
				require.InDelta(t, 1.7, result.BalanceAmountUSD, 1e-10)
				f.money(998.3, 0, 2.7, 0)
				fixedVideoAssertSubscription(t, sub.ID, 1)
				f.quotaUsage(1.7, 1.7, 1.7)
			} else {
				f.money(1000, 0, 0, 0)
				fixedVideoAssertSubscription(t, sub.ID, 0)
				f.quotaUsage(0, 0, 0)
			}
		})
	}
}

func TestVideoMediaReconciliationAtomicRollbackAndConcurrentDecisions(t *testing.T) {
	f := newVideoBillingFixture(t)
	f.quota(100)
	task := f.reserve(f.task(10, 1, 1))
	media := videoMediaReviewTask(t, f, task, "failed")
	_, err := videoMediaReviewTransaction(context.Background(), task.Hold.UserID, media, "release", true)
	require.NoError(t, err)
	f.money(990, 10, 10, 0)
	f.quotaUsage(10, 10, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var charged, released atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			action := "release"
			if i%2 == 0 {
				action = "charge"
			}
			_, err := videoMediaReviewTransaction(ctx, task.Hold.UserID, media, action, false)
			if err == nil {
				if action == "charge" {
					charged.Add(1)
				} else {
					released.Add(1)
				}
			} else if err != service.ErrVideoTaskConflict {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, charged.Load()+released.Load())
	if charged.Load() == 1 {
		f.money(990, 0, 10, 0)
	} else {
		f.money(1000, 0, 0, 0)
	}
}

func TestVideoMediaReconciliationRejectsSuccessRunningAndWrongIdentity(t *testing.T) {
	for _, status := range []string{"completed", "processing", "queued", "submission_unknown"} {
		t.Run(status, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			task := f.reserve(f.task(10, 1, 1))
			media := videoMediaReviewTask(t, f, task, status)
			if status == "completed" {
				// 已完成视频仅在待对账时开放人工核定；reserved 仍属自动流程（见 CompletedReconciliation 用例）。
				_, err := integrationDB.ExecContext(context.Background(), `UPDATE video_tasks SET billing_status='reserved' WHERE id=$1`, task.ID)
				require.NoError(t, err)
			}
			_, err := videoMediaReviewTransaction(context.Background(), task.Hold.UserID, media, "release", false)
			require.ErrorIs(t, err, service.ErrVideoTaskConflict)
			f.money(990, 10, 10, 0)
		})
	}
	f := newVideoBillingFixture(t)
	task := f.reserve(f.task(10, 1, 1))
	media := videoMediaReviewTask(t, f, task, "failed")
	wrongGroup := f.groupID + 100
	media.GroupID = &wrongGroup
	_, err := videoMediaReviewTransaction(context.Background(), task.Hold.UserID, media, "release", false)
	require.ErrorIs(t, err, service.ErrVideoTaskConflict)
	f.money(990, 10, 10, 0)
}

func TestVideoMediaReconciliationReleaseRespectsResetWindows(t *testing.T) {
	f := newVideoBillingFixture(t)
	f.quota(100)
	sub := fixedVideoSubscription(t, f, 1, 9)
	task := f.reserve(tokenPrepayTestTask(t, f, 3, .3))
	media := videoMediaReviewTask(t, f, task, "expired")
	// 同日人工重置和自然切换过的窗口均属于新用量，旧任务不得回退它们。
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE user_subscriptions SET
 daily_usage_usd=5,daily_reset_generation=daily_reset_generation+1,
 weekly_usage_usd=6,weekly_window_start=weekly_window_start+INTERVAL '1 second' WHERE id=$1`, sub.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(context.Background(), `UPDATE user_platform_quotas SET
 daily_usage_usd=7,daily_reset_generation=daily_reset_generation+1 WHERE user_id=$1 AND platform='video'`, f.userID)
	require.NoError(t, err)
	_, err = videoMediaReviewTransaction(context.Background(), task.Hold.UserID, media, "expire", false)
	require.NoError(t, err)
	f.money(1000, 0, 0, 0)
	f.quotaUsage(7, 0, 0)
	var daily, weekly, monthly float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd
 FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&daily, &weekly, &monthly))
	require.Equal(t, 5.0, daily)
	require.Equal(t, 6.0, weekly)
	require.Zero(t, monthly)
}

func TestVideoMediaReconciliationZeroPrepayNeverCreatesRefundOrCharge(t *testing.T) {
	f := newVideoBillingFixture(t)
	task := f.reserve(newDeferredVideoTestTask(t, f, nil))
	media := videoMediaReviewTask(t, f, task, "failed")
	_, err := videoMediaReviewTransaction(context.Background(), task.Hold.UserID, media, "charge", false)
	require.ErrorIs(t, err, service.ErrVideoTaskConflict)
	result, err := videoMediaReviewTransaction(context.Background(), task.Hold.UserID, media, "expire", false)
	require.NoError(t, err)
	require.Zero(t, result.HoldAmountUSD)
	require.Zero(t, result.ActualAmountUSD)
	f.money(1000, 0, 0, 0)
}

func TestVideoMediaReconciliationDoesNotBlockAutomaticRelease(t *testing.T) {
	f := newVideoBillingFixture(t)
	f.quota(100)
	task := f.reserve(f.task(10, 1, 1))
	media := videoMediaReviewTask(t, f, task, "failed")
	command := task.Hold
	command.RequestID, command.RequestFingerprint = "video_release:"+task.ID, ""
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start := make(chan struct{})
	errs := make(chan error, 2)
	var manualCharge, automaticRelease atomic.Bool
	go func() {
		<-start
		result, err := videoMediaReviewTransaction(ctx, task.Hold.UserID, media, "charge", false)
		if result != nil && result.Applied && err == nil {
			manualCharge.Store(true)
		}
		errs <- err
	}()
	go func() {
		<-start
		result, err := f.repo.ReleaseBatchImageBalance(ctx, &command)
		if result != nil && result.Applied && err == nil {
			automaticRelease.Store(true)
		}
		errs <- err
	}()
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			require.ErrorIs(t, err, service.ErrVideoTaskConflict)
		}
	}
	require.NotEqual(t, manualCharge.Load(), automaticRelease.Load())
	if manualCharge.Load() {
		f.money(990, 0, 10, 0)
	} else {
		f.money(1000, 0, 0, 0)
	}
}

// 已完成视频在自动结算转入待对账后，管理员必须能人工扣款或退款，冻结不能永久滞留；
// 仍在 reserved（等待成品或取回探测）时不开放，自动过期也不能处理已完成视频。
func TestVideoMediaCompletedReconciliationManualSettlement(t *testing.T) {
	for _, action := range []string{"charge", "release"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			completedAt := time.Now().Add(-2 * time.Hour)
			f := newVideoBillingFixture(t)
			f.quota(100)
			task := f.reserve(tokenPrepayTestTask(t, f, 9, .3))
			videoMediaReviewTask(t, f, task, "completed")
			repo := &mediaTaskRepository{db: integrationDB}
			observation := service.MediaTaskObservation{Source: "video", TaskID: task.ID, MediaType: "video", Platform: service.PlatformVideo,
				Model: "video-token-prepay", Status: "completed", UserID: task.UserID, APIKeyID: task.APIKeyID, GroupID: &task.GroupID,
				AccountID: &task.AccountID, RequestID: "video_capture:" + task.ID, CreatedAt: completedAt.Add(-time.Minute), CompletedAt: &completedAt}
			require.NoError(t, repo.Observe(ctx, observation))
			var id int64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT id FROM media_tasks WHERE source='video' AND task_id=$1 AND api_key_id=$2`, task.ID, task.APIKeyID).Scan(&id))
			media, err := repo.Get(ctx, service.MediaTaskActor{UserID: task.UserID}, id)
			require.NoError(t, err)
			require.Equal(t, "completed", media.Status)

			// reserved 表示自动流程仍在等待成品或取回探测，不开放人工入口。
			_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET billing_status='reserved' WHERE id=$1`, task.ID)
			require.NoError(t, err)
			review, err := repo.GetMediaTaskBillingReview(ctx, media)
			require.NoError(t, err)
			require.Equal(t, "pending", review.Status)
			require.False(t, review.CanRelease)
			require.False(t, review.CanCharge)
			_, err = repo.ResolveMediaTaskBilling(ctx, service.MediaTaskSettlementCommand{TaskID: media.ID, ActorID: f.userID, Action: action, Reason: "等待中"})
			require.ErrorIs(t, err, service.ErrMediaTaskSettlementConflict)

			_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET billing_status='reconciliation' WHERE id=$1`, task.ID)
			require.NoError(t, err)
			review, err = repo.GetMediaTaskBillingReview(ctx, media)
			require.NoError(t, err)
			require.True(t, review.CanRelease)
			require.True(t, review.CanCharge)
			// 自动过期只覆盖失败类任务，不能替已完成视频做决定。
			cutoff := time.Now()
			_, err = repo.ResolveMediaTaskBilling(ctx, service.MediaTaskSettlementCommand{TaskID: media.ID, Action: "expire", FailedBefore: &cutoff})
			require.ErrorIs(t, err, service.ErrMediaTaskSettlementConflict)

			_, err = repo.ResolveMediaTaskBilling(ctx, service.MediaTaskSettlementCommand{TaskID: media.ID, ActorID: f.userID, Action: action, Reason: "已完成视频人工核对"})
			require.NoError(t, err)
			fresh, err := f.tasks.Get(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, "completed", fresh.Status, "人工核定不改写视频生成状态")
			require.True(t, fresh.EffectsDone)
			if action == "charge" {
				require.Equal(t, "settled", fresh.BillingStatus)
				f.money(997.3, 0, 2.7, 0)
			} else {
				require.Equal(t, "released", fresh.BillingStatus)
				f.money(1000, 0, 0, 0)
			}
			_, err = f.tasks.Claim(ctx, task.ID, time.Minute)
			require.ErrorIs(t, err, service.ErrVideoTaskNotFound, "人工决定后自动流程不能再领取")
		})
	}
}
