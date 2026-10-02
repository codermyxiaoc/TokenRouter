package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

// guardVideoBudgetOperation 同一行串行检查捕获与释放，不能仅依赖两个不同的去重键。
// @project-doc docs/domains/video_tasks.md#video_task_billing
func guardVideoBudgetOperation(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand, operation batchImageAllowanceOperation) error {
	var state, status, leaseToken string
	var leaseValid bool
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT billing_status,status,record,lease_token,COALESCE(lease_until>clock_timestamp(),FALSE) FROM video_tasks WHERE id=$1 AND api_key_id=$2 FOR UPDATE`, cmd.BatchID, cmd.APIKeyID).Scan(&state, &status, &raw, &leaseToken, &leaseValid)
	if err != nil {
		return err
	}
	var task service.VideoTaskRecord
	if err = json.Unmarshal(raw, &task); err != nil {
		return err
	}
	if task.Hold.UserID != cmd.UserID || task.AccountID != cmd.VideoAccountID {
		return service.ErrVideoTaskConflict
	}
	if task.Hold.VideoTokenPrepay != cmd.VideoTokenPrepay ||
		(cmd.VideoTokenPrepay && cmd.VideoDeferredBilling) ||
		task.Hold.VideoPrepayDurationSeconds != cmd.VideoPrepayDurationSeconds {
		return service.ErrVideoTaskConflict
	}
	if cmd.VideoTokenPrepay {
		// 预扣只允许来自已持久化的 Token 价卡，基础金额与时长以创建快照为准。
		if task.Quote == nil || task.Quote.Mode != service.BillingModeVideoToken || task.Quote.TokenPrepay == nil ||
			task.Quote.TokenPrepay.PricePerSecond == nil || !validVideoBudgetAmount(*task.Quote.TokenPrepay.PricePerSecond) ||
			cmd.PricingSnapshotVersion < 3 || cmd.SettlementRateScale != 1 ||
			!validVideoBudgetAmount(cmd.BaseAmountUSD) || task.Hold.BaseAmountUSD != cmd.BaseAmountUSD ||
			!validVideoBudgetAmount(cmd.VideoPrepayDurationSeconds) || cmd.VideoPrepayDurationSeconds <= 0 {
			return service.ErrVideoTaskConflict
		}
	} else if cmd.VideoPrepayDurationSeconds != 0 {
		return service.ErrVideoTaskConflict
	}
	if cmd.VideoFixedAmountUSD < 0 || math.IsNaN(cmd.VideoFixedAmountUSD) || math.IsInf(cmd.VideoFixedAmountUSD, 0) ||
		cmd.VideoActualFixedAmountUSD < 0 || math.IsNaN(cmd.VideoActualFixedAmountUSD) || math.IsInf(cmd.VideoActualFixedAmountUSD, 0) ||
		task.Hold.VideoFixedAmountUSD != cmd.VideoFixedAmountUSD ||
		cmd.VideoActualFixedAmountUSD > cmd.VideoFixedAmountUSD {
		return service.ErrVideoTaskConflict
	}
	if cmd.VideoFixedAmountUSD > 0 {
		if task.Quote == nil || cmd.PricingSnapshotVersion < 3 || cmd.SettlementRateScale != 1 {
			return service.ErrVideoTaskConflict
		}
		fixed, err := task.Quote.FixedImageInputCost()
		if err != nil || fixed != cmd.VideoFixedAmountUSD {
			return service.ErrVideoTaskConflict
		}
	}
	// 延后计费只能来自已持久化的 Token 任务，禁止把有预算任务临时改成无上限扣费。
	if task.Hold.VideoDeferredBilling != cmd.VideoDeferredBilling {
		return service.ErrVideoTaskConflict
	}
	if cmd.VideoDeferredBilling && (task.Quote == nil || task.Quote.Mode != service.BillingModeVideoToken ||
		task.Hold.SettlementRateScale != 1 || cmd.SettlementRateScale != 1 ||
		task.Hold.BaseAmountUSD != 0 || cmd.BaseAmountUSD != 0 || cmd.HoldAmount != 0 ||
		cmd.BalanceHoldAmount != 0 || len(cmd.SubscriptionHoldAllocations) != 0 || cmd.AllowanceReserved) {
		return service.ErrVideoTaskConflict
	}
	switch operation {
	case batchImageAllowanceReserve:
		// 过期提交者不能在恢复器接管后重新冻结资金，租约检查必须与预留处于同一事务。
		if state != "pending" || status != "prepared" || !leaseValid || cmd.VideoLeaseToken == "" || cmd.VideoLeaseToken != leaseToken {
			return service.ErrVideoTaskConflict
		}
	case batchImageAllowanceCapture:
		if (state != "reserved" && state != "reconciliation") || status != "completed" {
			return service.ErrVideoTaskConflict
		}
	case batchImageAllowanceRelease:
		if (state != "reserved" && state != "reconciliation") || (status != "failed" && status != "cancelled" && status != "prepared") {
			return service.ErrVideoTaskConflict
		}
	}
	return nil
}

func validVideoBudgetAmount(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// reserveVideoTokenPrepayBudget 仅在预扣阶段强制 1 倍，不改写命令中最终视频结算的倍率快照。
func reserveVideoTokenPrepayBudget(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	prepay := *cmd
	prepay.BaseAmountUSD = service.QuantizeUsageBillingAmount(cmd.BaseAmountUSD)
	prepay.SubscriptionRateMultiplier, prepay.SubscriptionRateMultiplierScale = 1, 1
	prepay.BalanceRateMultiplier, prepay.SettlementRateScale = 1, 1
	prepay.DisablePlanGroupRateMultiplier = true
	result, err := reserveVideoFixedBudget(ctx, tx, &prepay)
	if err != nil {
		return nil, err
	}
	cmd.HoldAmount = result.HoldAmountUSD
	return result, nil
}

// captureVideoTokenPrepayBudget 在同一事务退回原预扣并严格分配真实费用，补扣不足时连退款一起回滚。
// 新模式的预扣只是定额资金保证；最终订阅及其套餐倍率按完成时资格选取，原组/余额/独立倍率保持冻结。
func captureVideoTokenPrepayBudget(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	held, err := batchImageHoldClaimExists(ctx, tx, batchImageHoldClaimRequestID(cmd), cmd.APIKeyID)
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, service.ErrVideoTaskConflict
	}
	if !validVideoBudgetAmount(cmd.ActualBaseAmountUSD) {
		return nil, service.ErrVideoUsageUnavailable
	}
	// 订阅与平台退款沿用原窗口及手动重置代次，不能减去新周期已经发生的用量。
	released, err := releaseUsageBillingBatchImageBilling(ctx, tx, cmd)
	if err != nil {
		return nil, err
	}
	if err := applyBatchImageAllowance(ctx, tx, cmd, batchImageAllowanceRelease); err != nil {
		return nil, err
	}
	if err := applyVideoPlatformQuota(ctx, tx, cmd, batchImageAllowanceRelease, released); err != nil {
		return nil, err
	}
	result, err := captureDeferredVideoBudget(ctx, tx, cmd)
	if err != nil {
		return nil, err
	}
	// 保留原预扣作为历史事实；实际资金来源保存在新的结算分配中。
	result.HoldAmountUSD = service.TotalBatchImageHoldAmount(cmd)
	result.EstimatedAmountUSD = result.HoldAmountUSD
	if result.NewBalance == nil {
		result.NewBalance, result.FrozenBalance = released.NewBalance, released.FrozenBalance
	}
	return result, nil
}

// captureDeferredVideoBudget 在原捕获事务内分配真实费用，任何额度不足都会连同去重键一起回滚。
// 调用方必须没有提交时预占或已在同一事务退回预扣；使用完成时可用订阅及其套餐覆盖倍率。
// 组、余额和独立视频倍率仍用原快照，固定图片费仍按 1 倍。
func captureDeferredVideoBudget(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	if cmd.ActualBaseAmountUSD < 0 || math.IsNaN(cmd.ActualBaseAmountUSD) || math.IsInf(cmd.ActualBaseAmountUSD, 0) {
		return nil, service.ErrVideoUsageUnavailable
	}
	result, err := allocateVideoBudgetComponents(ctx, tx, cmd, cmd.ActualBaseAmountUSD, cmd.VideoActualFixedAmountUSD)
	if err != nil {
		return nil, err
	}
	balanceAmount := result.BalanceAmountUSD
	// 复用严格余额检查；冻结与捕获在同一事务中完成，不向外暴露临时冻结或允许负余额。
	balanceCommand := *cmd
	balanceCommand.HoldAmount, balanceCommand.ActualAmount = balanceAmount, balanceAmount
	if _, err := reserveUsageBillingBatchImageBalance(ctx, tx, &balanceCommand); err != nil {
		return nil, err
	}
	balanceResult, err := captureUsageBillingBatchImageBalance(ctx, tx, &balanceCommand)
	if err != nil {
		return nil, err
	}
	result.NewBalance, result.FrozenBalance = balanceResult.NewBalance, balanceResult.FrozenBalance
	result.ActualAmountUSD = result.SubscriptionAmountUSD + balanceAmount
	// 额度发生在真实扣费时，不能用任务创建时间把长任务计入过期的 Key 窗口。
	amount := service.QuantizeUsageBillingAmount(result.ActualAmountUSD)
	if amount > 0 {
		if err := reserveBatchImageAPIKeyAllowance(ctx, tx, cmd.APIKeyID, amount, time.Now().UTC()); err != nil {
			return nil, err
		}
		if err := reserveBatchImageMemberAllowance(ctx, tx, cmd, amount); err != nil {
			return nil, err
		}
	}
	// 平台额度仅累计实际余额部分；共用严格预留检查后，外层捕获无需再退差额。
	if err := applyVideoPlatformQuota(ctx, tx, cmd, batchImageAllowanceReserve, result); err != nil {
		return nil, err
	}
	return result, nil
}

// allocateVideoBudgetComponent 复用现有订阅资格和窗口扣款，固定图片费强制按 1 倍分配。
func allocateVideoBudgetComponent(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand, base float64, fixed bool) (*service.BatchImageBalanceHoldResult, error) {
	result := &service.BatchImageBalanceHoldResult{}
	if fixed {
		// 固定附加费使用账本八位金额精度，余额和订阅拿到同一个确定金额。
		base = service.QuantizeUsageBillingAmount(base)
	}
	if base <= 0 {
		return result, nil
	}
	allocationCommand := &service.UsageBillingCommand{
		APIKeyBillingMode: cmd.APIKeyBillingMode, PreferredSubscriptionID: cmd.PreferredSubscriptionID,
		UserID: cmd.UserID, GroupID: cmd.GroupID, BaseAmountUSD: base,
		SubscriptionRateMultiplier:      cmd.SubscriptionRateMultiplier,
		SubscriptionRateMultiplierScale: cmd.SubscriptionRateMultiplierScale,
		BalanceRateMultiplier:           cmd.BalanceRateMultiplier, DisablePlanGroupRateMultiplier: cmd.DisablePlanGroupRateMultiplier,
		IncludeAllocationPricing: true, PreserveZeroRateAllocation: true,
	}
	if fixed {
		allocationCommand.SubscriptionRateMultiplier = 1
		allocationCommand.SubscriptionRateMultiplierScale = 1
		allocationCommand.BalanceRateMultiplier = 1
		allocationCommand.DisablePlanGroupRateMultiplier = true
	}
	allocationCommand.Normalize()
	remainingBase, subscriptionAmount, allocations, err := allocateUsageBillingSubscriptions(ctx, tx, allocationCommand)
	if err != nil {
		return nil, err
	}
	if allocationCommand.APIKeyBillingMode == service.APIKeyBillingModeSubscription && remainingBase > 1e-10 {
		return nil, service.ErrPreferredSubscriptionInsufficient
	}
	balanceRate := usageBillingNonNegativeRate(allocationCommand.BalanceRateMultiplier)
	balanceAmount := normalizeBatchImageBalanceAmount(remainingBase * balanceRate)
	result.SubscriptionAmountUSD, result.BalanceAmountUSD = subscriptionAmount, balanceAmount
	result.ActualAmountUSD = subscriptionAmount + balanceAmount
	result.BillingAllocations = allocations
	if balanceAmount > 0 {
		result.BillingAllocations = append(result.BillingAllocations, domain.BillingAllocation{
			Type: domain.BillingAllocationTypeBalance, AmountUSD: balanceAmount, BaseAmountUSD: remainingBase, RateMultiplier: balanceRate,
		})
	}
	if fixed {
		for i := range result.BillingAllocations {
			result.BillingAllocations[i].Component = service.VideoImageInputBillingComponent
		}
	}
	return result, nil
}

// allocateVideoBudgetComponents 在同一付款人事务锁内先分配视频费用，再分配固定费。
func allocateVideoBudgetComponents(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand, base, fixed float64) (*service.BatchImageBalanceHoldResult, error) {
	video, err := allocateVideoBudgetComponent(ctx, tx, cmd, base, false)
	if err != nil {
		return nil, err
	}
	images, err := allocateVideoBudgetComponent(ctx, tx, cmd, fixed, true)
	if err != nil {
		return nil, err
	}
	video.SubscriptionAmountUSD += images.SubscriptionAmountUSD
	video.BalanceAmountUSD = normalizeBatchImageBalanceAmount(video.BalanceAmountUSD + images.BalanceAmountUSD)
	video.ActualAmountUSD = video.SubscriptionAmountUSD + video.BalanceAmountUSD
	video.BillingAllocations = append(video.BillingAllocations, images.BillingAllocations...)
	return video, nil
}

// reserveVideoFixedBudget 将两种计费组件合并冻结和预记，任何来源不足都会整体回滚。
func reserveVideoFixedBudget(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	result, err := allocateVideoBudgetComponents(ctx, tx, cmd, cmd.BaseAmountUSD, cmd.VideoFixedAmountUSD)
	if err != nil {
		return nil, err
	}
	balanceCommand := *cmd
	balanceCommand.HoldAmount = result.BalanceAmountUSD
	balance, err := reserveUsageBillingBatchImageBalance(ctx, tx, &balanceCommand)
	if err != nil {
		return nil, err
	}
	result.NewBalance, result.FrozenBalance = balance.NewBalance, balance.FrozenBalance
	result.HoldAmountUSD = result.ActualAmountUSD
	result.EstimatedAmountUSD = result.ActualAmountUSD
	result.ActualAmountUSD = 0
	allocations := make([]domain.BillingAllocation, 0, len(result.BillingAllocations))
	for _, allocation := range result.BillingAllocations {
		if allocation.Type == domain.BillingAllocationTypeSubscription {
			allocations = append(allocations, allocation)
		}
	}
	cmd.HoldAmount = result.HoldAmountUSD
	if err := persistBatchImageBillingHold(ctx, tx, cmd, result.BalanceAmountUSD, allocations, result.HoldAmountUSD, result.EstimatedAmountUSD); err != nil {
		return nil, err
	}
	return result, nil
}

// readVideoBillingCaptureResult 从原子提交结果重放金额，避免重新推导延后结算、预扣或固定费组件分配。
func readVideoBillingCaptureResult(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	var raw, resultJSON []byte
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT record,billing_status,billing_result FROM video_tasks WHERE id=$1 AND api_key_id=$2`, cmd.BatchID, cmd.APIKeyID).
		Scan(&raw, &state, &resultJSON); err != nil {
		return nil, err
	}
	var task service.VideoTaskRecord
	if err := json.Unmarshal(raw, &task); err != nil {
		return nil, err
	}
	if task.Hold.VideoDeferredBilling != cmd.VideoDeferredBilling || task.Hold.VideoTokenPrepay != cmd.VideoTokenPrepay ||
		task.Hold.VideoPrepayDurationSeconds != cmd.VideoPrepayDurationSeconds || task.Hold.VideoFixedAmountUSD != cmd.VideoFixedAmountUSD ||
		(!task.Hold.VideoDeferredBilling && !task.Hold.VideoTokenPrepay && task.Hold.VideoFixedAmountUSD <= 0) || task.Hold.UserID != cmd.UserID || task.AccountID != cmd.VideoAccountID ||
		task.APIKeyID != cmd.APIKeyID || state != "settled" || len(resultJSON) == 0 {
		return nil, service.ErrVideoTaskConflict
	}
	var result service.BatchImageBalanceHoldResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		return nil, err
	}
	result.Applied = false
	return &result, nil
}

