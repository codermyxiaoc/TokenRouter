//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/stretchr/testify/require"
)

// 使用同一冻结合同模拟数据库捕获去重，不读取当前套餐倍率。
type creativeUsageBillingRepo struct {
	UsageBillingRepository
	calls int
	seen  map[string]bool
}

func (r *creativeUsageBillingRepo) CaptureBatchImageBalance(_ context.Context, cmd *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error) {
	r.calls++
	if r.seen == nil {
		r.seen = make(map[string]bool)
	}
	applied := !r.seen[cmd.RequestID]
	r.seen[cmd.RequestID] = true
	plan, err := PlanBatchImageBillingCapture(cmd)
	if err != nil {
		return nil, err
	}
	return &BatchImageBalanceHoldResult{Applied: applied, ActualAmountUSD: plan.ActualAmountUSD,
		SubscriptionAmountUSD: plan.SubscriptionAmountUSD, BalanceAmountUSD: plan.BalanceAmountUSD,
		BillingAllocations: plan.BillingAllocations}, nil
}

type creativeUsageLogRepo struct {
	UsageLogRepository
	err  error
	logs map[string]*UsageLog
}

func (r *creativeUsageLogRepo) Create(_ context.Context, log *UsageLog) (bool, error) {
	if r.err != nil {
		return false, r.err
	}
	if r.logs == nil {
		r.logs = make(map[string]*UsageLog)
	}
	if _, exists := r.logs[log.RequestID]; exists {
		return false, nil
	}
	r.logs[log.RequestID] = log
	return true, nil
}

func creativeUsageTestRun(id string) *CreativeRun {
	now := time.Now()
	account, subscription := int64(55), int64(77)
	actual := 0.4
	return &CreativeRun{RunID: id, UserID: 7, GroupID: 12, APIKeyID: 900, AccountID: &account,
		Model: "gpt-image-1", RequestedModel: "gpt-image-1", Operation: CreativeOperationGenerate,
		Status: CreativeRunStatusSettlementPending, RequestedOutputCount: 1, ImageSize: "1K",
		EstimatedCost: actual, HoldAmount: &actual, ActualCost: &actual, BaseUnitPrice: 1,
		SubscriptionRateMultiplier: 0.6, BalanceRateMultiplier: 0.2, BalanceHoldAmount: 0.1,
		SubscriptionHoldAllocations: []domain.BillingAllocation{{Type: domain.BillingAllocationTypeSubscription,
			SubscriptionID: &subscription, AmountUSD: 0.3, BaseAmountUSD: 0.5, RateMultiplier: 0.6}},
		CreatedAt: now.Add(-time.Minute), ProviderResultRecordedAt: &now, CompletedAt: &now}
}

func TestCreativeSettlementUsageFailureRetriesBeforePublishingTerminal(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprintf("result_lost_%t", lost), func(t *testing.T) {
			svc := newCreativeTestService()
			repo := svc.Repo.(*creativeFakeRunRepo)
			run := creativeUsageTestRun("crun_usage_retry")
			run.ActualCost, run.CompletedAt = nil, nil
			repo.runs[run.RunID] = run
			repo.outputs[run.RunID] = []*CreativeRunOutput{{RunID: run.RunID, Status: CreativeRunOutputStatusSucceeded}}
			if !lost {
				svc.TransientStore.(*creativeFakeTransient).outputs[fmtInputKey(run.RunID, 0)] = []byte("image")
			}
			billing := &creativeUsageBillingRepo{}
			logs := &creativeUsageLogRepo{err: errors.New("usage database unavailable")}
			svc.BillingRepo, svc.UsageLogRepo = billing, logs
			event := CreativeRunOutbox{RunID: run.RunID, Operation: CreativeRunOutboxSettle}
			require.Error(t, svc.reconcileCreativeOutboxEvent(context.Background(), event))
			require.Equal(t, CreativeRunStatusSettlementPending, run.Status)
			require.Nil(t, run.CompletedAt)
			logs.err = nil
			require.NoError(t, svc.reconcileCreativeOutboxEvent(context.Background(), event))
			want := CreativeRunStatusSucceeded
			if lost {
				want = CreativeRunStatusResultLost
			}
			require.Equal(t, want, run.Status)
			require.Equal(t, 2, billing.calls)
			require.Len(t, billing.seen, 1)
			require.Len(t, logs.logs, 1)
			log := logs.logs[CreativeSettlementRequestID(run.RunID)]
			require.InDelta(t, 0.3, log.SubscriptionAmountUSD, 1e-10)
			require.InDelta(t, 0.1, log.BalanceAmountUSD, 1e-10)
			require.InDelta(t, 0.4, log.ActualCost, 1e-10)
			require.Equal(t, int64(77), *log.SubscriptionID)
			// 持久 outbox 直接完成结算，不需要重复入队或调用上游。
			require.Empty(t, svc.Queue.(*creativeFakeQueue).enqueued)
			require.True(t, svc.Queue.(*creativeFakeQueue).lastLock.released)
		})
	}
}

