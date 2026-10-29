package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

type mediaBillingQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type mediaBillingSnapshot struct {
	hold         service.BatchImageBalanceHoldCommand
	state        string
	actual       *float64
	sourceStatus string
}

// 原任务与付款人分别校验，团队成员只能释放该请求已持久化的原付款人资金。
func readMediaBillingSnapshot(ctx context.Context, q mediaBillingQueryer, task *service.MediaTask, lock bool) (*mediaBillingSnapshot, error) {
	s := &mediaBillingSnapshot{}
	switch task.Source {
	case "async_image":
		r, err := readImageBillingReservation(ctx, q, task.RequestID, task.APIKeyID, lock)
		if err != nil {
			return nil, err
		}
		s.hold, s.state = r.Hold, r.State
		var status string
		if err := q.QueryRowContext(ctx, `SELECT status FROM image_tasks WHERE id=$1 AND user_id=$2 AND api_key_id=$3`, task.TaskID, task.UserID, task.APIKeyID).Scan(&status); err == nil {
			s.sourceStatus = status
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if r.Result != nil {
			amount := r.Result.BalanceAmountUSD + r.Result.SubscriptionAmountUSD
			s.actual = &amount
		}
	case "video":
		query := "SELECT " + videoTaskColumns + " FROM video_tasks WHERE id=$1 AND user_id=$2 AND api_key_id=$3"
		if lock {
			query += " FOR UPDATE"
		}
		r, err := scanVideoTask(q.QueryRowContext(ctx, query, task.TaskID, task.UserID, task.APIKeyID))
		if err != nil {
			return nil, err
		}
		if !videoMediaTaskBillingIdentityMatches(task, r) {
			return nil, service.ErrMediaTaskSettlementConflict
		}
		if _, err := videoMediaTaskReservedAmounts(&r.Hold); err != nil {
			return nil, err
		}
		s.hold, s.state = r.Hold, r.BillingStatus
		s.sourceStatus = r.Status
		if r.BillingResult != nil && r.BillingStatus == "settled" {
			amount := r.BillingResult.ActualAmountUSD
			s.actual = &amount
		}
	default:
		return nil, sql.ErrNoRows
	}
	if s.hold.UserID <= 0 || s.hold.APIKeyID != task.APIKeyID || s.hold.ActorUserID != task.UserID ||
		!validVideoBudgetAmount(s.hold.BalanceHoldAmount) {
		return nil, service.ErrMediaTaskSettlementConflict
	}
	for _, allocation := range s.hold.SubscriptionHoldAllocations {
		if allocation.Type != domain.BillingAllocationTypeSubscription || allocation.SubscriptionID == nil || !validVideoBudgetAmount(allocation.AmountUSD) {
			return nil, service.ErrMediaTaskSettlementConflict
		}
	}
	return s, nil
}

func mediaSnapshotAmount(s *mediaBillingSnapshot) (float64, float64) {
	sub := 0.0
	for _, a := range s.hold.SubscriptionHoldAllocations {
		sub += a.AmountUSD
	}
	return s.hold.BalanceHoldAmount, sub
}

func mediaSettlementEligible(status string) bool {
	return status == "failed" || status == "cancelled" || status == "expired"
}

// mediaManualSettlementEligible 允许管理员人工核定两类“已完成但资金仍冻结”的任务：
//   - 异步图片已完成但预占仍待核对的历史任务（缺少结算证据）；
//   - 视频已完成但自动结算进入待对账（用量无法确认、扣费持续失败等），否则冻结可能永不释放。
//
// 自动流程既不能按完成标记猜测扣费，也不能无条件退款，只能由管理员按实际交付情况决定扣费或释放；
// 已完成视频仅在账务已转入 reconciliation 时开放，reserved 仍在等待成品或取回探测，由自动流程按期限处理。
// 自动过期处理仍只覆盖失败类状态。
func mediaManualSettlementEligible(source, status, billingState string) bool {
	if mediaSettlementEligible(status) {
		return true
	}
	switch source {
	case "async_image":
		return status == "completed"
	case "video":
		return status == "completed" && billingState == "reconciliation"
	}
	return false
}
func mediaSnapshotPending(s *mediaBillingSnapshot) bool {
	return s.state == "reserved" || s.state == "reconciliation"
}

// 决策表是手工核定金额的账务凭证，不伪装成供应商返回的图片数量或 Token 用量。
func readMediaBillingDecision(ctx context.Context, q mediaBillingQueryer, id int64) (*service.MediaTaskBillingReview, *service.MediaTaskSettlementResult, error) {
	r := &service.MediaTaskBillingReview{}
	result := &service.MediaTaskSettlementResult{}
	var amount float64
	err := q.QueryRowContext(ctx, `SELECT action,actor_id,reason,reserved_amount,amount,resolved_at,billing_user_id,actor_user_id,api_key_id
	 FROM media_task_billing_resolutions WHERE media_task_id=$1`, id).Scan(&r.Action, &r.ActorID, &r.Reason, &r.OriginalReservedAmount, &amount, &r.ResolvedAt, &result.UserID, &result.ActorUserID, &result.APIKeyID)
	if err != nil {
		return nil, nil, err
	}
	r.ActualAmount = &amount
	switch r.Action {
	case "charge":
		r.Status = "charged"
	case "expire":
		r.Status = "expired"
	default:
		r.Status = "released"
	}
	return r, result, nil
}

func (r *mediaTaskRepository) GetMediaTaskBillingReview(ctx context.Context, task *service.MediaTask) (*service.MediaTaskBillingReview, error) {
	decision, _, err := readMediaBillingDecision(ctx, r.db, task.ID)
	if err == nil {
		return decision, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	review := &service.MediaTaskBillingReview{Status: "unreserved"}
	snapshot, err := readMediaBillingSnapshot(ctx, r.db, task, false)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, service.ErrImageBillingReservationNotFound) || errors.Is(err, service.ErrVideoTaskNotFound) {
		if task.ActualCost != nil {
			review.Status = "charged"
			review.ActualAmount = task.ActualCost
		}
		return review, nil
	}
	if err != nil {
		return nil, err
	}
	balance, sub := mediaSnapshotAmount(snapshot)
	review.OriginalReservedAmount = balance + sub
	switch snapshot.state {
	case "captured", "settled":
		review.Status = "charged"
		review.ActualAmount = snapshot.actual
	case "released":
		review.Status = "released"
		zero := 0.0
		review.ActualAmount = &zero
	case "reserved", "reconciliation":
		review.Status = "pending"
		review.HeldBalanceAmount, review.HeldSubscriptionAmount = balance, sub
		review.CanRelease = mediaManualSettlementEligible(task.Source, task.Status, snapshot.state) && (snapshot.sourceStatus == "" || mediaManualSettlementEligible(task.Source, snapshot.sourceStatus, snapshot.state)) && (task.ActualCost == nil || *task.ActualCost == 0)
		review.CanCharge = review.CanRelease && balance+sub > 0 && !snapshot.hold.VideoDeferredBilling
	}
	return review, nil
}

// ResolveMediaTaskBilling 的资金、最终决定及投影在同一事务内提交；不同动作竞争只允许一方成功。
// 锁顺序为付款用户 -> 媒体投影 -> 原资金账本，不等待自动结算已经持有的 usage 去重键。
func (r *mediaTaskRepository) ResolveMediaTaskBilling(ctx context.Context, cmd service.MediaTaskSettlementCommand) (*service.MediaTaskSettlementResult, error) {
	if cmd.TaskID <= 0 || (cmd.Action != "charge" && cmd.Action != "release" && cmd.Action != "expire") ||
		(cmd.Action == "expire" && (cmd.ActorID != 0 || cmd.FailedBefore == nil)) || (cmd.Action != "expire" && cmd.ActorID <= 0) {
		return nil, service.ErrMediaTaskInvalid
	}
	return retryPostgresDeadlock(ctx, "media_task_settlement", 0, func() (*service.MediaTaskSettlementResult, error) { return r.resolveMediaTaskBillingOnce(ctx, cmd) })
}

func (r *mediaTaskRepository) resolveMediaTaskBillingOnce(ctx context.Context, cmd service.MediaTaskSettlementCommand) (*service.MediaTaskSettlementResult, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	query := "SELECT " + strings.Replace(mediaTaskColumns, mediaTaskStatusSQL, "t.status", 1) + mediaTaskJoins + " WHERE t.id=$1"
	task, err := scanMediaTask(tx.QueryRowContext(ctx, query, cmd.TaskID))
	if err != nil {
		return nil, err
	}
	initial, err := readMediaBillingSnapshot(ctx, tx, task, false)
	if err != nil {
		return nil, service.ErrMediaTaskSettlementConflict
	}
	if err = lockUsageBillingUser(ctx, tx, initial.hold.UserID); err != nil {
		return nil, err
	}
	task, err = scanMediaTask(tx.QueryRowContext(ctx, query+" FOR UPDATE OF t", cmd.TaskID))
	if err != nil {
		return nil, err
	}
	decision, previous, err := readMediaBillingDecision(ctx, tx, cmd.TaskID)
	if err == nil {
		if decision.Action != cmd.Action {
			return nil, service.ErrMediaTaskSettlementConflict
		}
		return previous, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// 自动过期只处理失败类状态；人工核定额外覆盖已完成但资金待核对的异步图片和视频。
	eligible := func(status, _ string) bool { return mediaSettlementEligible(status) }
	if cmd.Action != "expire" {
		eligible = func(status, state string) bool { return mediaManualSettlementEligible(task.Source, status, state) }
	}
	if !eligible(task.Status, initial.state) {
		return nil, service.ErrMediaTaskSettlementConflict
	}
	failedAt := task.UpdatedAt
	if task.CompletedAt != nil {
		failedAt = *task.CompletedAt
	}
	if cmd.FailedBefore != nil && failedAt.After(*cmd.FailedBefore) {
		return nil, service.ErrMediaTaskSettlementConflict
	}
	// 读日志不能替代账本，但已有收费记录时绝不把未确认的冻结重复处理。
	if task.ActualCost != nil && *task.ActualCost > 0 {
		return nil, service.ErrMediaTaskSettlementConflict
	}
	locked, err := readMediaBillingSnapshot(ctx, tx, task, true)
	if err != nil || locked.hold.UserID != initial.hold.UserID || !mediaSnapshotPending(locked) || !eligible(task.Status, locked.state) || (locked.sourceStatus != "" && !eligible(locked.sourceStatus, locked.state)) {
		return nil, service.ErrMediaTaskSettlementConflict
	}
	balance, sub := mediaSnapshotAmount(locked)
	if math.IsInf(balance+sub, 0) || (cmd.Action == "charge" && (balance+sub <= 0 || locked.hold.VideoDeferredBilling)) {
		return nil, service.ErrMediaTaskSettlementConflict
	}
	var settled *service.BatchImageBalanceHoldResult
	if task.Source == "video" {
		settled, err = resolveVideoMediaTaskBilling(ctx, tx, task, cmd.Action)
	} else {
		settled, err = resolveImageMediaTaskBilling(ctx, tx, task, locked, cmd.Action)
	}
	if err != nil {
		if errors.Is(err, service.ErrVideoTaskConflict) {
			return nil, service.ErrMediaTaskSettlementConflict
		}
		return nil, err
	}
	var actor any
	if cmd.ActorID > 0 {
		actor = cmd.ActorID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO media_task_billing_resolutions
	 (media_task_id,action,actor_id,reason,billing_user_id,actor_user_id,api_key_id,reserved_amount,amount,balance_amount,subscription_amount)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, task.ID, cmd.Action, actor, cmd.Reason, locked.hold.UserID, locked.hold.ActorUserID, task.APIKeyID, balance+sub, settled.ActualAmountUSD, settled.BalanceAmountUSD, settled.SubscriptionAmountUSD)
	if err != nil {
		return nil, err
	}
	if cmd.Action == "expire" {
		_, err = tx.ExecContext(ctx, `UPDATE media_tasks SET status='expired',updated_at=NOW() WHERE id=$1`, task.ID)
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &service.MediaTaskSettlementResult{UserID: locked.hold.UserID, ActorUserID: locked.hold.ActorUserID, APIKeyID: task.APIKeyID}, nil
}

// 查询只选真实待核对预留；多实例领取后仍在资金锁内复核，因此重复扫描不会重复退款。
func (r *mediaTaskRepository) ListExpiredMediaTaskBilling(ctx context.Context, cutoff time.Time, limit int) ([]int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `SELECT t.id FROM media_tasks t
	 LEFT JOIN image_billing_reservations i ON t.source='async_image' AND i.request_id=t.request_id AND i.api_key_id=t.api_key_id
	 LEFT JOIN video_tasks v ON t.source='video' AND v.id=t.task_id AND v.api_key_id=t.api_key_id AND v.user_id=t.user_id
	 WHERE t.status IN ('failed','cancelled','expired') AND COALESCE(t.completed_at,t.updated_at)<=$1
	 AND (t.billing_review_retry_at IS NULL OR t.billing_review_retry_at<=NOW())
	 AND NOT EXISTS(SELECT 1 FROM usage_logs u WHERE u.request_id=t.request_id AND u.api_key_id=t.api_key_id AND u.actual_cost>0)
	 AND NOT EXISTS(SELECT 1 FROM media_task_billing_resolutions d WHERE d.media_task_id=t.id)
	 AND (i.state IN ('reserved','reconciliation') OR v.billing_status IN ('reserved','reconciliation'))
	 ORDER BY COALESCE(t.completed_at,t.updated_at),t.id LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// 异常账本短暂退避，避免永久冲突占满扫描窗口；不刷新首次失败时间。
func (r *mediaTaskRepository) DeferMediaTaskBillingReview(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE media_tasks SET billing_review_retry_at=NOW()+INTERVAL '5 minutes' WHERE id=$1`, id)
	return err
}

// 未知图片结果由管理员按原预留核定，不编造图片张数、上游用量或当前价卡。
func resolveImageMediaTaskBilling(ctx context.Context, tx *sql.Tx, task *service.MediaTask, snapshot *mediaBillingSnapshot, action string) (*service.BatchImageBalanceHoldResult, error) {
	hold := &snapshot.hold
	var used bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2
	 UNION ALL SELECT 1 FROM usage_billing_dedup_archive WHERE request_id=$1 AND api_key_id=$2)`, task.RequestID, task.APIKeyID).Scan(&used); err != nil {
		return nil, err
	}
	if used {
		return nil, service.ErrMediaTaskSettlementConflict
	}
	balance, sub := mediaSnapshotAmount(snapshot)
	result := &service.BatchImageBalanceHoldResult{HoldAmountUSD: balance + sub}
	state := "released"
	if action == "charge" {
		balanceCmd := *hold
		balanceCmd.HoldAmount, balanceCmd.ActualAmount = balance, balance
		if _, err := captureUsageBillingBatchImageBalance(ctx, tx, &balanceCmd); err != nil {
			return nil, err
		}
		result.ActualAmountUSD, result.BalanceAmountUSD, result.SubscriptionAmountUSD = balance+sub, balance, sub
		result.BillingAllocations = append(result.BillingAllocations, hold.SubscriptionHoldAllocations...)
		if balance > 0 {
			result.BillingAllocations = append(result.BillingAllocations, domain.BillingAllocation{Type: domain.BillingAllocationTypeBalance, AmountUSD: balance})
		}
		billing := &service.UsageBillingCommand{RequestID: fmt.Sprintf("media_task_charge:%d", task.ID), UserID: hold.UserID, ActorUserID: hold.ActorUserID, APIKeyID: hold.APIKeyID, GroupID: hold.GroupID, TeamID: hold.TeamID,
			QuotaPlatform: hold.QuotaPlatform, APIKeyQuotaCost: balance + sub, APIKeyRateLimitCost: balance + sub}
		applied := &service.UsageBillingApplyResult{Applied: true, BalanceAmountUSD: balance, SubscriptionAmountUSD: sub, BillingAllocations: result.BillingAllocations}
		billing.Normalize()
		if err := applyUsageBillingAllowanceEffects(ctx, tx, billing, applied); err != nil {
			return nil, err
		}
		if err := applyUsageBillingPlatformQuota(ctx, tx, billing, balance); err != nil {
			return nil, err
		}
		if err := recordDailyActivitySpending(ctx, tx, hold.UserID, hold.APIKeyID, billing.RequestID, sub, balance); err != nil {
			return nil, err
		}
		state = "captured"
	} else {
		if err := releaseBatchImageSubscriptionAllocations(ctx, tx, hold, hold.SubscriptionHoldAllocations); err != nil {
			return nil, err
		}
		if _, err := releaseUsageBillingBatchImageFrozenBalance(ctx, tx, hold.UserID, balance); err != nil {
			return nil, err
		}
	}
	// 账本终态与永久决策共同阻止原自动补账恢复；晚到结果不能重启扣费。
	encoded, err := json.Marshal(&service.UsageBillingApplyResult{Applied: true, BalanceAmountUSD: result.BalanceAmountUSD, SubscriptionAmountUSD: result.SubscriptionAmountUSD, BillingAllocations: result.BillingAllocations})
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE image_billing_reservations SET state=$3,result=$4::jsonb,capture_fingerprint=$5,
	 settlement_completed_at=NOW(),settlement_retry_at=NULL,updated_at=NOW() WHERE request_id=$1 AND api_key_id=$2`, task.RequestID, task.APIKeyID, state, string(encoded), fmt.Sprintf("media_task:%d:%s", task.ID, action))
	return result, err
}
