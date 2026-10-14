package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

var _ service.ImageBillingSettlementRepository = (*usageBillingRepository)(nil)

// SaveImageBillingSettlementEvidence 只接受原预占所有者的已确认图片元数据；首次证据不可被后续报价覆盖。
func (r *usageBillingRepository) SaveImageBillingSettlementEvidence(ctx context.Context, input *service.ImageBillingSettlementEvidence) error {
	if r == nil || r.db == nil {
		return service.ErrImageBillingReservationInvalid
	}
	evidence, encoded, err := service.NormalizeImageBillingSettlementEvidence(input)
	if err != nil {
		return err
	}
	cmd := &evidence.Command
	result, err := r.db.ExecContext(ctx, `UPDATE image_billing_reservations
		SET settlement_evidence = COALESCE(settlement_evidence, $4::jsonb),
		    settlement_retry_at = COALESCE(settlement_retry_at, NOW() + INTERVAL '1 minute')
		WHERE request_id = $1 AND api_key_id = $2 AND user_id = $3
		  AND state IN ('reserved', 'reconciliation', 'captured')
		  AND (settlement_evidence IS NULL OR settlement_evidence = $4::jsonb)`, cmd.RequestID, cmd.APIKeyID, cmd.UserID, string(encoded))
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrImageBillingReservationConflict
	}
	return nil
}

// RecoverImageBillingSettlements 只认领有成功证据的账本；未知上游结果仍留待人工核对。
// Capture 自身持久幂等；进程在捕获与日志回填之间退出时，下一次恢复不会再次扣费。
func (r *usageBillingRepository) RecoverImageBillingSettlements(ctx context.Context, now time.Time, limit int) ([]service.ImageBillingSettlementRecovered, error) {
	if r == nil || r.db == nil {
		return nil, service.ErrImageBillingReservationInvalid
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := r.db.QueryContext(ctx, `WITH due AS (
		SELECT request_id, api_key_id FROM image_billing_reservations
		WHERE settlement_evidence IS NOT NULL AND settlement_completed_at IS NULL
		  AND settlement_retry_at <= $1 AND state IN ('reserved', 'reconciliation', 'captured')
		ORDER BY settlement_retry_at, request_id, api_key_id LIMIT $2 FOR UPDATE SKIP LOCKED
	) UPDATE image_billing_reservations AS r SET
		settlement_retry_at = $1 + LEAST(900, 60 * (r.settlement_attempts + 1)) * INTERVAL '1 second',
		settlement_attempts = r.settlement_attempts + 1
	FROM due WHERE r.request_id = due.request_id AND r.api_key_id = due.api_key_id
	RETURNING r.settlement_evidence`, now, limit)
	if err != nil {
		return nil, err
	}
	var evidenceList []*service.ImageBillingSettlementEvidence
	var allErr error
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			allErr = errors.Join(allErr, err)
			continue
		}
		var item service.ImageBillingSettlementEvidence
		if err := json.Unmarshal(raw, &item); err != nil {
			allErr = errors.Join(allErr, service.ErrImageBillingReservationInvalid)
			continue
		}
		safe, _, err := service.NormalizeImageBillingSettlementEvidence(&item)
		if err != nil {
			allErr = errors.Join(allErr, err)
			continue
		}
		evidenceList = append(evidenceList, safe)
	}
	allErr = errors.Join(allErr, rows.Err(), rows.Close())
	var recovered []service.ImageBillingSettlementRecovered
	for _, evidence := range evidenceList {
		cmd := &evidence.Command
		result, err := r.CaptureImageBilling(ctx, cmd.RequestID, cmd, cmd.BaseAmountUSD)
		if err != nil {
			allErr = errors.Join(allErr, err)
			continue
		}
		if result == nil {
			allErr = errors.Join(allErr, service.ErrImageBillingReservationConflict)
			continue
		}
		if err := r.completeImageBillingSettlement(ctx, evidence, result); err != nil {
			allErr = errors.Join(allErr, err)
			continue
		}
		recovered = append(recovered, service.ImageBillingSettlementRecovered{UserID: cmd.UserID, ActorUserID: cmd.ActorUserID, APIKeyID: cmd.APIKeyID, AccountID: cmd.AccountID})
	}
	return recovered, allErr
}

// completeImageBillingSettlement 在单事务内补齐真实日志并确认恢复完成；只允许修正同所有者的未扣费占位行。
func (r *usageBillingRepository) completeImageBillingSettlement(ctx context.Context, evidence *service.ImageBillingSettlementEvidence, result *service.UsageBillingApplyResult) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	cmd := &evidence.Command
	stored, err := readImageBillingReservation(ctx, tx, cmd.RequestID, cmd.APIKeyID)
	if err != nil {
		return err
	}
	if stored.State != service.ImageBillingCaptured || stored.Result == nil || stored.Hold.UserID != cmd.UserID {
		return service.ErrImageBillingReservationConflict
	}
	log := evidence.Usage
	// 以锁内读取的最终账本分配为准，不能使用重试前的预估金额。
	service.ApplyImageBillingSettlementResult(&log, stored.Result)
	usageRepo := &usageLogRepository{}
	if _, err := usageRepo.createSingle(ctx, tx, &log); err != nil {
		return err
	}
	allocations, err := json.Marshal(log.BillingAllocations)
	if err != nil {
		return err
	}
	if string(allocations) == "null" {
		allocations = []byte("[]")
	}
	updated, err := tx.ExecContext(ctx, `UPDATE usage_logs SET
		actual_cost=$6, total_cost=$7, subscription_amount_usd=$8, balance_amount_usd=$9,
		billing_allocations=$10::jsonb, rate_multiplier=$11, billing_type=$12, subscription_id=$13
		WHERE request_id=$1 AND api_key_id=$2 AND user_id=$3 AND billing_user_id=$4 AND account_id=$5
		AND (actual_cost=0 OR ABS(actual_cost-$6) < 0.00000001)`, cmd.RequestID, cmd.APIKeyID, log.UserID, cmd.UserID, cmd.AccountID,
		log.ActualCost, log.TotalCost, log.SubscriptionAmountUSD, log.BalanceAmountUSD, string(allocations), log.RateMultiplier, log.BillingType, log.SubscriptionID)
	if err != nil {
		return err
	}
	affected, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return service.ErrImageBillingReservationConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE image_billing_reservations SET settlement_completed_at=NOW(), updated_at=NOW()
		WHERE request_id=$1 AND api_key_id=$2 AND state='captured'`, cmd.RequestID, cmd.APIKeyID); err != nil {
		return err
	}
	return tx.Commit()
}
