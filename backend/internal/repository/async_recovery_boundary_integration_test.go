//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 同名捕获凭证和用量日志只能匹配任务原执行 Key，不能借用其他用户的记录。
func TestAsyncUsageRecoveryRejectsOtherUsersBillingEvidence(t *testing.T) {
	owner, other := newVideoBillingFixture(t), newVideoBillingFixture(t)
	ctx := context.Background()
	creativeRepo := &creativeRunRepository{client: integrationEntClient}
	batchRepo := &batchImageRepository{db: integrationDB, sql: integrationDB}
	logs := NewUsageLogRepository(integrationEntClient, integrationDB, nil)
	runID, err := service.NewCreativeRunID()
	require.NoError(t, err)
	run, err := creativeRepo.CreateCreativeRun(ctx, service.CreateCreativeRunParams{
		RunID: runID, UserID: owner.userID, GroupID: owner.groupID, APIKeyID: owner.keyID,
		Model: "test-image", RequestedModel: "test-image", Operation: service.CreativeOperationGenerate,
		RequestedOutputCount: 1, ImageSize: "1K", ResponseMIMEType: "image/png", PromptHash: "test", RequestFingerprint: runID,
	})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE creative_runs SET status='succeeded', actual_cost=0.4,
	    completed_at=NOW(), provider_result_recorded_at=NOW(), account_id=$2 WHERE run_id=$1`, runID, owner.accountID)
	require.NoError(t, err)
	actual := 0.4
	batch, err := batchRepo.CreateBatchImageJob(ctx, service.CreateBatchImageJobParams{
		BatchID: batchImageTestID(t, "other-key-evidence"), UserID: owner.userID, BillingUserID: owner.userID,
		APIKeyID: &owner.keyID, AccountID: &owner.accountID, Provider: service.BatchImageProviderGeminiAPI,
		Model: "test-image", Status: "completed", ItemCount: 1, SuccessCount: 1, ActualCost: &actual,
	})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE batch_image_jobs SET settled_at=NOW() WHERE id=$1`, batch.ID)
	require.NoError(t, err)
	for _, requestID := range []string{service.CreativeCaptureRequestID(runID), service.BatchImageCaptureRequestID(batch.BatchID)} {
		_, err = integrationDB.ExecContext(ctx, `INSERT INTO usage_billing_dedup(request_id,api_key_id,request_fingerprint) VALUES($1,$2,'test')`, requestID, other.keyID)
		require.NoError(t, err)
	}
	for _, requestID := range []string{service.CreativeSettlementRequestID(runID), service.BatchImageCaptureRequestID(batch.BatchID)} {
		_, err = logs.Create(ctx, &service.UsageLog{UserID: other.userID, BillingUserID: other.userID, APIKeyID: other.keyID,
			AccountID: other.accountID, RequestID: requestID, Model: "test-image", ActualCost: actual, TotalCost: actual, CreatedAt: time.Now()})
		require.NoError(t, err)
	}
	since := time.Now().Add(-time.Hour)
	creativeRows, err := creativeRepo.ListCreativeRunsForUsageRecovery(ctx, run.ID-1, since, 10)
	require.NoError(t, err)
	require.Empty(t, creativeRows)
	batchRows, err := batchRepo.ListBatchImageJobsForUsageRecovery(ctx, batch.ID-1, since, 10)
	require.NoError(t, err)
	require.Empty(t, batchRows)
	recorded, err := creativeRepo.MarkCreativeUsageRecorded(ctx, runID)
	require.NoError(t, err)
	require.False(t, recorded)
	recorded, err = batchRepo.MarkBatchImageUsageRecorded(ctx, batch.BatchID)
	require.NoError(t, err)
	require.False(t, recorded)
	// 原 Key 的账本可以恢复资格，但没有原 Key 的日志仍不能投递完成。
	for _, requestID := range []string{service.CreativeCaptureRequestID(runID), service.BatchImageCaptureRequestID(batch.BatchID)} {
		_, err = integrationDB.ExecContext(ctx, `INSERT INTO usage_billing_dedup(request_id,api_key_id,request_fingerprint) VALUES($1,$2,'test')`, requestID, owner.keyID)
		require.NoError(t, err)
	}
	creativeRows, err = creativeRepo.ListCreativeRunsForUsageRecovery(ctx, run.ID-1, since, 10)
	require.NoError(t, err)
	require.Len(t, creativeRows, 1)
	batchRows, err = batchRepo.ListBatchImageJobsForUsageRecovery(ctx, batch.ID-1, since, 10)
	require.NoError(t, err)
	require.Len(t, batchRows, 1)
	recorded, err = creativeRepo.MarkCreativeUsageRecorded(ctx, runID)
	require.NoError(t, err)
	require.False(t, recorded)
	recorded, err = batchRepo.MarkBatchImageUsageRecorded(ctx, batch.BatchID)
	require.NoError(t, err)
	require.False(t, recorded)
}

// 多实例同时恢复过期任务后的容量时，账户限额仍必须只放行一个新生成。
func TestVideoExpiredCapacityStillSerializesConcurrentAdmissions(t *testing.T) {
	f := newVideoBillingFixture(t)
	ctx := context.Background()
	expired := createVideoRepositoryRecord(t, f, f.keyID, f.accountID, service.VideoEndpointSeedance, "expired-capacity")
	expired.Status, expired.BillingStatus = "expired", "reconciliation"
	require.NoError(t, f.tasks.Save(ctx, expired, true))
	const count = 24
	errs := make([]error, count)
	created := make([]bool, count)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			now := time.Now().UTC()
			task := &service.VideoTaskRecord{ID: "vid_" + uuid.NewString(), UserID: f.userID, APIKeyID: f.keyID, GroupID: f.groupID,
				AccountID: f.accountID, PayloadHash: uuid.NewString(), Status: "prepared", BillingStatus: "pending",
				CreatedAt: now, NextPollAt: now, LeaseToken: uuid.NewString(), LeaseUntil: now.Add(time.Minute),
				Target: service.VideoUpstreamTarget{Version: 1, Endpoint: service.VideoEndpointSeedance, AccountID: f.accountID}}
			_, created[i], errs[i] = f.tasks.Create(ctx, task, 1)
		}(i)
	}
	close(start)
	wg.Wait()
	accepted := 0
	for i, err := range errs {
		if created[i] {
			accepted++
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, service.ErrVideoTaskBusy)
		}
	}
	require.Equal(t, 1, accepted)
	stored, err := f.tasks.Get(ctx, expired.ID)
	require.NoError(t, err)
	require.Equal(t, "reconciliation", stored.BillingStatus)
	require.False(t, stored.EffectsDone)
}
