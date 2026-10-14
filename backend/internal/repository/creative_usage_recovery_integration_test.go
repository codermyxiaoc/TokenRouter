//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 历史补写必须有原始捕获账本；投递标记阻止正常清理后的日志被重复恢复。
func TestCreativeUsageRecoveryRequiresCaptureEvidenceAndPreservesRetention(t *testing.T) {
	f := newVideoBillingFixture(t)
	ctx := context.Background()
	repo := &creativeRunRepository{client: integrationEntClient}
	var afterID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM creative_runs`).Scan(&afterID))
	since := time.Now().Add(-time.Hour)
	runs := map[string]*service.CreativeRun{}
	for _, kind := range []string{"hot", "archive", "unpaid", "old"} {
		runID, err := service.NewCreativeRunID()
		require.NoError(t, err)
		run, err := repo.CreateCreativeRun(ctx, service.CreateCreativeRunParams{
			RunID: runID, UserID: f.userID, GroupID: f.groupID, APIKeyID: f.keyID,
			Model: "test-image", RequestedModel: "test-image", Operation: service.CreativeOperationGenerate,
			RequestedOutputCount: 1, ImageSize: "1K", ResponseMIMEType: "image/png", PromptHash: "test", RequestFingerprint: runID,
		})
		require.NoError(t, err)
		completed := time.Now()
		if kind == "old" {
			completed = since.Add(-time.Hour)
		}
		_, err = integrationDB.ExecContext(ctx, `UPDATE creative_runs SET status='result_lost', account_id=$2,
		    completed_at=$3, actual_cost=0.4, provider_result_recorded_at=$3 WHERE run_id=$1`, run.RunID, f.accountID, completed)
		require.NoError(t, err)
		if kind == "hot" || kind == "old" {
			_, err = integrationDB.ExecContext(ctx, `INSERT INTO usage_billing_dedup(request_id,api_key_id,request_fingerprint) VALUES($1,$2,'test')`, service.CreativeCaptureRequestID(run.RunID), f.keyID)
			require.NoError(t, err)
		}
		if kind == "archive" {
			_, err = integrationDB.ExecContext(ctx, `INSERT INTO usage_billing_dedup_archive(request_id,api_key_id,request_fingerprint,created_at) VALUES($1,$2,'test',NOW())`, service.CreativeCaptureRequestID(run.RunID), f.keyID)
			require.NoError(t, err)
		}
		runs[kind] = run
	}
	found, err := repo.ListCreativeRunsForUsageRecovery(ctx, afterID, since, 1)
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, runs["hot"].RunID, found[0].RunID)
	found, err = repo.ListCreativeRunsForUsageRecovery(ctx, found[0].ID, since, 10)
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, runs["archive"].RunID, found[0].RunID)
	recorded, err := repo.MarkCreativeUsageRecorded(ctx, runs["hot"].RunID)
	require.NoError(t, err)
	require.False(t, recorded)
	logs := NewUsageLogRepository(integrationEntClient, integrationDB, nil)
	_, err = logs.Create(ctx, &service.UsageLog{UserID: f.userID, BillingUserID: f.userID, APIKeyID: f.keyID, AccountID: f.accountID,
		RequestID: service.CreativeSettlementRequestID(runs["hot"].RunID), Model: "test-image", ActualCost: 0.4, TotalCost: 0.4, CreatedAt: time.Now()})
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		recorded, err = repo.MarkCreativeUsageRecorded(ctx, runs["hot"].RunID)
		require.NoError(t, err)
		require.True(t, recorded)
	}
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM usage_logs WHERE api_key_id=$1 AND request_id=$2`, f.keyID, service.CreativeSettlementRequestID(runs["hot"].RunID))
	require.NoError(t, err)
	found, err = repo.ListCreativeRunsForUsageRecovery(ctx, afterID, since, 10)
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, runs["archive"].RunID, found[0].RunID)
}
