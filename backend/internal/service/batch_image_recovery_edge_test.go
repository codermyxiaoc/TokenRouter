//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 从持久已受理任务补队列后，完整 worker 链路只能查询原任务，不能再次调用 Submit。
func TestBatchImageRecoveredWorkerNeverResubmitsAndRetriesUsageDelivery(t *testing.T) {
	ctx := context.Background()
	repo := newFakeBatchImageRepository()
	job := testSettlingBatchImageJob("imgbatch_recovery_pipeline")
	job.ID, job.Status, job.Provider = 1, BatchImageJobStatusSubmitted, "fake"
	job.ItemCount, job.SuccessCount, job.FailCount = 1, 0, 0
	providerID := "provider/original-accepted"
	job.ProviderJobName = &providerID
	repo.jobs[job.BatchID] = job
	queueRestore := &batchRecoverableQueue{}
	recovery := &BatchImageQueueRecoveryService{Repo: &batchQueueRecoveryRepo{jobs: []*BatchImageJob{job}}, Queue: queueRestore}
	_, err := recovery.RecoverOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{job.BatchID}, queueRestore.calls)
	provider := &fakeProcessorProvider{getErr: errors.New("upstream unavailable")}
	billing := &fakeBatchImageBillingRepo{}
	logs := &creativeUsageLogRepo{err: errors.New("usage write unavailable")}
	processor := newTestBatchImageProcessor(repo, provider)
	processor.BillingRepo = billing
	pipeline := &BatchImagePipelineProcessor{ProviderProcessor: processor, SettlementService: &BatchImageSettlementService{
		Repo: repo, BillingRepo: billing, UsageLogRepo: logs, Pricing: &fakeBatchImagePricingResolver{unitPrice: 0.25},
	}}
	queue := newFakeBatchImageQueue(queueRestore.calls[0])
	worker := NewBatchImageWorker(queue, pipeline, BatchImageWorkerOptions{ReserveBlockTimeout: time.Millisecond})
	require.NoError(t, worker.RunOnce(ctx))
	require.Empty(t, billing.captures)
	require.Empty(t, billing.releases)
	require.Empty(t, queue.acked)
	provider.getErr = nil
	provider.status = &BatchProviderStatus{InternalState: BatchProviderStateSucceeded, ProviderOutputRef: "files/output"}
	provider.result = `{"key":"ok","response":{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + batchImageTestData + `"}}]}}]}}` + "\n"
	require.NoError(t, worker.RunOnce(ctx))
	require.Equal(t, BatchImageJobStatusSettling, job.Status)
	require.NoError(t, worker.RunOnce(ctx))
	require.Equal(t, BatchImageJobStatusSettling, job.Status)
	require.Empty(t, queue.acked)
	require.Zero(t, job.RetryCount)
	logs.err = nil
	require.NoError(t, worker.RunOnce(ctx))
	require.Equal(t, BatchImageJobStatusCompleted, job.Status)
	require.Equal(t, []string{job.BatchID}, queue.acked)
	require.Len(t, billing.captures, 2)
	require.Len(t, billing.seen, 1)
	require.Empty(t, billing.reserves)
	require.Empty(t, billing.releases)
	require.Equal(t, providerID, *job.ProviderJobName)
	require.Len(t, logs.logs, 1)
	require.InDelta(t, 0.25, logs.logs[BatchImageCaptureRequestID(job.BatchID)].ActualCost, 1e-10)
}

type batchImageMarkerFailureRepo struct {
	*fakeBatchImageRepository
	err error
}

func (r *batchImageMarkerFailureRepo) ListBatchImageJobsForUsageRecovery(context.Context, int64, time.Time, int) ([]*BatchImageJob, error) {
	return nil, nil
}
func (r *batchImageMarkerFailureRepo) MarkBatchImageUsageRecorded(context.Context, string) (bool, error) {
	return r.err == nil, r.err
}

// 已写日志但完成标记失败，不能走达到重试上限后的退款分支。
func TestBatchImageUsageMarkerFailureNeverRefundsCapturedCharge(t *testing.T) {
	repo := &batchImageMarkerFailureRepo{fakeBatchImageRepository: newFakeBatchImageRepository(), err: errors.New("marker unavailable")}
	job := testSettlingBatchImageJob("imgbatch_marker_retry")
	job.RetryCount = batchImageSettlementMaxRetries - 1
	code := "SETTLEMENT_BILLING_FAILED"
	job.LastErrorCode = &code
	repo.jobs[job.BatchID] = job
	billing, logs := &fakeBatchImageBillingRepo{}, &creativeUsageLogRepo{}
	svc := &BatchImageSettlementService{Repo: repo, BillingRepo: billing, UsageLogRepo: logs, Pricing: &fakeBatchImagePricingResolver{unitPrice: 0.25}}
	_, err := svc.Settle(context.Background(), job.BatchID)
	require.Error(t, err)
	require.Equal(t, BatchImageJobStatusSettling, job.Status)
	require.Equal(t, batchImageSettlementMaxRetries-1, job.RetryCount)
	require.Len(t, logs.logs, 1)
	require.Empty(t, billing.releases)
	repo.err = nil
	_, err = svc.Settle(context.Background(), job.BatchID)
	require.NoError(t, err)
	require.Equal(t, BatchImageJobStatusCompleted, job.Status)
	require.Len(t, billing.seen, 1)
	require.Len(t, logs.logs, 1)
	require.Empty(t, billing.releases)
}
