package service

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"go.uber.org/zap"
)

// 创作台计费请求 ID 前缀：全部经由 usage_billing_dedup 幂等表去重，
// 同一 runID 的同一操作（hold/capture/release）重试不会产生重复资金动作。
const (
	creativeHoldRequestPrefix       = "creative_hold:"
	creativeCaptureRequestPrefix    = "creative_capture:"
	creativeReleaseRequestPrefix    = "creative_release:"
	creativeSettlementRequestPrefix = "creative_settle:"
)

// creativePricingSnapshotVersion 采用与批量图片第二版一致的按基础金额分配语义。
// 创作台没有批量折扣与账号倍率：scale 固定为 1，hold 与结算同价。
const creativePricingSnapshotVersion = 2

func CreativeHoldRequestID(runID string) string {
	return creativeHoldRequestPrefix + strings.TrimSpace(runID)
}

func CreativeCaptureRequestID(runID string) string {
	return creativeCaptureRequestPrefix + strings.TrimSpace(runID)
}

func CreativeReleaseRequestID(runID string) string {
	return creativeReleaseRequestPrefix + strings.TrimSpace(runID)
}

func CreativeSettlementRequestID(runID string) string {
	return creativeSettlementRequestPrefix + strings.TrimSpace(runID)
}

// buildCreativeHoldCommand 把任务元数据转换为计费预占命令。
// 直接复用 BatchImageBalanceHoldCommand（BatchID 填 runID），不复制计费 SQL。
func buildCreativeHoldCommand(run *CreativeRun, requestID string, actualBaseAmount float64) (*BatchImageBalanceHoldCommand, error) {
	if run == nil {
		return nil, ErrCreativeBillingHoldFailed
	}
	if run.APIKeyID <= 0 {
		return nil, ErrCreativeBillingHoldFailed
	}
	holdAmount := run.EstimatedCost
	if run.HoldAmount != nil {
		holdAmount = *run.HoldAmount
	}
	if holdAmount < 0 {
		holdAmount = 0
	}
	if actualBaseAmount < 0 {
		actualBaseAmount = 0
	}
	groupID := run.GroupID
	cmd := &BatchImageBalanceHoldCommand{
		RequestID:                       requestID,
		APIKeyID:                        run.APIKeyID,
		UserID:                          run.UserID,
		ActorUserID:                     run.UserID,
		GroupID:                         &groupID,
		QuotaPlatform:                   run.QuotaPlatform,
		BatchID:                         run.RunID,
		APIKeyBillingMode:               APIKeyBillingModeAuto,
		HoldAmount:                      holdAmount,
		ActualAmount:                    0,
		PricingSnapshotVersion:          creativePricingSnapshotVersion,
		BaseAmountUSD:                   math.Max(run.BaseUnitPrice, 0) * float64(max(run.RequestedOutputCount, 0)),
		ActualBaseAmountUSD:             actualBaseAmount,
		SubscriptionRateMultiplier:      math.Max(run.SubscriptionRateMultiplier, 0),
		SubscriptionRateMultiplierScale: 1,
		BalanceRateMultiplier:           math.Max(run.BalanceRateMultiplier, 0),
		SettlementRateScale:             1,
		DisablePlanGroupRateMultiplier:  !run.PlanGroupRateEnabled,
		BalanceHoldAmount:               run.BalanceHoldAmount,
		SubscriptionHoldAllocations:     cloneBillingAllocations(run.SubscriptionHoldAllocations),
		AllowanceReserved:               run.AllowanceReserved,
		CreativeEntity:                  true,
		ReservedAt:                      run.CreatedAt,
		RequestPayloadHash:              strings.TrimSpace(run.RequestFingerprint),
	}
	return cmd, nil
}

