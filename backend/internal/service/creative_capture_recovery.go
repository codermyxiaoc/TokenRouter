package service

import (
	"context"
	"errors"
	"math"
	"time"

	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
)

// CreativeCaptureReceiptReader 只读返回创作台任务是否已捕获资金及其扣款回执；不提供扣款或释放入口。
type CreativeCaptureReceiptReader interface {
	CreativeCaptureReceipt(ctx context.Context, runID string, apiKeyID int64) (*BatchImageBalanceHoldResult, bool, error)
}

// creativeCaptureReceipt 查询任务的捕获事实；仓储不支持时视为未捕获，由仓储互斥守卫兜底拒绝重复结清。
func (s *CreativePublicService) creativeCaptureReceipt(ctx context.Context, run *CreativeRun) (*BatchImageBalanceHoldResult, bool, error) {
	reader, ok := s.BillingRepo.(CreativeCaptureReceiptReader)
	if !ok || run == nil {
		return nil, false, nil
	}
	return reader.CreativeCaptureReceipt(ctx, run.RunID, run.APIKeyID)
}

// finishCapturedRun 收尾“资金已捕获、但日志或终态未写完”的任务：不重新判断交付、不释放原预占，
// 只按已扣金额补写使用日志并进入已结算终态。捕获时一张都未交付（素材后来过期）的任务进入 result_lost
// 并保留实扣金额，是否退款需按独立的退款流程核定，不能复用预占释放。
// @project-doc docs/domains/creative_studio.md#creative_delivery_billing
func (s *CreativePublicService) finishCapturedRun(ctx context.Context, run *CreativeRun, receipt *BatchImageBalanceHoldResult) error {
	outputs, err := s.Repo.ListCreativeRunOutputs(ctx, run.RunID)
	if err != nil {
		return err
	}
	count := 0
	for _, output := range outputs {
		if output != nil && (output.Status == CreativeRunOutputStatusSucceeded || output.Status == CreativeRunOutputStatusAcked) {
			count++
		}
	}
	if receipt == nil {
		// 历史任务没有回执：只能按预占快照还原。扣款当时究竟按几张成功计费无从得知，
		// 只有全部输出仍是成功时该重建才是确定的；成功张数少于请求数说明中间可能已有输出被改判，
		// 按剩余张数重建会把实扣金额记小，必须保留人工核对而不是猜测。
		if count == 0 || count != run.RequestedOutputCount {
			return errors.New("creative run was captured without a receipt and its charged amount cannot be confirmed; settlement requires reconciliation")
		}
		command, err := buildCreativeHoldCommand(run, CreativeCaptureRequestID(run.RunID), math.Max(run.BaseUnitPrice, 0)*float64(count))
		if err != nil {
			return err
		}
		plan, err := PlanBatchImageBillingCapture(command)
		if err != nil {
			return err
		}
		receipt = &BatchImageBalanceHoldResult{ActualAmountUSD: plan.ActualAmountUSD, SubscriptionAmountUSD: plan.SubscriptionAmountUSD,
			BalanceAmountUSD: plan.BalanceAmountUSD, BillingAllocations: plan.BillingAllocations}
	}
	actualCost := receipt.ActualAmountUSD
	run.ActualCost = &actualCost
	if marker, ok := s.Repo.(CreativeRunAllowanceMarker); ok && run.AllowanceReserved {
		if err := marker.SetCreativeRunAllowanceReserved(ctx, run.RunID, false); err != nil {
			return err
		}
		run.AllowanceReserved = false
	}
	s.invalidateCreativeAuthCache(ctx, run.UserID)
	now := time.Now()
	if count > 0 {
		if err := s.recordCreativeUsageLog(ctx, run, actualCost, count, receipt, now); err != nil {
			return err
		}
		if marker, ok := s.Repo.(CreativeUsageRecoveryRepository); ok {
			recorded, err := marker.MarkCreativeUsageRecorded(ctx, run.RunID)
			if err != nil {
				return err
			}
			if !recorded {
				return errors.New("creative usage was not persisted")
			}
		}
	}
	logger.L().Warn("creative.captured_run_finished_without_release",
		zap.String("run_id", run.RunID),
		zap.Int("delivered_count", count),
		zap.Float64("actual_cost", actualCost),
	)
	if count == 0 {
		err = s.Repo.MarkCreativeRunResultLostAfterSettlement(ctx, run.RunID, actualCost, now)
	} else {
		err = s.Repo.MarkCreativeRunSucceeded(ctx, run.RunID, actualCost, now)
	}
	if err != nil && !errors.Is(err, ErrCreativeInvalidTransition) {
		return err
	}
	return nil
}
