//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 所有账务样本仅在隔离数据库创建，不访问上游或线上余额。
func mediaImageReviewFixture(t *testing.T, subscription float64, failedAt time.Time) (*mediaTaskRepository, *usageBillingRepository, *service.ImageBillingReservation, *service.MediaTask) {
	t.Helper()
	ctx := context.Background()
	billing, cmd, _ := newImageBillingIntegrationFixture(t, 1, subscription)
	hold, err := billing.ReserveImageBilling(ctx, cmd)
	require.NoError(t, err)
	repo := &mediaTaskRepository{db: integrationDB}
	o := service.MediaTaskObservation{Source: "async_image", TaskID: "imgtask_" + cmd.Hold.RequestID, MediaType: "image", Platform: "openai", Model: "image-budget", Status: "failed", UserID: cmd.Hold.ActorUserID, APIKeyID: cmd.Hold.APIKeyID, GroupID: cmd.Hold.GroupID, RequestID: cmd.Hold.RequestID, CreatedAt: failedAt.Add(-time.Minute), CompletedAt: &failedAt}
	require.NoError(t, repo.Observe(ctx, o))
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT id FROM media_tasks WHERE task_id=$1 AND api_key_id=$2`, o.TaskID, o.APIKeyID).Scan(&id))
	task, err := repo.Get(ctx, service.MediaTaskActor{UserID: o.UserID}, id)
	require.NoError(t, err)
	return repo, billing, hold, task
}

func TestMediaTaskImageReconciliationMixedFundsAndLateCapture(t *testing.T) {
	for _, action := range []string{"charge", "release", "expire"} {
		t.Run(action, func(t *testing.T) {
			repo, billing, hold, task := mediaImageReviewFixture(t, .50831488, time.Now().Add(-2*time.Hour))
			ctx := context.Background()
			before, err := repo.GetMediaTaskBillingReview(ctx, task)
			require.NoError(t, err)
			require.True(t, before.CanCharge)
			require.True(t, before.CanRelease)
			cutoff := time.Now().Add(-time.Hour)
			command := service.MediaTaskSettlementCommand{TaskID: task.ID, ActorID: task.UserID, Action: action, Reason: "管理员核对测试"}
			if action == "expire" {
				command.ActorID = 0
				command.FailedBefore = &cutoff
			}
			first, err := repo.ResolveMediaTaskBilling(ctx, command)
			require.NoError(t, err)
			require.Equal(t, hold.Hold.UserID, first.UserID)
			_, err = repo.ResolveMediaTaskBilling(ctx, command)
			require.NoError(t, err)
			view, err := repo.GetMediaTaskBillingReview(ctx, task)
			require.NoError(t, err)
			require.False(t, view.CanRelease)
			require.False(t, view.CanCharge)
			require.Zero(t, view.HeldBalanceAmount)
			require.Zero(t, view.HeldSubscriptionAmount)
			var balance, frozen, subUsed float64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance,frozen_balance FROM users WHERE id=$1`, hold.Hold.UserID).Scan(&balance, &frozen))
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_subscriptions WHERE user_id=$1`, hold.Hold.UserID).Scan(&subUsed))
			require.Zero(t, frozen)
			if action == "charge" {
				require.InDelta(t, 1-before.HeldBalanceAmount, balance, 1e-8)
				require.InDelta(t, before.HeldSubscriptionAmount, subUsed, 1e-8)
				require.InDelta(t, before.OriginalReservedAmount, *view.ActualAmount, 1e-8)
			} else {
				require.Equal(t, 1.0, balance)
				require.Zero(t, subUsed)
				require.Zero(t, *view.ActualAmount)
			}
			// 原生成或补账即便晚到，也不能再次消费已由管理员收敛的原预留。
			_, err = billing.CaptureImageBilling(ctx, hold.ID, imageBillingCaptureCommand(hold), .12)
			require.Error(t, err)
			var count int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_task_billing_resolutions WHERE media_task_id=$1`, task.ID).Scan(&count))
			require.Equal(t, 1, count)
			other := command
			other.Action = "charge"
			other.ActorID = task.UserID
			other.FailedBefore = nil
			if action == "charge" {
				other.Action = "release"
			}
			_, err = repo.ResolveMediaTaskBilling(ctx, other)
			require.ErrorIs(t, err, service.ErrMediaTaskSettlementConflict)
		})
	}
}

