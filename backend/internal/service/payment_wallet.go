package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/paymentorder"
	dbuser "github.com/TokenFlux/TokenRouter/ent/user"
	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

// PaymentTypeWallet 表示站内可用余额支付，与给余额加款的订单类型分开校验。
const PaymentTypeWallet = "balance"

// createWalletSubscriptionOrder 在同一事务中扣款、发放订阅并完成订单；任一步失败全部回滚。
// @project-doc docs/domains/payments_and_entitlements.md#wallet_subscription_payment
func (s *PaymentService) createWalletSubscriptionOrder(ctx context.Context, req CreateOrderRequest, cfg *PaymentConfig) (*CreateOrderResponse, error) {
	if req.OrderType != payment.OrderTypeSubscription {
		return nil, infraerrors.BadRequest("WALLET_SUBSCRIPTION_ONLY", "站内余额仅可用于购买或续费订阅")
	}
	key := strings.TrimSpace(req.IdempotencyKey)
	if key == "" || len(key) > 128 {
		return nil, infraerrors.BadRequest("INVALID_IDEMPOTENCY_KEY", "余额支付需要有效的 Idempotency-Key")
	}
	for _, ch := range key {
		if ch < 33 || ch > 126 {
			return nil, infraerrors.BadRequest("INVALID_IDEMPOTENCY_KEY", "余额支付的 Idempotency-Key 格式无效")
		}
	}
	if s.entClient == nil || s.subscriptionSvc == nil {
		return nil, infraerrors.ServiceUnavailable("WALLET_PAYMENT_UNAVAILABLE", "站内余额支付暂不可用")
	}
	// 128 位摘要用无填充 URL 安全编码生成 26 字符订单号，完整摘要仅保存在内部快照。
	digest := sha256.Sum256([]byte(strconv.FormatInt(req.UserID, 10) + ":" + key))
	outTradeNo := "bal_" + base64.RawURLEncoding.EncodeToString(digest[:16])
	legacyOutTradeNo := "bal_" + hex.EncodeToString(digest[:28])
	requestHash := hex.EncodeToString(digest[:])
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin wallet payment: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	query := tx.User.Query().Where(dbuser.IDEQ(req.UserID), dbuser.DeletedAtIsNil())
	if s.entClient.Driver().Dialect() != dialect.SQLite {
		query = query.ForUpdate()
	}
	user, err := query.Only(txCtx)
	if err != nil {
		return nil, fmt.Errorf("lock wallet payment user: %w", err)
	}
	if user.Status != payment.EntityStatusActive {
		return nil, infraerrors.Forbidden("USER_INACTIVE", "user account is disabled")
	}
	// 升级后的重试同时查找旧长编号，不能把历史已扣款请求当成新订单。
	existing, err := tx.PaymentOrder.Query().Where(paymentorder.OutTradeNoIn(outTradeNo, legacyOutTradeNo)).Only(txCtx)
	if err == nil {
		if existing.UserID != req.UserID || existing.PaymentType != PaymentTypeWallet || existing.OrderType != payment.OrderTypeSubscription || existing.PlanID == nil || *existing.PlanID != req.PlanID ||
			(existing.OutTradeNo != legacyOutTradeNo && psSnapshotStringValue(existing.ProviderSnapshot["wallet_request_hash"]) != requestHash) {
			return nil, infraerrors.Conflict("IDEMPOTENCY_CONFLICT", "同一支付请求不能用于不同订阅，请重新确认购买")
		}
		// 已完成支付优先返回原结果，后续关闭开关或下架套餐不改变历史交易。
		if existing.Status != OrderStatusCompleted {
			return nil, infraerrors.Conflict("WALLET_ORDER_STATE_INVALID", "订单状态异常，请查看订单记录")
		}
		return walletOrderResponse(existing), nil
	}
	if !dbent.IsNotFound(err) {
		return nil, fmt.Errorf("lookup wallet payment: %w", err)
	}
	if !cfg.Enabled {
		return nil, infraerrors.Forbidden("PAYMENT_DISABLED", "payment system is disabled")
	}
	if !cfg.WalletPaymentEnabled {
		return nil, infraerrors.Forbidden("WALLET_PAYMENT_DISABLED", "站内余额支付未开启")
	}
	plan, err := tx.SubscriptionPlan.Get(txCtx, req.PlanID)
	if dbent.IsNotFound(err) || (err == nil && !plan.ForSale) {
		return nil, infraerrors.NotFound("PLAN_NOT_AVAILABLE", "plan not found or not for sale")
	}
	if err != nil {
		return nil, fmt.Errorf("get wallet subscription plan: %w", err)
	}
	if math.IsNaN(plan.Price) || math.IsInf(plan.Price, 0) || plan.Price <= 0 {
		return nil, infraerrors.BadRequest("INVALID_AMOUNT", "订阅价格必须为正数")
	}
	price := decimal.NewFromFloat(plan.Price)
	if !price.Equal(price.Round(2)) {
		return nil, infraerrors.BadRequest("INVALID_AMOUNT", "订阅价格最多支持两位小数")
	}
	if err := s.checkDailyLimit(txCtx, tx, req.UserID, plan.Price, cfg.DailyLimit); err != nil {
		return nil, err
	}
	// frozen_balance 已在预占时从 balance 移出；仅扣可用余额，不允许购套餐产生欠费。
	updated, err := tx.User.Update().Where(dbuser.IDEQ(user.ID), dbuser.BalanceGTE(plan.Price)).AddBalance(-plan.Price).Save(txCtx)
	if err != nil {
		return nil, fmt.Errorf("deduct wallet payment: %w", err)
	}
	if updated != 1 {
		return nil, infraerrors.BadRequest("INSUFFICIENT_BALANCE", "站内余额不足，请先充值或选择其他支付方式")
	}
	now := time.Now()
	snapshot := domain.SubscriptionPlanSnapshot{
		Name: plan.Name, Price: plan.Price, Currency: plan.Currency,
		ValidityDays:  psComputeValidityDays(plan.ValidityDays, plan.ValidityUnit),
		DailyLimitUSD: plan.DailyLimitUsd, WeeklyLimitUSD: plan.WeeklyLimitUsd, MonthlyLimitUSD: plan.MonthlyLimitUsd,
	}
	order, err := tx.PaymentOrder.Create().
		SetUserID(user.ID).SetUserEmail(user.Email).SetUserName(user.Username).
		SetNillableUserNotes(psNilIfEmpty(user.Notes)).
		SetAmount(plan.Price).SetPayAmount(plan.Price).
		SetRechargeCode(outTradeNo).SetOutTradeNo(outTradeNo).
		SetPaymentType(PaymentTypeWallet).SetPaymentTradeNo(outTradeNo).
		SetProviderKey(PaymentTypeWallet).
		SetProviderSnapshot(map[string]any{"provider_key": PaymentTypeWallet, "currency": "USD", "wallet_request_hash": requestHash}).
		SetOrderType(payment.OrderTypeSubscription).SetPlanID(plan.ID).SetPlanSnapshot(snapshot).
		SetStatus(OrderStatusCompleted).SetPaidAt(now).SetCompletedAt(now).SetExpiresAt(now).
		SetClientIP(req.ClientIP).SetSrcHost(req.SrcHost).SetNillableSrcURL(psNilIfEmpty(req.SrcURL)).Save(txCtx)
	if err != nil {
		return nil, fmt.Errorf("create wallet payment order: %w", err)
	}
	input := &AssignSubscriptionInput{
		UserID: user.ID, PlanID: plan.ID, SourceOrderID: &order.ID,
		ValidityDays: snapshot.ValidityDays, DailyLimitUSD: snapshot.DailyLimitUSD,
		WeeklyLimitUSD: snapshot.WeeklyLimitUSD, MonthlyLimitUSD: snapshot.MonthlyLimitUSD,
		UseProvidedTemplate: true, Notes: fmt.Sprintf("wallet payment order %d", order.ID),
	}
	template, err := s.subscriptionSvc.resolveGrantPlanTemplate(txCtx, input)
	if err != nil {
		return nil, err
	}
	// 已持有同一用户行锁且来源订单在本事务唯一，复用既有发放及续期排队逻辑。
	sub, queued, err := s.subscriptionSvc.assignOrExtendSubscriptionUnlocked(txCtx, input, template)
	if err != nil {
		return nil, fmt.Errorf("fulfill wallet subscription: %w", err)
	}
	detail, err := json.Marshal(map[string]any{
		"payment_type": PaymentTypeWallet, "pay_amount": plan.Price, "currency": "USD",
		"balance_before": user.Balance, "balance_after": decimal.NewFromFloat(user.Balance).Sub(price).InexactFloat64(),
		"plan_id": plan.ID, "subscription_id": sub.ID, "queued": queued,
	})
	if err != nil {
		return nil, fmt.Errorf("encode wallet payment audit: %w", err)
	}
	for _, action := range []string{"WALLET_PAYMENT_DEDUCTED", "SUBSCRIPTION_SUCCESS", "AFFILIATE_REBATE_SKIPPED"} {
		// 余额充值时已经处理过资金返利，内部购买不再次计提返利。
		if _, err := tx.PaymentAuditLog.Create().SetOrderID(strconv.FormatInt(order.ID, 10)).
			SetAction(action).SetOperator("user:" + strconv.FormatInt(user.ID, 10)).SetDetail(string(detail)).Save(txCtx); err != nil {
			return nil, fmt.Errorf("write wallet payment audit: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit wallet payment: %w", err)
	}
	s.invalidateWalletPaymentCaches(ctx, user.ID)
	s.dispatchPaymentFulfillmentNotification(order, "SUBSCRIPTION_SUCCESS")
	return walletOrderResponse(order), nil
}

// walletOrderResponse 仅返回已经原子完成的本地交易，不生成二维码或外部支付链接。
func walletOrderResponse(order *dbent.PaymentOrder) *CreateOrderResponse {
	return &CreateOrderResponse{
		OrderID: order.ID, Amount: order.Amount, PayAmount: order.PayAmount, Status: order.Status,
		PaymentType: PaymentTypeWallet, OutTradeNo: order.OutTradeNo, Currency: "USD", ExpiresAt: order.ExpiresAt,
	}
}

// invalidateWalletPaymentCaches 在事务提交后失效资金及认证缓存，失败不能撤销已完成交易。
func (s *PaymentService) invalidateWalletPaymentCaches(ctx context.Context, userID int64) {
	cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var billingCache *BillingCacheService
	if s.redeemService != nil {
		if s.redeemService.authCacheInvalidator != nil {
			s.redeemService.authCacheInvalidator.InvalidateAuthCacheByUserID(cacheCtx, userID)
		}
		billingCache = s.redeemService.billingCacheService
	}
	if billingCache == nil && s.subscriptionSvc != nil {
		billingCache = s.subscriptionSvc.billingCacheService
	}
	if billingCache != nil {
		if err := billingCache.InvalidateUserBalance(cacheCtx, userID); err != nil {
			slog.Warn("wallet payment balance cache invalidation failed", "user_id", userID, "error", err)
		}
	}
}
