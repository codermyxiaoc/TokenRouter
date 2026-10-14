//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestBatchImageSettlementUsageFailureRetriesWithoutCompletingOrRecharging(t *testing.T) {
	repo := newFakeBatchImageRepository()
	job := testSettlingBatchImageJob("imgbatch_usage_retry")
	subscriptionID := int64(77)
	job.SubscriptionHoldAllocations = []domain.BillingAllocation{{Type: domain.BillingAllocationTypeSubscription, SubscriptionID: &subscriptionID, AmountUSD: 0.3}}
	job.BalanceHoldAmount = 0.95
	repo.jobs[job.BatchID] = job
	billing := &fakeBatchImageBillingRepo{}
	logs := &openAIRecordUsageLogRepoStub{err: errors.New("usage database unavailable")}
	svc := &BatchImageSettlementService{Repo: repo, BillingRepo: billing, UsageLogRepo: logs, Pricing: &fakeBatchImagePricingResolver{unitPrice: 0.25}}
	_, err := svc.Settle(context.Background(), job.BatchID)
	require.Error(t, err)
	require.Equal(t, BatchImageJobStatusSettling, job.Status)
	require.Zero(t, job.RetryCount)
	require.Empty(t, billing.releases)
	logs.err = nil
	_, err = svc.Settle(context.Background(), job.BatchID)
	require.NoError(t, err)
	require.Equal(t, BatchImageJobStatusCompleted, job.Status)
	require.Len(t, billing.seen, 1)
	require.Len(t, billing.captures, 2)
	require.InDelta(t, 0.3, logs.lastLog.SubscriptionAmountUSD, 1e-10)
	require.InDelta(t, 0.2, logs.lastLog.BalanceAmountUSD, 1e-10)
}

// 财务来源来自旧合同，恢复不读取管理员后来修改的套餐和价卡。
func TestBatchImageRecordedCaptureReceiptUsesFrozenFundingRates(t *testing.T) {
	job := testSettlingBatchImageJob("imgbatch_frozen_receipt")
	now := time.Now()
	actual, hold := 0.2, 0.25
	job.ActualCost, job.SettledAt, job.HoldAmount = &actual, &now, &hold
	job.PricingSnapshotVersion = 3
	job.BaseUnitPrice, job.BalanceRateMultiplier = 0.25, 0.2
	job.AccountRateMultiplier, job.HoldMultiplier, job.BatchDiscountMultiplier = 1, 1, 1
	job.BalanceHoldAmount = 0.1
	subscriptionID := int64(77)
	job.SubscriptionHoldAllocations = []domain.BillingAllocation{{Type: domain.BillingAllocationTypeSubscription,
		SubscriptionID: &subscriptionID, AmountUSD: 0.15, BaseAmountUSD: 0.25, RateMultiplier: 0.6}}
	receipt, err := batchImageRecordedCaptureReceipt(job)
	require.NoError(t, err)
	require.InDelta(t, 0.15, receipt.SubscriptionAmountUSD, 1e-10)
	require.InDelta(t, 0.05, receipt.BalanceAmountUSD, 1e-10)
	require.Len(t, receipt.BillingAllocations, 2)
	actual = 0.21
	_, err = batchImageRecordedCaptureReceipt(job)
	require.ErrorContains(t, err, "does not match")
}

type batchUsageRecoveryRepo struct {
	jobs  []*BatchImageJob
	logs  *openAIRecordUsageLogRepoStub
	after []int64
}

func (r *batchUsageRecoveryRepo) ListBatchImageJobsForUsageRecovery(_ context.Context, after int64, _ time.Time, limit int) ([]*BatchImageJob, error) {
	r.after = append(r.after, after)
	var result []*BatchImageJob
	for _, job := range r.jobs {
		if job.ID > after {
			result = append(result, job)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func (r *batchUsageRecoveryRepo) MarkBatchImageUsageRecorded(_ context.Context, id string) (bool, error) {
	return r.logs.lastLog != nil && r.logs.err == nil && r.logs.lastLog.RequestID == BatchImageCaptureRequestID(id), nil
}

func TestBatchImageUsageRecoverySkipsInvalidSnapshotAndDoesNotCallBilling(t *testing.T) {
	invalid, valid := testSettlingBatchImageJob("imgbatch_bad_history"), testSettlingBatchImageJob("imgbatch_good_history")
	now := time.Now()
	actual := 0.5
	invalid.ID, valid.ID = 1, 2
	valid.ActualCost, valid.SettledAt = &actual, &now
	logs := &openAIRecordUsageLogRepoStub{}
	repo := &batchUsageRecoveryRepo{jobs: []*BatchImageJob{invalid, valid}, logs: logs}
	// 无 BillingRepo/Pricing，任何补扣或重新定价都会让测试失败。
	svc := &BatchImageUsageRecoveryService{Repo: repo, Settlement: &BatchImageSettlementService{UsageLogRepo: logs}, Limit: 1}
	_, err := svc.RecoverOnce(context.Background())
	require.Error(t, err)
	require.Zero(t, logs.calls)
	n, err := svc.RecoverOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, actual, logs.lastLog.ActualCost)
	require.Equal(t, now, logs.lastLog.CreatedAt)
	require.Equal(t, []int64{0, 1}, repo.after)
}
