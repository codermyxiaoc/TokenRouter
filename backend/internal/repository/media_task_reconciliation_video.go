package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/shopspring/decimal"
)

// resolveVideoMediaTaskBilling 只处理失败视频尚未收敛的原预占，不猜测实际 Token 或上游成本。
// 调用方须先锁付款用户，再锁媒体任务，并在本事务内保存不可覆盖的最终处理决策。
// @project-doc docs/domains/video_tasks.md#video_task_billing
func resolveVideoMediaTaskBilling(ctx context.Context, tx *sql.Tx, media *service.MediaTask, action string) (*service.BatchImageBalanceHoldResult, error) {
	if tx == nil || media == nil || media.ID <= 0 || media.Source != "video" || media.TaskID == "" ||
		media.UserID <= 0 || media.APIKeyID <= 0 || (action != "charge" && action != "release" && action != "expire") {
		return nil, service.ErrMediaTaskInvalid
	}
	task, err := scanVideoTask(tx.QueryRowContext(ctx, "SELECT "+videoTaskColumns+`
 FROM video_tasks WHERE id=$1 AND user_id=$2 AND api_key_id=$3 FOR UPDATE`, media.TaskID, media.UserID, media.APIKeyID))
	if err != nil {
		return nil, err
	}
	// 已完成视频只有在自动结算已转入待对账后才允许人工核定（且不能走自动过期），
	// 避免与仍在等待成品、取回探测的正常流程抢先处理。
	completedPending := task.Status == "completed" && task.BillingStatus == "reconciliation" && action != "expire"
	if !videoMediaTaskBillingIdentityMatches(media, task) ||
		(task.Status != "failed" && task.Status != "cancelled" && task.Status != "expired" && !completedPending) ||
		(task.BillingStatus != "reserved" && task.BillingStatus != "reconciliation") || task.BillingResult != nil {
		return nil, service.ErrVideoTaskConflict
	}
	command := task.Hold
	reservation, err := videoMediaTaskReservedAmounts(&command)
	if err != nil {
		return nil, err
	}
	// 仅读既有去重证据，不认领自动捕获或释放的键，避免与先拿去重键的执行者反序等待。
	held, err := batchImageHoldClaimExists(ctx, tx, "video_hold:"+task.ID, task.APIKeyID)
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, service.ErrVideoTaskConflict
	}
	for _, prefix := range []string{"video_capture:", "video_release:"} {
		done, err := batchImageHoldClaimExists(ctx, tx, prefix+task.ID, task.APIKeyID)
		if err != nil {
			return nil, err
		}
		if done {
			return nil, service.ErrVideoTaskConflict
		}
	}
	operation := batchImageAllowanceRelease
	var result *service.BatchImageBalanceHoldResult
	if action == "charge" {
		if command.VideoDeferredBilling || reservation.ActualAmountUSD <= 0 {
			return nil, service.ErrVideoTaskConflict
		}
		// 全额确认保留原订阅分配；Token 预扣同样只捕获保证金额，不重新分配或追加扣款。
		balanceCommand := command
		balanceCommand.HoldAmount, balanceCommand.ActualAmount = reservation.BalanceAmountUSD, reservation.BalanceAmountUSD
		result, err = captureUsageBillingBatchImageBalance(ctx, tx, &balanceCommand)
		if err == nil {
			reservation.NewBalance, reservation.FrozenBalance = result.NewBalance, result.FrozenBalance
			result = reservation
		}
		command.ActualAmount = command.HoldAmount
		operation = batchImageAllowanceCapture
	} else {
		result, err = releaseUsageBillingBatchImageBilling(ctx, tx, &command)
		if err == nil {
			result.HoldAmountUSD, result.EstimatedAmountUSD = reservation.HoldAmountUSD, reservation.HoldAmountUSD
		}
	}
	if err != nil {
		return nil, err
	}
	if err := applyBatchImageAllowance(ctx, tx, &command, operation); err != nil {
		return nil, err
	}
	// 人工全额捕获仍保留已预记的平台额度，释放沿用原窗口与重置代次回退。
	if err := applyVideoPlatformQuota(ctx, tx, &command, operation, result); err != nil {
		return nil, err
	}
	if action == "charge" {
		if err := recordDailyActivitySpending(ctx, tx, command.UserID, command.APIKeyID,
			"media_task_charge:"+strconv.FormatInt(media.ID, 10), result.SubscriptionAmountUSD, result.BalanceAmountUSD); err != nil {
			return nil, err
		}
	}
	result.Applied = true
	task.BillingStatus = "released"
	if action == "charge" {
		task.BillingStatus = "settled"
	}
	if action == "expire" {
		task.Status = "expired"
	}
	task.Hold.AllowanceReserved = false
	task.BillingResult, task.EffectsDone = result, true
	task.LeaseToken, task.LeaseUntil = "", time.Time{}
	if task.CompletedAt == nil {
		now := time.Now().UTC()
		task.CompletedAt = &now
	}
	raw, err := json.Marshal(task)
	if err != nil {
		return nil, err
	}
	receipt, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	// 原租约在同一资金事务中失效，晚到观测不能改写终态，也不能重新捕获或释放。
	_, err = tx.ExecContext(ctx, `UPDATE video_tasks SET status=$2,billing_status=$3,billing_result=$4::jsonb,
 record=$5::jsonb,effects_done=TRUE,lease_token='',lease_until=NULL,allowance_reserved=FALSE,updated_at=NOW()
 WHERE id=$1`, task.ID, task.Status, task.BillingStatus, string(receipt), string(raw))
	if err != nil {
		return nil, err
	}
	return result, nil
}

