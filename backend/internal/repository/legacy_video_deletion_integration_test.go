//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 删除与在途 worker 共存：旧快照不能抹掉删除事实，但真实完成证据仍可继续结算。
func TestLegacyVideoRepositoryDeletionPreservesEvidenceAndLeases(t *testing.T) {
	f := newLegacyVideoFixture(t)
	ctx := context.Background()
	task := f.task()
	task.Source = "seedance_video"
	task.RequestID = service.StableGrokVideoBillingRequestID(service.SeedanceTaskKey(task.TaskID))
	require.NoError(t, f.repo.Save(ctx, task))
	claimed, err := f.repo.ClaimDue(ctx, time.Now(), 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	worker := claimed[0]
	deleteRepo := f.repo.(service.LegacyVideoTaskDeletionRepository)
	wrong := *task
	wrong.AccountID++
	require.ErrorIs(t, deleteRepo.MarkUpstreamDeleted(ctx, &wrong), service.ErrLegacyVideoTaskConflict)
	read, err := f.repo.Get(ctx, task.Source, task.TaskID, f.user, f.key)
	require.NoError(t, err)
	require.Nil(t, read.UpstreamDeletedAt)
	require.NoError(t, deleteRepo.MarkUpstreamDeleted(ctx, task))
	deletedAt := task.UpstreamDeletedAt
	require.NotNil(t, deletedAt)
	require.Equal(t, "reconciliation", task.BillingStatus)
	require.NoError(t, deleteRepo.MarkUpstreamDeleted(ctx, task))
	require.Equal(t, deletedAt, task.UpstreamDeletedAt, "重复保存不应改变首次删除时间")
	// 删除不撤销租约，已确认的真实完成和报价不能被丢弃。
	worker.Status, worker.BillingStatus = "completed", "pending"
	worker.SettlementResult = json.RawMessage(`{"Usage":{"OutputTokens":1000},"Observation":{"Status":"completed"},"Quote":{"test":true}}`)
	require.NoError(t, f.repo.Checkpoint(ctx, worker))
	require.NoError(t, f.repo.Finish(ctx, worker))
	read, err = f.repo.Get(ctx, task.Source, task.TaskID, f.user, f.key)
	require.NoError(t, err)
	require.NotNil(t, read.UpstreamDeletedAt)
	require.JSONEq(t, string(worker.SettlementResult), string(read.SettlementResult))
	claimed, err = f.repo.ClaimDue(ctx, time.Now(), 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1, "删除不能阻止已有证据的结算")
	claimed[0].BillingStatus = "settled"
	require.NoError(t, f.repo.Finish(ctx, claimed[0]))
	require.NoError(t, deleteRepo.MarkUpstreamDeleted(ctx, task))
	require.Equal(t, "settled", task.BillingStatus)
}

// 旧缓存任务的首次持久记录可以与删除事实同事务写入；无完成证据时停止领取上游查询。
func TestLegacyVideoRepositoryDeletedUnknownStopsClaims(t *testing.T) {
	f := newLegacyVideoFixture(t)
	ctx := context.Background()
	task := f.task()
	task.Source = "seedance_video"
	deleteRepo := f.repo.(service.LegacyVideoTaskDeletionRepository)
	require.NoError(t, deleteRepo.MarkUpstreamDeleted(ctx, task))
	require.NotNil(t, task.UpstreamDeletedAt)
	require.Equal(t, "reconciliation", task.BillingStatus)
	claimed, err := f.repo.ClaimDue(ctx, time.Now().Add(time.Hour), 10, time.Minute)
	require.NoError(t, err)
	require.Empty(t, claimed)
	// 删除发生前已领取的旧 worker 可以安全结束，但不得改回 pending 或丢掉删除时间。
	lease := "before-delete"
	_, err = integrationDB.ExecContext(ctx, `UPDATE legacy_video_tasks SET lease_token=$2,lease_until=NOW()+INTERVAL '1 minute' WHERE id=$1`, task.ID, lease)
	require.NoError(t, err)
	task.LeaseToken, task.BillingStatus = lease, "pending"
	task.UpstreamDeletedAt = nil
	require.NoError(t, f.repo.Finish(ctx, task))
	read, err := f.repo.Get(ctx, task.Source, task.TaskID, f.user, f.key)
	require.NoError(t, err)
	require.NotNil(t, read.UpstreamDeletedAt)
	require.Equal(t, "reconciliation", read.BillingStatus)
}
