//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 恢复必须同时持有完成状态、实扣金额和捕获幂等凭证；标记后清理日志不能再次触发恢复。
func TestBatchImageUsageRecoveryRequiresCaptureEvidenceAndPreservesRetention(t *testing.T) {
	f := newVideoBillingFixture(t)
	ctx := context.Background()
	repo := &batchImageRepository{db: integrationDB, sql: integrationDB}
	var afterID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM batch_image_jobs`).Scan(&afterID))
	since := time.Now().Add(-time.Hour)
	actual := 0.5
	jobs := map[string]*service.BatchImageJob{}
	for _, kind := range []string{"hot", "archive", "unpaid", "old"} {
		job, err := repo.CreateBatchImageJob(ctx, service.CreateBatchImageJobParams{
			BatchID: batchImageTestID(t, kind), UserID: f.userID, BillingUserID: f.userID, APIKeyID: &f.keyID, AccountID: &f.accountID,
			Provider: service.BatchImageProviderGeminiAPI, Model: "test-image", Status: "completed", ItemCount: 1, SuccessCount: 1, ActualCost: &actual,
		})
		require.NoError(t, err)
		settled := time.Now()
		if kind == "old" {
			settled = since.Add(-time.Hour)
		}
		_, err = integrationDB.ExecContext(ctx, `UPDATE batch_image_jobs SET settled_at=$2 WHERE id=$1`, job.ID, settled)
		require.NoError(t, err)
		if kind == "hot" || kind == "old" {
			_, err = integrationDB.ExecContext(ctx, `INSERT INTO usage_billing_dedup(request_id,api_key_id,request_fingerprint) VALUES($1,$2,'test')`, service.BatchImageCaptureRequestID(job.BatchID), f.keyID)
			require.NoError(t, err)
		}
		if kind == "archive" {
			_, err = integrationDB.ExecContext(ctx, `INSERT INTO usage_billing_dedup_archive(request_id,api_key_id,request_fingerprint,created_at) VALUES($1,$2,'test',NOW())`, service.BatchImageCaptureRequestID(job.BatchID), f.keyID)
			require.NoError(t, err)
		}
		jobs[kind] = job
	}
	found, err := repo.ListBatchImageJobsForUsageRecovery(ctx, afterID, since, 10)
	require.NoError(t, err)
	require.Len(t, found, 2)
	require.Equal(t, jobs["hot"].BatchID, found[0].BatchID)
	require.Equal(t, jobs["archive"].BatchID, found[1].BatchID)
	recorded, err := repo.MarkBatchImageUsageRecorded(ctx, jobs["hot"].BatchID)
	require.NoError(t, err)
	require.False(t, recorded)
	logs := NewUsageLogRepository(integrationEntClient, integrationDB, nil)
	_, err = logs.Create(ctx, &service.UsageLog{UserID: f.userID, BillingUserID: f.userID, APIKeyID: f.keyID, AccountID: f.accountID,
		RequestID: service.BatchImageCaptureRequestID(jobs["hot"].BatchID), Model: "test-image", ActualCost: actual, TotalCost: actual, CreatedAt: time.Now()})
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		recorded, err = repo.MarkBatchImageUsageRecorded(ctx, jobs["hot"].BatchID)
		require.NoError(t, err)
		require.True(t, recorded)
	}
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM usage_logs WHERE api_key_id=$1 AND request_id=$2`, f.keyID, service.BatchImageCaptureRequestID(jobs["hot"].BatchID))
	require.NoError(t, err)
	found, err = repo.ListBatchImageJobsForUsageRecovery(ctx, afterID, since, 10)
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, jobs["archive"].BatchID, found[0].BatchID)
}