func TestMediaTaskImageReconciliationConcurrentDecisions(t *testing.T) {
	repo, _, hold, task := mediaImageReviewFixture(t, 0, time.Now().Add(-2*time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errors := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			action := "charge"
			if n%2 == 0 {
				action = "release"
			}
			_, err := repo.ResolveMediaTaskBilling(ctx, service.MediaTaskSettlementCommand{TaskID: task.ID, ActorID: task.UserID, Action: action, Reason: "并发核对"})
			errors <- err
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			require.ErrorIs(t, err, service.ErrMediaTaskSettlementConflict)
		}
	}
	view, err := repo.GetMediaTaskBillingReview(ctx, task)
	require.NoError(t, err)
	var balance, frozen float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance,frozen_balance FROM users WHERE id=$1`, hold.Hold.UserID).Scan(&balance, &frozen))
	require.Zero(t, frozen)
	if view.Action == "charge" {
		require.InDelta(t, .88, balance, 1e-8)
	} else {
		require.Equal(t, 1.0, balance)
	}
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_task_billing_resolutions WHERE media_task_id=$1`, task.ID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestMediaTaskReconciliationDeadlineRollbackAndDeferral(t *testing.T) {
	repo, _, hold, task := mediaImageReviewFixture(t, 0, time.Now().Add(-30*time.Minute))
	ctx := context.Background()
	cutoff := time.Now().Add(-time.Hour)
	ids, err := repo.ListExpiredMediaTaskBilling(ctx, cutoff, 100)
	require.NoError(t, err)
	require.NotContains(t, ids, task.ID)
	_, err = repo.ResolveMediaTaskBilling(ctx, service.MediaTaskSettlementCommand{TaskID: task.ID, Action: "expire", Reason: "超时", FailedBefore: &cutoff})
	require.ErrorIs(t, err, service.ErrMediaTaskSettlementConflict)
	// 审计写入失败必须回滚之前已做的资金修改。
	_, err = repo.ResolveMediaTaskBilling(ctx, service.MediaTaskSettlementCommand{TaskID: task.ID, ActorID: 9223372036854775807, Action: "release", Reason: "模拟审计外键失败"})
	require.Error(t, err)
	var frozen float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT frozen_balance FROM users WHERE id=$1`, hold.Hold.UserID).Scan(&frozen))
	require.InDelta(t, .12, frozen, 1e-8)
	_, err = integrationDB.ExecContext(ctx, `UPDATE media_tasks SET completed_at=NOW()-INTERVAL '2 hours' WHERE id=$1`, task.ID)
	require.NoError(t, err)
	ids, err = repo.ListExpiredMediaTaskBilling(ctx, cutoff, 100)
	require.NoError(t, err)
	require.Contains(t, ids, task.ID)
	require.NoError(t, repo.DeferMediaTaskBillingReview(ctx, task.ID))
	ids, err = repo.ListExpiredMediaTaskBilling(ctx, cutoff, 100)
	require.NoError(t, err)
	require.NotContains(t, ids, task.ID)
	_, err = repo.ResolveMediaTaskBilling(ctx, service.MediaTaskSettlementCommand{TaskID: task.ID, Action: "expire", Reason: "超时", FailedBefore: &cutoff})
	require.NoError(t, err)
}

func TestMediaTaskExpiredImageProjectionCannotRevive(t *testing.T) {
	repo, _, _, task := mediaImageReviewFixture(t, 0, time.Now().Add(-2*time.Hour))
	ctx := context.Background()
	cutoff := time.Now().Add(-time.Hour)
	_, err := integrationDB.ExecContext(ctx, `UPDATE media_tasks SET error_message=$2 WHERE id=$1`, task.ID, imageTaskInterruptedSummary)
	require.NoError(t, err)
	_, err = repo.ResolveMediaTaskBilling(ctx, service.MediaTaskSettlementCommand{TaskID: task.ID, Action: "expire", Reason: "超时", FailedBefore: &cutoff})
	require.NoError(t, err)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	now := time.Now()
	require.NoError(t, projectDurableImageObservation(ctx, tx, service.MediaTaskObservation{Source: "async_image", TaskID: task.TaskID, MediaType: "image", Platform: "openai", Status: "completed", UserID: task.UserID, APIKeyID: task.APIKeyID, RequestID: task.RequestID, CreatedAt: task.CreatedAt, CompletedAt: &now}))
	require.NoError(t, tx.Commit())
	view, err := repo.Get(ctx, service.MediaTaskActor{UserID: task.UserID}, task.ID)
	require.NoError(t, err)
	require.Equal(t, "expired", view.Status)
	list, err := repo.List(ctx, service.MediaTaskActor{UserID: task.UserID}, service.MediaTaskFilter{Status: "expired", Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.EqualValues(t, 1, list.Total)
}

func TestMediaTaskReconciliationMissingLedgerAndAlreadyCharged(t *testing.T) {
	repo, billing, hold, task := mediaImageReviewFixture(t, 0, time.Now().Add(-2*time.Hour))
	ctx := context.Background()
	_, err := billing.CaptureImageBilling(ctx, hold.ID, imageBillingCaptureCommand(hold), .12)
	require.NoError(t, err)
	view, err := repo.GetMediaTaskBillingReview(ctx, task)
	require.NoError(t, err)
	require.Equal(t, "charged", view.Status)
	require.False(t, view.CanRelease)
	_, err = repo.ResolveMediaTaskBilling(ctx, service.MediaTaskSettlementCommand{TaskID: task.ID, ActorID: task.UserID, Action: "release", Reason: "不能退款已捕获"})
	require.ErrorIs(t, err, service.ErrMediaTaskSettlementConflict)
	missing := *task
	missing.TaskID = fmt.Sprintf("missing-%d", task.ID)
	missing.RequestID = "missing-request"
	missing.ID = 0
	view, err = repo.GetMediaTaskBillingReview(ctx, &missing)
	require.NoError(t, err)
	require.Equal(t, "unreserved", view.Status)
	require.False(t, view.CanCharge)
	require.Zero(t, view.HeldBalanceAmount)
}
