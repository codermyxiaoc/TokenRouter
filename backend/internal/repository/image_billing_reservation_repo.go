package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

var _ service.ImageBillingReservationRepository = (*usageBillingRepository)(nil)

// imageBillingStoredReservation 的快照只保存计费元数据，独立于图片任务短期结果。
type imageBillingStoredReservation struct {
	service.ImageBillingReservation
	fingerprint        string
	captureFingerprint string
}

// ReserveImageBilling 在付款用户锁内预占订阅和余额；不足时整笔回滚，不创建可执行任务。
func (r *usageBillingRepository) ReserveImageBilling(ctx context.Context, input *service.ImageBillingReserveCommand) (*service.ImageBillingReservation, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("image billing repository db is nil")
	}
	cmd, quote, expiresAt, err := normalizeImageBillingReserve(input)
	if err != nil {
		return nil, err
	}
	return retryPostgresDeadlock(ctx, "image_billing_reserve", 0, func() (*service.ImageBillingReservation, error) {
		return r.reserveImageBillingOnce(ctx, cmd, quote, expiresAt)
	})
}

func normalizeImageBillingReserve(input *service.ImageBillingReserveCommand) (service.BatchImageBalanceHoldCommand, json.RawMessage, time.Time, error) {
	if input == nil {
		return service.BatchImageBalanceHoldCommand{}, nil, time.Time{}, service.ErrImageBillingReservationInvalid
	}
	cmd := cloneBatchImageBalanceHoldCommand(&input.Hold)
	mode, validMode := service.NormalizeAPIKeyBillingMode(cmd.APIKeyBillingMode)
	if !validMode || cmd.UserID <= 0 || cmd.APIKeyID <= 0 || cmd.GroupID == nil || *cmd.GroupID <= 0 ||
		strings.TrimSpace(cmd.RequestID) == "" || len(cmd.RequestID) > 255 || cmd.CreativeEntity || cmd.VideoEntity ||
		cmd.VideoDeferredBilling || cmd.VideoTokenPrepay || cmd.VideoFixedAmountUSD != 0 || cmd.VideoActualFixedAmountUSD != 0 {
		return cmd, nil, time.Time{}, service.ErrImageBillingReservationInvalid
	}
	for _, value := range []float64{cmd.BaseAmountUSD, cmd.SubscriptionRateMultiplier, cmd.SubscriptionRateMultiplierScale, cmd.BalanceRateMultiplier} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return cmd, nil, time.Time{}, service.ErrImageBillingReservationInvalid
		}
	}
	if mode == service.APIKeyBillingModeSubscription && (cmd.PreferredSubscriptionID == nil || *cmd.PreferredSubscriptionID <= 0) {
		return cmd, nil, time.Time{}, service.ErrPreferredSubscriptionInvalid
	}
	cmd.RequestID = strings.TrimSpace(cmd.RequestID)
	cmd.APIKeyBillingMode = mode
	if mode != service.APIKeyBillingModeSubscription {
		cmd.PreferredSubscriptionID = nil
	}
	if cmd.ActorUserID <= 0 {
		cmd.ActorUserID = cmd.UserID
	}
	cmd.PricingSnapshotVersion = 3
	cmd.SettlementRateScale = 1
	if cmd.SubscriptionRateMultiplierScale == 0 {
		cmd.SubscriptionRateMultiplierScale = 1
	}
	// 金额和分配只能由事务生成，不能接受调用方伪造的冻结余额快照。
	cmd.HoldAmount, cmd.ActualAmount, cmd.ActualBaseAmountUSD, cmd.BalanceHoldAmount = 0, 0, 0, 0
	cmd.SubscriptionHoldAllocations = nil
	cmd.AllowanceReserved = false
	cmd.BatchID = cmd.RequestID
	cmd.ReservedAt = time.Now().UTC()
	quote := json.RawMessage(`{}`)
	if len(input.Quote) > 0 {
		if len(input.Quote) > 64*1024 {
			return cmd, nil, time.Time{}, service.ErrImageBillingReservationInvalid
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(input.Quote, &object); err != nil || object == nil {
			return cmd, nil, time.Time{}, service.ErrImageBillingReservationInvalid
		}
		var err error
		quote, err = json.Marshal(object)
		if err != nil {
			return cmd, nil, time.Time{}, err
		}
	}
	// 重试时不把时间和事务生成字段纳入指纹，原价格和资金归属必须相同。
	fingerprintCommand := cmd
	fingerprintCommand.ReservedAt = time.Time{}
	fingerprintCommand.RequestFingerprint = ""
	encoded, err := json.Marshal(struct {
		Hold  service.BatchImageBalanceHoldCommand
		Quote json.RawMessage
	}{fingerprintCommand, quote})
	if err != nil {
		return cmd, nil, time.Time{}, err
	}
	cmd.RequestFingerprint = imageBillingFingerprint(encoded)
	expiresAt := input.ExpiresAt.UTC()
	if expiresAt.IsZero() {
		expiresAt = cmd.ReservedAt.Add(35 * time.Minute)
	}
	if !expiresAt.After(cmd.ReservedAt) {
		return cmd, nil, time.Time{}, service.ErrImageBillingReservationInvalid
	}
	return cmd, quote, expiresAt, nil
}

