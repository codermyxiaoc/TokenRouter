package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// CreativeUsageRecoveryRepository 仅查询结算证据和投递状态，不提供扣款入口。
type CreativeUsageRecoveryRepository interface {
	ListCreativeRunsForUsageRecovery(context.Context, int64, time.Time, int) ([]*CreativeRun, error)
	MarkCreativeUsageRecorded(context.Context, string) (bool, error)
}

// ReconcileCreativeUsageOnce 补写近期已扣费终态日志；不调用当前价格、当前套餐或资金操作。
func (s *CreativePublicService) ReconcileCreativeUsageOnce(ctx context.Context) (int, error) {
	if s == nil || s.Repo == nil || s.UsageLogRepo == nil {
		return 0, nil
	}
	repo, ok := s.Repo.(CreativeUsageRecoveryRepository)
	if !ok {
		return 0, nil
	}
	s.usageRecoveryMu.Lock()
	defer s.usageRecoveryMu.Unlock()
	if s.usageRecoverySince.IsZero() {
		// 固定启动时窗口，短暂数据库故障不会让本轮待恢复记录滑出窗口。
		s.usageRecoverySince = time.Now().Add(-24 * time.Hour)
	}
	const limit = 50
	runs, err := repo.ListCreativeRunsForUsageRecovery(ctx, s.usageRecoveryAfter, s.usageRecoverySince, limit)
	if err != nil {
		return 0, err
	}
	recovered := 0
	var lastErr error
	for _, run := range runs {
		if err := ctx.Err(); err != nil {
			return recovered, err
		}
		if run == nil {
			continue
		}
		recorded, err := repo.MarkCreativeUsageRecorded(ctx, run.RunID)
		if err == nil && !recorded {
			var outputs []*CreativeRunOutput
			outputs, err = s.Repo.ListCreativeRunOutputs(ctx, run.RunID)
			if err == nil {
				var receipt *BatchImageBalanceHoldResult
				var count int
				receipt, count, err = creativeRecordedCaptureReceipt(run, outputs)
				if err == nil {
					err = s.recordCreativeUsageLog(ctx, run, *run.ActualCost, count, receipt, *run.CompletedAt)
				}
			}
			if err == nil {
				recorded, err = repo.MarkCreativeUsageRecorded(ctx, run.RunID)
				if err == nil && !recorded {
					err = errors.New("creative recovered usage was not persisted")
				}
			}
		}
		if err != nil {
			lastErr = fmt.Errorf("creative run %s usage requires reconciliation: %w", run.RunID, err)
		} else if recorded {
			recovered++
		}
		// 异常行不能阻塞后续页；下一轮从头扫描时再核对。
		s.usageRecoveryAfter = run.ID
	}
	if len(runs) < limit {
		s.usageRecoveryAfter = 0
	}
	return recovered, lastErr
}

// creativeRecordedCaptureReceipt 只用首次预占快照还原分摊，并与已保存实扣金额严格核对。
func creativeRecordedCaptureReceipt(run *CreativeRun, outputs []*CreativeRunOutput) (*BatchImageBalanceHoldResult, int, error) {
	if run == nil || run.ActualCost == nil || run.CompletedAt == nil || run.ProviderResultRecordedAt == nil ||
		run.AccountID == nil || *run.AccountID <= 0 || len(outputs) != run.RequestedOutputCount || len(outputs) == 0 ||
		math.IsNaN(*run.ActualCost) || math.IsInf(*run.ActualCost, 0) || *run.ActualCost < 0 ||
		math.IsNaN(run.BaseUnitPrice) || math.IsInf(run.BaseUnitPrice, 0) || run.BaseUnitPrice < 0 {
		return nil, 0, errors.New("creative captured amount or result metadata is unavailable")
	}
	count := 0
	for _, output := range outputs {
		if output == nil {
			return nil, 0, errors.New("creative captured output metadata is incomplete")
		}
		switch output.Status {
		case CreativeRunOutputStatusSucceeded, CreativeRunOutputStatusAcked:
			count++
		case CreativeRunOutputStatusFailed:
		default:
			return nil, 0, errors.New("creative captured output status cannot be confirmed")
		}
	}
	if count == 0 {
		return nil, 0, errors.New("creative captured successful output is unavailable")
	}
	command, err := buildCreativeHoldCommand(run, CreativeCaptureRequestID(run.RunID), run.BaseUnitPrice*float64(count))
	if err != nil {
		return nil, 0, err
	}
	plan, err := PlanBatchImageBillingCapture(command)
	if err != nil {
		return nil, 0, err
	}
	if math.IsNaN(plan.ActualAmountUSD) || math.IsInf(plan.ActualAmountUSD, 0) || math.Abs(plan.ActualAmountUSD-*run.ActualCost) > 1e-8 {
		return nil, 0, errors.New("creative billing snapshot does not match captured amount")
	}
	return &BatchImageBalanceHoldResult{ActualAmountUSD: plan.ActualAmountUSD, SubscriptionAmountUSD: plan.SubscriptionAmountUSD,
		BalanceAmountUSD: plan.BalanceAmountUSD, BillingAllocations: plan.BillingAllocations}, count, nil
}