type creativeUsageRecoveryRepo struct {
	*creativeFakeRunRepo
	ordered  []*CreativeRun
	logs     *creativeUsageLogRepo
	marked   map[string]bool
	afterIDs []int64
}

func (r *creativeUsageRecoveryRepo) ListCreativeRunsForUsageRecovery(_ context.Context, after int64, _ time.Time, limit int) ([]*CreativeRun, error) {
	r.afterIDs = append(r.afterIDs, after)
	var runs []*CreativeRun
	for _, run := range r.ordered {
		if run.ID > after && !r.marked[run.RunID] {
			runs = append(runs, run)
			if len(runs) == limit {
				break
			}
		}
	}
	return runs, nil
}

func (r *creativeUsageRecoveryRepo) MarkCreativeUsageRecorded(_ context.Context, id string) (bool, error) {
	if r.logs.logs[CreativeSettlementRequestID(id)] != nil {
		r.marked[id] = true
	}
	return r.marked[id], nil
}

func TestCreativeUsageRecoveryPagesPastBadEvidenceWithoutCharging(t *testing.T) {
	logs := &creativeUsageLogRepo{}
	repo := &creativeUsageRecoveryRepo{creativeFakeRunRepo: newCreativeFakeRunRepo(), logs: logs, marked: map[string]bool{}}
	for i := 1; i <= 51; i++ {
		run := creativeUsageTestRun(fmt.Sprintf("crun_history_%d", i))
		run.ID, run.Status = int64(i), CreativeRunStatusResultLost
		if i <= 50 {
			run.ActualCost = nil
		}
		repo.ordered = append(repo.ordered, run)
		repo.outputs[run.RunID] = []*CreativeRunOutput{{RunID: run.RunID, Status: CreativeRunOutputStatusAcked}}
	}
	// 不配置资金或模型执行器；补写只能使用持久账务事实。
	svc := &CreativePublicService{Repo: repo, UsageLogRepo: logs}
	n, err := svc.ReconcileCreativeUsageOnce(context.Background())
	require.Error(t, err)
	require.Zero(t, n)
	require.Empty(t, logs.logs)
	n, err = svc.ReconcileCreativeUsageOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, []int64{0, 50}, repo.afterIDs)
	run := repo.ordered[50]
	log := logs.logs[CreativeSettlementRequestID(run.RunID)]
	require.Equal(t, *run.CompletedAt, log.CreatedAt)
	require.InDelta(t, 0.4, log.ActualCost, 1e-10)
	require.InDelta(t, 0.3, log.SubscriptionAmountUSD, 1e-10)
	require.InDelta(t, 0.1, log.BalanceAmountUSD, 1e-10)
}

func TestCreativeRecordedCaptureReceiptRejectsAmountDriftAndUnknownResults(t *testing.T) {
	run := creativeUsageTestRun("crun_receipt")
	outputs := []*CreativeRunOutput{{Status: CreativeRunOutputStatusAcked}}
	_, count, err := creativeRecordedCaptureReceipt(run, outputs)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	*run.ActualCost = 0.41
	_, _, err = creativeRecordedCaptureReceipt(run, outputs)
	require.ErrorContains(t, err, "does not match")
	*run.ActualCost = 0.4
	outputs[0].Status = CreativeRunOutputStatusPending
	_, _, err = creativeRecordedCaptureReceipt(run, outputs)
	require.ErrorContains(t, err, "cannot be confirmed")
}
