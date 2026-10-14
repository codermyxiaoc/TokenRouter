package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

// BatchImageUsageRecoveryRepository 从已提交的账务凭证恢复日志，不提供扣款入口。
type BatchImageUsageRecoveryRepository interface {
	ListBatchImageJobsForUsageRecovery(context.Context, int64, time.Time, int) ([]*BatchImageJob, error)
	MarkBatchImageUsageRecorded(context.Context, string) (bool, error)
}

type BatchImageUsageRecoveryService struct {
	Repo       BatchImageUsageRecoveryRepository
	Settlement *BatchImageSettlementService
	Limit      int
	mu         sync.Mutex
	after      int64
	since      time.Time
}

// RecoverOnce 只重建已确认扣费的展示事实；不能调用捕获、释放、当前价卡或当前套餐。
func (s *BatchImageUsageRecoveryService) RecoverOnce(ctx context.Context) (int, error) {
	if s == nil || s.Repo == nil || s.Settlement == nil {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.since.IsZero() {
		// 固定首次启动的回溯起点，运行期间数据库故障不会让待补记录滑出窗口。
		s.since = time.Now().Add(-24 * time.Hour)
	}
	limit := s.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	jobs, err := s.Repo.ListBatchImageJobsForUsageRecovery(ctx, s.after, s.since, limit)
	if err != nil {
		return 0, err
	}
	recovered := 0
	var lastErr error
	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return recovered, err
		}
		if job == nil {
			continue
		}
		recorded, err := s.Repo.MarkBatchImageUsageRecorded(ctx, job.BatchID)
		if err == nil && !recorded {
			var receipt *BatchImageBalanceHoldResult
			receipt, err = batchImageRecordedCaptureReceipt(job)
			if err == nil {
				err = s.Settlement.recordUsageLog(ctx, job, *job.ActualCost, BatchImageCaptureRequestID(job.BatchID), *job.SettledAt, receipt)
			}
			if err == nil {
				recorded, err = s.Repo.MarkBatchImageUsageRecorded(ctx, job.BatchID)
				if err == nil && !recorded {
					err = errors.New("batch image recovered usage was not persisted")
				}
			}
		}
		if err != nil {
			lastErr = fmt.Errorf("batch image %s usage recovery requires reconciliation: %w", job.BatchID, err)
		} else if recorded {
			recovered++
		}
		s.after = job.ID
	}
	if len(jobs) < limit {
		s.after = 0
	}
	return recovered, lastErr
}

// batchImageRecordedCaptureReceipt 使用首次预占合同还原捕获分配，并与已保存的真实金额核对。
func batchImageRecordedCaptureReceipt(job *BatchImageJob) (*BatchImageBalanceHoldResult, error) {
	if job == nil || job.ActualCost == nil || job.SettledAt == nil || job.SuccessCount < 0 || job.SuccessCount > job.ItemCount ||
		math.IsNaN(*job.ActualCost) || math.IsInf(*job.ActualCost, 0) || *job.ActualCost < 0 {
		return nil, errors.New("batch image settled amount is unavailable")
	}
	amount := *job.ActualCost
	if job.PricingSnapshotVersion >= 2 {
		amount = job.BaseUnitPrice * float64(job.SuccessCount)
	}
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
		return nil, errors.New("batch image pricing snapshot is invalid")
	}
	command, err := buildBatchImageHoldCommand(job, BatchImageCaptureRequestID(job.BatchID), amount, batchImageDerefString(job.ManifestHash))
	if err != nil {
		return nil, err
	}
	plan, err := PlanBatchImageBillingCapture(command)
	if err != nil {
		return nil, err
	}
	if math.IsNaN(plan.ActualAmountUSD) || math.IsInf(plan.ActualAmountUSD, 0) || math.Abs(plan.ActualAmountUSD-*job.ActualCost) > 1e-8 {
		return nil, errors.New("batch image billing snapshot does not match captured amount")
	}
	return &BatchImageBalanceHoldResult{ActualAmountUSD: plan.ActualAmountUSD, SubscriptionAmountUSD: plan.SubscriptionAmountUSD,
		BalanceAmountUSD: plan.BalanceAmountUSD, BillingAllocations: plan.BillingAllocations}, nil
}