// videoMediaTaskBillingIdentityMatches 不用当前密钥所有者替换历史付款人，团队任务保留行为人与付款人。
func videoMediaTaskBillingIdentityMatches(media *service.MediaTask, task *service.VideoTaskRecord) bool {
	if media == nil || task == nil || media.Source != "video" || task.ID != media.TaskID ||
		task.UserID != media.UserID || task.APIKeyID != media.APIKeyID || task.GroupID <= 0 || task.AccountID <= 0 {
		return false
	}
	if (media.GroupID != nil && *media.GroupID != task.GroupID) || (media.AccountID != nil && *media.AccountID != task.AccountID) {
		return false
	}
	hold := task.Hold
	return hold.VideoEntity && !hold.CreativeEntity && hold.UserID > 0 && hold.ActorUserID == task.UserID &&
		hold.BatchID == task.ID && hold.APIKeyID == task.APIKeyID && hold.GroupID != nil && *hold.GroupID == task.GroupID &&
		hold.VideoAccountID == task.AccountID
}

// videoMediaTaskReservedAmounts 只使用已落库资金分配，不能从当前倍率或预估费用反算冻结额。
func videoMediaTaskReservedAmounts(command *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	if command == nil || !validVideoBudgetAmount(command.HoldAmount) || !validVideoBudgetAmount(command.BalanceHoldAmount) {
		return nil, service.ErrVideoTaskConflict
	}
	result := &service.BatchImageBalanceHoldResult{HoldAmountUSD: command.HoldAmount, EstimatedAmountUSD: command.HoldAmount,
		BalanceAmountUSD: command.BalanceHoldAmount}
	subscriptionTotal := decimal.Zero
	for _, allocation := range command.SubscriptionHoldAllocations {
		if allocation.Type != domain.BillingAllocationTypeSubscription || allocation.SubscriptionID == nil || *allocation.SubscriptionID <= 0 ||
			!validVideoBudgetAmount(allocation.AmountUSD) {
			return nil, service.ErrVideoTaskConflict
		}
		subscriptionTotal = subscriptionTotal.Add(decimal.NewFromFloat(allocation.AmountUSD))
		result.BillingAllocations = append(result.BillingAllocations, allocation)
	}
	total := subscriptionTotal.Add(decimal.NewFromFloat(command.BalanceHoldAmount))
	// 任务总预占列为十位精度，钱包为八位；只容纳持久化本身的舍入，不静默补足缺失分配。
	if !total.Round(10).Equal(decimal.NewFromFloat(command.HoldAmount).Round(10)) ||
		(command.VideoDeferredBilling && (!total.IsZero() || command.AllowanceReserved)) {
		return nil, service.ErrVideoTaskConflict
	}
	result.SubscriptionAmountUSD, _ = subscriptionTotal.Float64()
	result.ActualAmountUSD, _ = total.Float64()
	if result.BalanceAmountUSD > 0 {
		result.BillingAllocations = append(result.BillingAllocations, domain.BillingAllocation{
			Type: domain.BillingAllocationTypeBalance, AmountUSD: result.BalanceAmountUSD,
		})
	}
	return result, nil
}