func finishVideoBudgetOperation(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand, operation batchImageAllowanceOperation, result *service.BatchImageBalanceHoldResult) error {
	if operation == batchImageAllowanceReserve {
		if err := saveVideoSubscriptionWindows(ctx, tx, cmd); err != nil {
			return err
		}
	}
	if !cmd.VideoTokenPrepay || operation != batchImageAllowanceCapture {
		// 按秒预扣的捕获已完成原窗口退款及真实费用预记，不能再按旧 hold 退第二次。
		if err := applyVideoPlatformQuota(ctx, tx, cmd, operation, result); err != nil {
			return err
		}
	}
	state := "reserved"
	if operation == batchImageAllowanceCapture {
		state = "settled"
		// 账号成本只累计一次；用户、密钥和团队已经在预留/捕获事务中处理，不能再走普通扣款。
		if cmd.VideoAccountQuotaCost > 0 {
			if _, err := incrementUsageBillingAccountQuota(ctx, tx, cmd.VideoAccountID, cmd.VideoAccountQuotaCost); err != nil {
				return err
			}
		}
	}
	if operation == batchImageAllowanceRelease {
		state = "released"
	}
	var resultJSON any
	if operation != batchImageAllowanceReserve {
		raw, err := json.Marshal(result)
		if err != nil {
			return err
		}
		resultJSON = string(raw)
	}
	_, err := tx.ExecContext(ctx, `UPDATE video_tasks SET billing_status=$2,billing_result=$3::jsonb,updated_at=NOW() WHERE id=$1`, cmd.BatchID, state, resultJSON)
	return err
}