func (r *usageBillingRepository) reserveImageBillingOnce(ctx context.Context, original service.BatchImageBalanceHoldCommand, quote json.RawMessage, expiresAt time.Time) (*service.ImageBillingReservation, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	cmd := cloneBatchImageBalanceHoldCommand(&original)
	// 与普通计费及其他预占统一锁顺序：付款用户、预占记录、订阅。
	if err := lockUsageBillingUser(ctx, tx, cmd.UserID); err != nil {
		return nil, err
	}
	existing, err := readImageBillingReservation(ctx, tx, cmd.RequestID, cmd.APIKeyID)
	if err == nil {
		if existing.Hold.UserID != cmd.UserID || existing.fingerprint != cmd.RequestFingerprint {
			return nil, service.ErrImageBillingReservationConflict
		}
		if existing.State == service.ImageBillingReserved && !existing.ExpiresAt.After(time.Now()) {
			if _, err := tx.ExecContext(ctx, `UPDATE image_billing_reservations SET state = 'reconciliation', updated_at = NOW() WHERE request_id = $1 AND api_key_id = $2`, cmd.RequestID, cmd.APIKeyID); err != nil {
				return nil, err
			}
			existing.State = service.ImageBillingReconciliation
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &existing.ImageBillingReservation, nil
	}
	if !errors.Is(err, service.ErrImageBillingReservationNotFound) {
		return nil, err
	}
	// 历史后付费已经结算的请求不能在升级后再创建一份预占。
	settled, err := batchImageHoldClaimExists(ctx, tx, cmd.RequestID, cmd.APIKeyID)
	if err != nil {
		return nil, err
	}
	if settled {
		return nil, service.ErrImageBillingReservationConflict
	}
	allocationCommand := &service.UsageBillingCommand{
		UserID: cmd.UserID, GroupID: cmd.GroupID, APIKeyBillingMode: cmd.APIKeyBillingMode,
		PreferredSubscriptionID: cmd.PreferredSubscriptionID, BaseAmountUSD: cmd.BaseAmountUSD,
		SubscriptionRateMultiplier: cmd.SubscriptionRateMultiplier, SubscriptionRateMultiplierScale: cmd.SubscriptionRateMultiplierScale,
		BalanceRateMultiplier: cmd.BalanceRateMultiplier, DisablePlanGroupRateMultiplier: cmd.DisablePlanGroupRateMultiplier,
		IncludeAllocationPricing: true, PreserveZeroRateAllocation: true,
	}
	remaining, _, allocations, err := allocateUsageBillingSubscriptions(ctx, tx, allocationCommand)
	if err != nil {
		return nil, err
	}
	if cmd.APIKeyBillingMode == service.APIKeyBillingModeSubscription && remaining > 1e-10 {
		return nil, service.ErrPreferredSubscriptionInsufficient
	}
	cmd.BalanceHoldAmount = service.QuantizeUsageBillingAmount(remaining * cmd.BalanceRateMultiplier)
	cmd.SubscriptionHoldAllocations = allocations
	cmd.HoldAmount = cmd.BalanceHoldAmount
	for _, allocation := range allocations {
		cmd.HoldAmount += allocation.AmountUSD
	}
	balanceCommand := cmd
	balanceCommand.HoldAmount = cmd.BalanceHoldAmount
	if _, err := reserveUsageBillingBatchImageBalance(ctx, tx, &balanceCommand); err != nil {
		return nil, err
	}
	snapshot, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO image_billing_reservations
		(request_id, api_key_id, user_id, fingerprint, state, snapshot, quote, expires_at)
		VALUES ($1, $2, $3, $4, 'reserved', $5::jsonb, $6::jsonb, $7)`,
		cmd.RequestID, cmd.APIKeyID, cmd.UserID, cmd.RequestFingerprint, string(snapshot), string(quote), expiresAt)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &service.ImageBillingReservation{ID: cmd.RequestID, State: service.ImageBillingReserved, Applied: true,
		Hold: cmd, Quote: append(json.RawMessage(nil), quote...), ExpiresAt: expiresAt}, nil
}

func readImageBillingReservation(ctx context.Context, tx *sql.Tx, id string, apiKeyID int64, lockRow ...bool) (*imageBillingStoredReservation, error) {
	var snapshot, quote, result []byte
	var stored imageBillingStoredReservation
	var ownerID int64
	query := `SELECT user_id, state, fingerprint, snapshot, quote, capture_fingerprint, result, expires_at
		FROM image_billing_reservations WHERE request_id = $1 AND api_key_id = $2`
	if len(lockRow) == 0 || lockRow[0] {
		query += " FOR UPDATE"
	}
	err := tx.QueryRowContext(ctx, query, id, apiKeyID).
		Scan(&ownerID, &stored.State, &stored.fingerprint, &snapshot, &quote, &stored.captureFingerprint, &result, &stored.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrImageBillingReservationNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(snapshot, &stored.Hold); err != nil {
		return nil, err
	}
	if stored.Hold.UserID != ownerID || stored.Hold.APIKeyID != apiKeyID || stored.Hold.RequestID != id {
		return nil, service.ErrImageBillingReservationConflict
	}
	stored.ID, stored.Quote = id, quote
	if len(result) > 0 && !bytes.Equal(result, []byte("null")) {
		if err := json.Unmarshal(result, &stored.Result); err != nil {
			return nil, err
		}
	}
	return &stored, nil
}

// CaptureImageBilling 将冻结金额捕获和普通用量去重、配额统计放在同一事务中。
func (r *usageBillingRepository) CaptureImageBilling(ctx context.Context, id string, input *service.UsageBillingCommand, actualBaseAmount float64) (*service.UsageBillingApplyResult, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("image billing repository db is nil")
	}
	if input == nil || id == "" || input.RequestID != id || input.UserID <= 0 || input.APIKeyID <= 0 ||
		math.IsNaN(actualBaseAmount) || math.IsInf(actualBaseAmount, 0) || actualBaseAmount < 0 {
		return nil, service.ErrImageBillingReservationInvalid
	}
	return retryPostgresDeadlock(ctx, "image_billing_capture", 0, func() (*service.UsageBillingApplyResult, error) {
		return r.captureImageBillingOnce(ctx, id, input, actualBaseAmount)
	})
}

func (r *usageBillingRepository) captureImageBillingOnce(ctx context.Context, id string, input *service.UsageBillingCommand, actualBaseAmount float64) (*service.UsageBillingApplyResult, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// 先读取不可变价格合同，以普通账本相同顺序认领去重键，再锁付款用户和预占行。
	// 若反过来持有用户锁再等普通去重键，会与历史后付费请求形成锁环。
	stored, err := readImageBillingReservation(ctx, tx, id, input.APIKeyID, false)
	if err != nil {
		return nil, err
	}
	if stored.Hold.UserID != input.UserID || stored.Hold.ActorUserID != input.ActorUserID ||
		!imageBillingOptionalIDEqual(stored.Hold.TeamID, input.TeamID) || !imageBillingOptionalIDEqual(stored.Hold.GroupID, input.GroupID) {
		return nil, service.ErrImageBillingReservationConflict
	}
	if stored.State == service.ImageBillingReleased {
		return nil, service.ErrImageBillingReservationReleased
	}
	plan, err := planImageBillingCapture(&stored.Hold, actualBaseAmount)
	if err != nil {
		return nil, err
	}
	cmd := *input
	cmd.BaseAmountUSD, cmd.BillableAmountUSD = actualBaseAmount, plan.ActualAmountUSD
	cmd.APIKeyBillingMode, cmd.PreferredSubscriptionID = stored.Hold.APIKeyBillingMode, stored.Hold.PreferredSubscriptionID
	cmd.SubscriptionRateMultiplier, cmd.SubscriptionRateMultiplierScale = stored.Hold.SubscriptionRateMultiplier, stored.Hold.SubscriptionRateMultiplierScale
	cmd.BalanceRateMultiplier = stored.Hold.BalanceRateMultiplier
	cmd.DisablePlanGroupRateMultiplier = stored.Hold.DisablePlanGroupRateMultiplier
	cmd.SubscriptionScopeID = nil
	if cmd.APIKeyQuotaCost > 0 {
		cmd.APIKeyQuotaCost = plan.ActualAmountUSD
	}
	if cmd.APIKeyRateLimitCost > 0 {
		cmd.APIKeyRateLimitCost = plan.ActualAmountUSD
	}
	cmd.RequestFingerprint = ""
	cmd.Normalize()
	applied, err := r.claimUsageBillingKey(ctx, tx, &cmd)
	if err != nil {
		return nil, err
	}
	if err := lockUsageBillingUser(ctx, tx, input.UserID); err != nil {
		return nil, err
	}
	locked, err := readImageBillingReservation(ctx, tx, id, input.APIKeyID)
	if err != nil {
		return nil, err
	}
	if locked.Hold.UserID != input.UserID || locked.fingerprint != stored.fingerprint {
		return nil, service.ErrImageBillingReservationConflict
	}
	stored = locked
	if stored.State == service.ImageBillingCaptured {
		if applied || stored.captureFingerprint != cmd.RequestFingerprint || stored.Result == nil {
			return nil, service.ErrImageBillingReservationConflict
		}
		result := *stored.Result
		result.Applied = false
		return &result, nil
	}
	if stored.State == service.ImageBillingReleased {
		return nil, service.ErrImageBillingReservationReleased
	}
	if stored.State != service.ImageBillingReserved && stored.State != service.ImageBillingReconciliation {
		return nil, service.ErrImageBillingReservationConflict
	}
	if !applied {
		// 账本捕获和普通去重始终原子提交，单边存在说明不能安全再次消费冻结额。
		return nil, service.ErrImageBillingReservationConflict
	}
	if err := releaseBatchImageSubscriptionAllocations(ctx, tx, &stored.Hold, plan.SubscriptionReleases); err != nil {
		return nil, err
	}
	balanceCommand := stored.Hold
	balanceCommand.HoldAmount, balanceCommand.ActualAmount = stored.Hold.BalanceHoldAmount, plan.BalanceAmountUSD
	balance, err := captureUsageBillingBatchImageBalance(ctx, tx, &balanceCommand)
	if err != nil {
		return nil, err
	}
	result := &service.UsageBillingApplyResult{Applied: true, SubscriptionAmountUSD: plan.SubscriptionAmountUSD,
		BalanceAmountUSD: plan.BalanceAmountUSD, BillingAllocations: plan.BillingAllocations}
	if balance != nil {
		result.NewBalance = balance.NewBalance
	}
	if err := applyUsageBillingAllowanceEffects(ctx, tx, &cmd, result); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE image_billing_reservations SET state = 'captured', capture_fingerprint = $3,
		result = $4::jsonb, updated_at = NOW() WHERE request_id = $1 AND api_key_id = $2`, id, cmd.APIKeyID, cmd.RequestFingerprint, string(encoded)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// planImageBillingCapture 按冻结的基础用量及各来源倍率结算，显式免费覆盖不会回落成余额收费。
func planImageBillingCapture(hold *service.BatchImageBalanceHoldCommand, actualBaseAmount float64) (*service.BatchImageBillingCapturePlan, error) {
	if hold == nil || math.IsNaN(actualBaseAmount) || math.IsInf(actualBaseAmount, 0) || actualBaseAmount < 0 || actualBaseAmount-hold.BaseAmountUSD > 1e-10 {
		return nil, service.ErrBatchImageSettlementCostExceedsHold
	}
	plan := &service.BatchImageBillingCapturePlan{BalanceHoldAmount: hold.BalanceHoldAmount}
	remaining := actualBaseAmount
	for _, allocation := range hold.SubscriptionHoldAllocations {
		if allocation.Type != domain.BillingAllocationTypeSubscription || allocation.SubscriptionID == nil || allocation.BaseAmountUSD < 0 || allocation.AmountUSD < 0 || allocation.RateMultiplier < 0 {
			return nil, service.ErrImageBillingReservationInvalid
		}
		covered := math.Min(remaining, allocation.BaseAmountUSD)
		keptAmount := math.Min(allocation.AmountUSD, covered*allocation.RateMultiplier)
		if covered > 0 {
			kept := allocation
			kept.BaseAmountUSD, kept.AmountUSD = covered, keptAmount
			plan.BillingAllocations = append(plan.BillingAllocations, kept)
			plan.SubscriptionAmountUSD += keptAmount
			remaining = math.Max(0, remaining-covered)
		}
		if released := allocation.AmountUSD - keptAmount; released > 1e-10 {
			release := allocation
			release.AmountUSD = released
			plan.SubscriptionReleases = append(plan.SubscriptionReleases, release)
		}
	}
	plan.BalanceAmountUSD = service.QuantizeUsageBillingAmount(remaining * hold.BalanceRateMultiplier)
	if plan.BalanceAmountUSD-hold.BalanceHoldAmount > 1e-10 {
		return nil, service.ErrBatchImageSettlementCostExceedsHold
	}
	plan.BalanceAmountUSD = math.Min(plan.BalanceAmountUSD, hold.BalanceHoldAmount)
	if remaining > 0 {
		plan.BillingAllocations = append(plan.BillingAllocations, domain.BillingAllocation{Type: domain.BillingAllocationTypeBalance,
			AmountUSD: plan.BalanceAmountUSD, BaseAmountUSD: remaining, RateMultiplier: hold.BalanceRateMultiplier})
	}
	plan.ActualAmountUSD = plan.SubscriptionAmountUSD + plan.BalanceAmountUSD
	return plan, nil
}

// ReleaseImageBilling 仅释放已证实未产生费用的请求；不确定结果由调用方标记待核对。
func (r *usageBillingRepository) ReleaseImageBilling(ctx context.Context, id string, userID, apiKeyID int64) error {
	if r == nil || r.db == nil || strings.TrimSpace(id) == "" || userID <= 0 || apiKeyID <= 0 {
		return service.ErrImageBillingReservationInvalid
	}
	_, err := retryPostgresDeadlock(ctx, "image_billing_release", 0, func() (bool, error) {
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return false, err
		}
		defer func() { _ = tx.Rollback() }()
		if err := lockUsageBillingUser(ctx, tx, userID); err != nil {
			return false, err
		}
		stored, err := readImageBillingReservation(ctx, tx, id, apiKeyID)
		if err != nil {
			return false, err
		}
		if stored.Hold.UserID != userID {
			return false, service.ErrImageBillingReservationConflict
		}
		if stored.State == service.ImageBillingReleased {
			return false, nil
		}
		if stored.State != service.ImageBillingReserved && stored.State != service.ImageBillingReconciliation {
			return false, service.ErrImageBillingReservationConflict
		}
		if err := releaseBatchImageSubscriptionAllocations(ctx, tx, &stored.Hold, stored.Hold.SubscriptionHoldAllocations); err != nil {
			return false, err
		}
		if _, err := releaseUsageBillingBatchImageFrozenBalance(ctx, tx, userID, stored.Hold.BalanceHoldAmount); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE image_billing_reservations SET state = 'released', updated_at = NOW()
			WHERE request_id = $1 AND api_key_id = $2`, id, apiKeyID); err != nil {
			return false, err
		}
		return true, tx.Commit()
	})
	return err
}

// MarkImageBillingReconciliation 不改变冻结金额，允许后续拿到确定结果后继续捕获或释放。
func (r *usageBillingRepository) MarkImageBillingReconciliation(ctx context.Context, id string, userID, apiKeyID int64) error {
	if r == nil || r.db == nil || id == "" || userID <= 0 || apiKeyID <= 0 {
		return service.ErrImageBillingReservationInvalid
	}
	_, err := r.db.ExecContext(ctx, `UPDATE image_billing_reservations SET state = 'reconciliation', updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2 AND user_id = $3 AND state = 'reserved'`, id, apiKeyID, userID)
	return err
}

// ReconcileExpiredImageBilling 多实例只认领到期未结束记录，不把未知生成结果自动当作失败退款。
func (r *usageBillingRepository) ReconcileExpiredImageBilling(ctx context.Context, now time.Time, limit int) (int64, error) {
	if r == nil || r.db == nil {
		return 0, service.ErrImageBillingReservationInvalid
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	result, err := r.db.ExecContext(ctx, `WITH expired AS (
		SELECT request_id, api_key_id FROM image_billing_reservations WHERE state = 'reserved' AND expires_at <= $1
		ORDER BY expires_at, request_id, api_key_id LIMIT $2 FOR UPDATE SKIP LOCKED
	) UPDATE image_billing_reservations AS reservation SET state = 'reconciliation', updated_at = NOW()
	FROM expired WHERE reservation.request_id = expired.request_id AND reservation.api_key_id = expired.api_key_id`, now, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func imageBillingFingerprint(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func imageBillingOptionalIDEqual(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
