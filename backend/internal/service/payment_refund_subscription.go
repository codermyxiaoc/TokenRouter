package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/paymentauditlog"
	"github.com/TokenFlux/TokenRouter/ent/paymentorder"
	"github.com/TokenFlux/TokenRouter/ent/usersubscription"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

// refundSubscriptionHold 保留整份订阅撤回前的状态；不改有效期、用量或套餐链。
// 仅适用于扣减会耗尽整个订阅的退款，正常扣减有效期仍使用原有订阅服务。
type refundSubscriptionHold struct {
	OriginalStatus string    `json:"originalStatus"`
	HeldAt         time.Time `json:"heldAt"`
	StartsAt       time.Time `json:"startsAt"`
	ExpiresAt      time.Time `json:"expiresAt"`
}

// holdRefundSubscription 暂停本订单授予的订阅，渠道终态明确前不能软删除权益。
func (s *PaymentService) holdRefundSubscription(ctx context.Context, p *RefundPlan) error {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin subscription refund hold: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	sub, err := tx.UserSubscription.Query().Where(
		usersubscription.IDEQ(p.SubscriptionID), usersubscription.UserIDEQ(p.Order.UserID),
		usersubscription.SourceOrderIDEQ(p.OrderID), usersubscription.DeletedAtIsNil(),
		usersubscription.StatusIn(SubscriptionStatusActive, SubscriptionStatusPending), lockRefundSubscriptionRow,
	).Unique(false).Only(txCtx)
	if err != nil {
		return fmt.Errorf("load refundable subscription: %w", err)
	}
	hold := &refundSubscriptionHold{OriginalStatus: sub.Status, HeldAt: time.Now().UTC().Truncate(time.Microsecond), StartsAt: sub.StartsAt, ExpiresAt: sub.ExpiresAt}
	update := tx.UserSubscription.Update().Where(usersubscription.IDEQ(sub.ID), usersubscription.UpdatedAtEQ(sub.UpdatedAt), usersubscription.DeletedAtIsNil()).
		SetStatus(SubscriptionStatusSuspended).SetUpdatedAt(hold.HeldAt)
	settleRefundSubscriptionResetCounts(update.Mutation(), sub, hold.HeldAt)
	claimed, err := update.Save(txCtx)
	if err != nil {
		return fmt.Errorf("hold refundable subscription: %w", err)
	}
	if claimed != 1 {
		return infraerrors.Conflict("CONFLICT", "subscription changed during refund")
	}
	if err := createPaymentAuditLogTx(txCtx, tx.Client(), p.OrderID, "REFUND_SUBSCRIPTION_HELD", map[string]any{
		"subscriptionID": p.SubscriptionID, "subscriptionHold": hold, "deductionHeld": true,
		"deductBalance": p.DeductBalance, "deductionType": p.DeductionType, "subDaysDeducted": p.SubDaysToDeduct,
		"refundAmount": p.RefundAmount, "reason": p.Reason, "force": p.Force,
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit subscription refund hold: %w", err)
	}
	p.subscriptionHold = hold
	s.invalidateRefundSubscriptionCache(ctx, p)
	return nil
}

// settleHeldRefundSubscription 只操作原订单、原版本的暂停快照，管理员修改后不能盲目覆盖。
// 调用方必须在订单终态事务内调用，避免权益变更成功但订单仍处于 pending。
func (s *PaymentService) settleHeldRefundSubscription(ctx context.Context, p *RefundPlan, refundSucceeded bool) error {
	if p.subscriptionHold == nil {
		return nil
	}
	tx := dbent.TxFromContext(ctx)
	if tx == nil {
		return fmt.Errorf("subscription refund settlement requires transaction")
	}
	hold := p.subscriptionHold
	status := hold.OriginalStatus
	if !refundSucceeded {
		if !hold.ExpiresAt.After(time.Now()) {
			status = SubscriptionStatusExpired
		} else if time.Now().Before(hold.StartsAt) {
			status = SubscriptionStatusPending
		}
	} else {
		status = SubscriptionStatusSuspended
	}
	row, err := tx.UserSubscription.Query().Where(
		usersubscription.IDEQ(p.SubscriptionID), usersubscription.UserIDEQ(p.Order.UserID),
		usersubscription.SourceOrderIDEQ(p.OrderID), usersubscription.DeletedAtIsNil(),
		usersubscription.StatusEQ(SubscriptionStatusSuspended), usersubscription.UpdatedAtEQ(hold.HeldAt),
		usersubscription.StartsAtEQ(hold.StartsAt), usersubscription.ExpiresAtEQ(hold.ExpiresAt), lockRefundSubscriptionRow,
	).Unique(false).Only(ctx)
	if dbent.IsNotFound(err) {
		return infraerrors.Conflict("REFUND_SUBSCRIPTION_CHANGED", "held subscription changed; verify manually")
	}
	if err != nil {
		return fmt.Errorf("lock held subscription: %w", err)
	}
	update := tx.UserSubscription.UpdateOneID(row.ID).SetStatus(status)
	// 暂停段只推进统计水位；不能恢复 active 后再把暂停期间的重置点累计进去。
	settleRefundSubscriptionResetCounts(update.Mutation(), row, time.Now())
	if _, err := update.Save(ctx); err != nil {
		return fmt.Errorf("restore held subscription: %w", err)
	}
	if refundSucceeded {
		// 终态成功才沿用原撤销逻辑推进套餐链；失败恢复不增加天数或清零用量。
		if err := s.subscriptionSvc.RevokeSubscription(ctx, p.SubscriptionID); err != nil {
			return fmt.Errorf("revoke held subscription: %w", err)
		}
	}
	return nil
}

// rollbackHeldRefundSubscription 为提交渠道立即失败的路径复用相同的受保护恢复逻辑。
func (s *PaymentService) rollbackHeldRefundSubscription(ctx context.Context, p *RefundPlan) error {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.settleHeldRefundSubscription(dbent.NewTxContext(ctx, tx), p, false); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateRefundSubscriptionCache(ctx, p)
	return nil
}

// invalidateRefundSubscriptionCache 只在提交后失效用户鉴权/API Key 快照。
// 当前计费缓存不缓存订阅状态，额度与状态准入仍读取订阅仓储，不复用余额缓存。
func (s *PaymentService) invalidateRefundSubscriptionCache(ctx context.Context, p *RefundPlan) {
	if p.DeductionType != payment.DeductionTypeSubscription || s.redeemService == nil {
		return
	}
	cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	s.redeemService.invalidateRedeemCaches(cacheCtx, p.Order.UserID, &RedeemCode{Type: RedeemTypeSubscription})
}

// recoverInterruptedSubscriptionRefund 从持久化暂停快照恢复可审计的待确认状态。
// 渠道请求是否已发送无法由本地崩溃判断；没有本次退款号时必须人工核实，绝不重发或归还。
func (s *PaymentService) recoverInterruptedSubscriptionRefund(ctx context.Context, o *dbent.PaymentOrder) (*dbent.PaymentOrder, error) {
	if o.Status != OrderStatusRefunding {
		return o, nil
	}
	if time.Since(o.UpdatedAt) < 15*time.Minute {
		return nil, infraerrors.Conflict("REFUND_IN_PROGRESS", "refund submission is still in progress")
	}
	entry, err := s.entClient.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(o.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUBSCRIPTION_HELD"), paymentauditlog.CreatedAtGTE(o.UpdatedAt)).Order(paymentauditlog.ByCreatedAt(sql.OrderDesc()), paymentauditlog.ByID(sql.OrderDesc())).First(ctx)
	if err != nil {
		return nil, fmt.Errorf("load interrupted subscription refund: %w", err)
	}
	var detail refundPendingAuditDetail
	if err := json.Unmarshal([]byte(entry.Detail), &detail); err != nil {
		return nil, fmt.Errorf("parse interrupted subscription refund: %w", err)
	}
	if detail.SubscriptionHold == nil || !detail.DeductionHeld || detail.DeductionType != payment.DeductionTypeSubscription || detail.SubscriptionID <= 0 {
		return nil, fmt.Errorf("interrupted subscription refund has no valid held snapshot")
	}
	detail.SubmissionUncertain = true
	raw, err := json.Marshal(detail)
	if err != nil {
		return nil, err
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	// 旧尝试的审计不能恢复当前尝试；当前订阅也必须仍是相同暂停版本。
	hold := detail.SubscriptionHold
	valid, err := tx.UserSubscription.Query().Where(usersubscription.IDEQ(detail.SubscriptionID), usersubscription.UserIDEQ(o.UserID), usersubscription.SourceOrderIDEQ(o.ID),
		usersubscription.StatusEQ(SubscriptionStatusSuspended), usersubscription.UpdatedAtEQ(hold.HeldAt), usersubscription.DeletedAtIsNil(),
		usersubscription.StartsAtEQ(hold.StartsAt), usersubscription.ExpiresAtEQ(hold.ExpiresAt)).Exist(txCtx)
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, infraerrors.Conflict("REFUND_SUBSCRIPTION_CHANGED", "interrupted refund snapshot no longer matches subscription; verify manually")
	}
	claimed, err := tx.PaymentOrder.Update().Where(paymentorder.IDEQ(o.ID), paymentorder.StatusEQ(OrderStatusRefunding), paymentorder.UpdatedAtEQ(o.UpdatedAt)).
		SetStatus(OrderStatusRefundPending).SetRefundAmount(detail.RefundAmount).SetRefundReason(detail.Reason).SetForceRefund(detail.Force).Save(txCtx)
	if err != nil {
		return nil, err
	}
	if claimed != 1 {
		return nil, infraerrors.Conflict("CONFLICT", "refund state changed during recovery")
	}
	if _, err := tx.PaymentAuditLog.Create().SetOrderID(strconv.FormatInt(o.ID, 10)).SetAction("REFUND_PENDING").SetOperator("system").SetDetail(string(raw)).Save(txCtx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateRefundSubscriptionCache(ctx, s.refundFinalizePlan(o, detail))
	return s.entClient.PaymentOrder.Get(ctx, o.ID)
}

// lockRefundSubscriptionRow 与仓储状态变更使用相同的行锁边界。
// SQLite 单元夹具使用事务写锁及条件更新，线上 PostgreSQL 必须先锁定最新计数快照。
func lockRefundSubscriptionRow(selector *sql.Selector) {
	if selector.Dialect() != dialect.SQLite {
		selector.ForUpdate()
	}
}

// settleRefundSubscriptionResetCounts 按锁内旧状态结清计数，并与状态写入同一条 UPDATE。
// 不改变消费额度、重置代次或窗口起点，避免退款暂停破坏 fork 的自然重置次数。
func settleRefundSubscriptionResetCounts(mutation *dbent.UserSubscriptionMutation, row *dbent.UserSubscription, now time.Time) {
	sub := &UserSubscription{
		StartsAt: row.StartsAt, ExpiresAt: row.ExpiresAt, Status: row.Status, DeletedAt: row.DeletedAt,
		DailyLimitUSD: row.DailyLimitUsd, WeeklyLimitUSD: row.WeeklyLimitUsd, MonthlyLimitUSD: row.MonthlyLimitUsd,
		DailyWindowStart: row.DailyWindowStart, WeeklyWindowStart: row.WeeklyWindowStart, MonthlyWindowStart: row.MonthlyWindowStart,
		DailyResetCount: row.DailyResetCount, WeeklyResetCount: row.WeeklyResetCount, MonthlyResetCount: row.MonthlyResetCount, ResetCountedAt: row.ResetCountedAt,
	}
	sub.AccrueScheduledResetCounts(now)
	mutation.SetDailyResetCount(sub.DailyResetCount)
	mutation.SetWeeklyResetCount(sub.WeeklyResetCount)
	mutation.SetMonthlyResetCount(sub.MonthlyResetCount)
	mutation.SetResetCountedAt(sub.ResetCountedAt)
}

// returnPartialRefundSubscription 只允许原订单仍有效的订阅恢复扣减天数。
// 已过期时 ExtendSubscription 会按当前时间续期并重置窗口，不能据此赠送等待渠道期间的额外权益。
func (s *PaymentService) returnPartialRefundSubscription(ctx context.Context, p *RefundPlan) error {
	if dbent.TxFromContext(ctx) == nil {
		tx, err := s.entClient.Tx(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err := s.returnPartialRefundSubscription(dbent.NewTxContext(ctx, tx), p); err != nil {
			return err
		}
		return tx.Commit()
	}
	tx := dbent.TxFromContext(ctx)
	row, err := tx.UserSubscription.Query().Where(usersubscription.IDEQ(p.SubscriptionID), usersubscription.UserIDEQ(p.Order.UserID),
		usersubscription.SourceOrderIDEQ(p.OrderID), usersubscription.DeletedAtIsNil(),
		usersubscription.StatusIn(SubscriptionStatusActive, SubscriptionStatusPending), lockRefundSubscriptionRow).Unique(false).Only(ctx)
	if err != nil {
		return fmt.Errorf("load refundable subscription deduction: %w", err)
	}
	if !row.ExpiresAt.After(time.Now()) {
		return infraerrors.Conflict("REFUND_MANUAL_REVIEW_REQUIRED", "deducted subscription has expired; verify original entitlement manually")
	}
	expected := row.ExpiresAt.AddDate(0, 0, p.SubDaysToDeduct)
	if expected.After(MaxExpiresAt) {
		expected = MaxExpiresAt
	}
	restored, err := s.subscriptionSvc.ExtendSubscription(ctx, p.SubscriptionID, p.SubDaysToDeduct)
	if err != nil {
		return fmt.Errorf("return held subscription deduction: %w", err)
	}
	// 防止校验后恰好越过到期点；异常续期和套餐链变更必须随外层事务一并回滚。
	if restored == nil || !restored.ExpiresAt.Equal(expected) || !restored.StartsAt.Equal(row.StartsAt) {
		return infraerrors.Conflict("REFUND_MANUAL_REVIEW_REQUIRED", "subscription period changed during refund rollback; verify manually")
	}
	return nil
}