// reserveCreativeBalanceHold 冻结任务预计费用；返回后 run 上回填混合预占快照。
func reserveCreativeBalanceHold(ctx context.Context, repo UsageBillingRepository, run *CreativeRun) error {
	if repo == nil {
		return ErrCreativeBillingHoldFailed.WithCause(errors.New("creative billing repository is not configured"))
	}
	if run.EstimatedCost <= 0 && (!run.PlanGroupRateEnabled || run.BaseUnitPrice <= 0 || run.RequestedOutputCount <= 0) {
		// 确定免费的合同无需冻结；允许套餐覆盖且基础金额为正时，零预估不能替代事务内资金分配。
		run.BalanceHoldAmount = 0
		run.SubscriptionHoldAllocations = nil
		return nil
	}
	cmd, err := buildCreativeHoldCommand(run, CreativeHoldRequestID(run.RunID), 0)
	if err != nil {
		return err
	}
	// 预占阶段按新任务预记语义统计 API Key/成员额度，并在任务行上落预记标记；
	// 捕获/释放阶段则使用任务行上持久化的 run.AllowanceReserved。
	cmd.AllowanceReserved = true
	result, err := repo.ReserveBatchImageBalance(ctx, cmd)
	if err != nil {
		if errors.Is(err, ErrBatchImageInsufficientBalance) {
			return ErrCreativeInsufficientBalance
		}
		if errors.Is(err, ErrAPIKeyQuotaExhausted) || errors.Is(err, ErrAPIKeyRateLimit5hExceeded) || errors.Is(err, ErrAPIKeyRateLimit1dExceeded) ||
			errors.Is(err, ErrAPIKeyRateLimit7dExceeded) || errors.Is(err, ErrTeamMemberDailyExceeded) || errors.Is(err, ErrTeamMemberWeeklyExceeded) ||
			errors.Is(err, ErrTeamMemberMonthlyExceeded) || errors.Is(err, ErrUserPlatformDailyQuotaExhausted) ||
			errors.Is(err, ErrUserPlatformWeeklyQuotaExhausted) || errors.Is(err, ErrUserPlatformMonthlyQuotaExhausted) {
			return err
		}
		return ErrCreativeBillingHoldFailed.WithCause(err)
	}
	if result != nil {
		run.BalanceHoldAmount = result.BalanceAmountUSD
		run.SubscriptionHoldAllocations = batchImageSubscriptionAllocations(result.BillingAllocations)
		holdAmount := result.HoldAmountUSD
		run.HoldAmount = &holdAmount
		run.EstimatedCost = result.EstimatedAmountUSD
	}
	// 预占成功后同步更新内存快照，后续创建失败回滚必须携带真实 allowance 状态。
	run.AllowanceReserved = true
	return nil
}

// captureCreativeBalanceHold 按成功输出数捕获实际费用（幂等，request_id 去重）。
func captureCreativeBalanceHold(ctx context.Context, repo UsageBillingRepository, run *CreativeRun, successCount int) (*BatchImageBalanceHoldResult, error) {
	if repo == nil {
		return nil, ErrCreativeSettlementBillingFail.WithCause(errors.New("creative billing repository is not configured"))
	}
	// 捕获只能覆盖原任务已预占的输出数量，不能因异常产物额外透支用户资金。
	if run == nil || successCount < 0 || successCount > run.RequestedOutputCount {
		return nil, ErrCreativeSettlementBillingFail.WithCause(errors.New("creative output count exceeds its reservation"))
	}
	// token 模式按执行时算定的 token 费用扣（不超过预扣上限），按张模式按单价 × 成功张数。
	actualBase := creativeCaptureBase(run, successCount)
	cmd, err := buildCreativeHoldCommand(run, CreativeCaptureRequestID(run.RunID), actualBase)
	if err != nil {
		return nil, err
	}
	result, err := repo.CaptureBatchImageBalance(ctx, cmd)
	if err != nil {
		return nil, ErrCreativeSettlementBillingFail.WithCause(err)
	}
	if result != nil {
		run.ActualCost = &result.ActualAmountUSD
	}
	return result, nil
}

// UsageBillingRequestRecorder 只读确认某个资金动作是否已经提交（含归档），不提供扣款或释放入口。
type UsageBillingRequestRecorder interface {
	UsageBillingRequestRecorded(ctx context.Context, requestID string, apiKeyID int64) (bool, error)
}

// creativeCaptureRecorded 判断该任务是否已捕获资金；仓储不支持查询时按未捕获处理，由仓储互斥守卫兜底。
func creativeCaptureRecorded(ctx context.Context, repo UsageBillingRepository, run *CreativeRun) (bool, error) {
	recorder, ok := repo.(UsageBillingRequestRecorder)
	if !ok || run == nil {
		return false, nil
	}
	return recorder.UsageBillingRequestRecorded(ctx, CreativeCaptureRequestID(run.RunID), run.APIKeyID)
}

// releaseCreativeBalanceHold 释放未消耗的冻结（失败/取消/结果丢失路径，幂等）。
// 与批量图片一致：同 request id 的指纹冲突视为已释放，避免毒消息循环。
func releaseCreativeBalanceHold(ctx context.Context, repo UsageBillingRepository, run *CreativeRun) error {
	if repo == nil || run == nil {
		return nil
	}
	holdAmount := run.EstimatedCost
	if run.HoldAmount != nil {
		holdAmount = *run.HoldAmount
	}
	if holdAmount <= 0 {
		return nil
	}
	cmd, err := buildCreativeHoldCommand(run, CreativeReleaseRequestID(run.RunID), 0)
	if err != nil {
		return err
	}
	if _, err := repo.ReleaseBatchImageBalance(ctx, cmd); err != nil {
		if errors.Is(err, ErrUsageBillingRequestConflict) {
			logger.L().Warn("creative.release_fingerprint_conflict_treated_as_released",
				zap.String("run_id", run.RunID),
			)
			return nil
		}
		if errors.Is(err, ErrBatchImageHoldAlreadyCaptured) {
			// 资金已捕获，原预占已经不在冻结池中；再释放会误减其它任务的冻结。原样上抛由调用方按已扣款收尾。
			return err
		}
		return ErrCreativeBillingHoldFailed.WithCause(err)
	}
	return nil
}
