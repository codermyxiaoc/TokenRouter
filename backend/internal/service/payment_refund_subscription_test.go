//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/paymentauditlog"
	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
	"github.com/TokenFlux/TokenRouter/ent/usersubscription"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

// refundSubscriptionRepo 使用真实 Ent 数据与调用方事务，验证暂停/恢复和套餐链不会脱离订单事务。
type refundSubscriptionRepo struct {
	UserSubscriptionRepository
	client *dbent.Client
}

// 真实提交后才触发缓存失效；从回调重新读取必须可见已提交的订阅状态。
type refundSubscriptionCacheProbe struct {
	APIKeyAuthCacheInvalidator
	read func(context.Context, int64)
}

func (p refundSubscriptionCacheProbe) InvalidateAuthCacheByUserID(ctx context.Context, id int64) {
	p.read(ctx, id)
}

func TestRefundSubscriptionCacheInvalidationFollowsCommit(t *testing.T) {
	ctx := context.Background()
	svc, plan, original, _ := newRefundSubscriptionFixture(t)
	var seen []string
	svc.redeemService = &RedeemService{authCacheInvalidator: refundSubscriptionCacheProbe{read: func(ctx context.Context, id int64) {
		require.Equal(t, plan.Order.UserID, id)
		require.Nil(t, dbent.TxFromContext(ctx))
		stored, err := svc.entClient.UserSubscription.Get(ctx, original.ID)
		require.NoError(t, err)
		seen = append(seen, stored.Status)
	}}}
	restore := replacePaymentProviderFactoryForTest(t, &pendingRefundProviderTestDouble{queryStatus: payment.ProviderStatusFailed})
	defer restore()
	_, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.NotEmpty(t, seen)
	require.Equal(t, SubscriptionStatusSuspended, seen[len(seen)-1])
	_, err = svc.QueryAndFinalizeRefund(ctx, plan.OrderID)
	require.NoError(t, err)
	require.Equal(t, SubscriptionStatusActive, seen[len(seen)-1])
}

func TestRefundSubscriptionCrashRecoveryNeverResendsOrReturnsUnknownRefund(t *testing.T) {
	ctx := context.Background()
	svc, plan, original, _ := newRefundSubscriptionFixture(t)
	_, err := svc.entClient.PaymentOrder.UpdateOneID(plan.OrderID).SetStatus(OrderStatusRefunding).SetUpdatedAt(time.Now().Add(-16 * time.Minute)).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, svc.holdRefundSubscription(ctx, plan))
	// 模拟暂停提交后进程退出，内存中的计划丢失；新服务只能读持久化审计。
	svc = &PaymentService{entClient: svc.entClient, loadBalancer: &captureLoadBalancer{}}
	provider := &pendingRefundProviderTestDouble{queryStatus: payment.ProviderStatusSuccess}
	restore := replacePaymentProviderFactoryForTest(t, provider)
	defer restore()
	_, err = svc.QueryAndFinalizeRefund(ctx, plan.OrderID)
	require.Equal(t, "REFUND_QUERY_UNSUPPORTED", infraerrors.Reason(err))
	require.Zero(t, provider.refundCalls)
	stored, err := svc.entClient.PaymentOrder.Get(ctx, plan.OrderID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, stored.Status)
	detail, err := svc.latestRefundPendingDetail(ctx, plan.OrderID)
	require.NoError(t, err)
	require.True(t, detail.SubmissionUncertain)
	require.NotNil(t, detail.SubscriptionHold)
	require.Equal(t, plan.SubDaysToDeduct, detail.SubDaysDeducted)
	held, err := svc.entClient.UserSubscription.Get(ctx, original.ID)
	require.NoError(t, err)
	require.Equal(t, SubscriptionStatusSuspended, held.Status)
	require.Nil(t, held.DeletedAt)
}

func TestRefundSubscriptionCrashRecoveryRejectsEarlierAttemptSnapshot(t *testing.T) {
	ctx := context.Background()
	svc, plan, _, _ := newRefundSubscriptionFixture(t)
	require.NoError(t, svc.holdRefundSubscription(ctx, plan))
	// 旧尝试审计时间在新一次 REFUNDING 认领时间之前，不能用来接管新尝试。
	entry, err := svc.entClient.PaymentAuditLog.Query().Where(paymentauditlog.ActionEQ("REFUND_SUBSCRIPTION_HELD")).Only(ctx)
	require.NoError(t, err)
	require.NoError(t, svc.entClient.PaymentAuditLog.DeleteOneID(entry.ID).Exec(ctx))
	_, err = svc.entClient.PaymentAuditLog.Create().SetOrderID(entry.OrderID).SetAction(entry.Action).SetOperator(entry.Operator).
		SetDetail(entry.Detail).SetCreatedAt(time.Now().Add(-time.Hour)).Save(ctx)
	require.NoError(t, err)
	order, err := svc.entClient.PaymentOrder.UpdateOneID(plan.OrderID).SetStatus(OrderStatusRefunding).SetUpdatedAt(time.Now().Add(-16 * time.Minute)).Save(ctx)
	require.NoError(t, err)
	_, err = svc.recoverInterruptedSubscriptionRefund(ctx, order)
	require.Error(t, err)
	stored, err := svc.entClient.PaymentOrder.Get(ctx, plan.OrderID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunding, stored.Status)
}

func (r *refundSubscriptionRepo) db(ctx context.Context) *dbent.Client {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return r.client
}

func refundSubscriptionEntity(row *dbent.UserSubscription) *UserSubscription {
	return &UserSubscription{ID: row.ID, UserID: row.UserID, PlanID: row.PlanID, StartsAt: row.StartsAt, ExpiresAt: row.ExpiresAt, Status: row.Status, SourceOrderID: row.SourceOrderID, DailyUsageUSD: row.DailyUsageUsd, UpdatedAt: row.UpdatedAt, DeletedAt: row.DeletedAt}
}

func (r *refundSubscriptionRepo) GetByID(ctx context.Context, id int64) (*UserSubscription, error) {
	row, err := r.db(ctx).UserSubscription.Query().Where(usersubscription.IDEQ(id), usersubscription.DeletedAtIsNil()).Only(ctx)
	if err != nil {
		return nil, err
	}
	return refundSubscriptionEntity(row), nil
}

func (r *refundSubscriptionRepo) ListByUserIDAndPlanID(ctx context.Context, userID, planID int64) ([]UserSubscription, error) {
	rows, err := r.db(ctx).UserSubscription.Query().Where(usersubscription.UserIDEQ(userID), usersubscription.PlanIDEQ(planID), usersubscription.DeletedAtIsNil()).All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]UserSubscription, 0, len(rows))
	for _, row := range rows {
		result = append(result, *refundSubscriptionEntity(row))
	}
	return result, nil
}

func (r *refundSubscriptionRepo) Delete(ctx context.Context, id int64) error {
	return r.db(ctx).UserSubscription.UpdateOneID(id).SetDeletedAt(time.Now()).Exec(ctx)
}

func (r *refundSubscriptionRepo) Update(ctx context.Context, sub *UserSubscription) error {
	return r.db(ctx).UserSubscription.UpdateOneID(sub.ID).SetStartsAt(sub.StartsAt).SetExpiresAt(sub.ExpiresAt).SetStatus(sub.Status).Exec(ctx)
}

func newRefundSubscriptionFixture(t *testing.T) (*PaymentService, *RefundPlan, *dbent.UserSubscription, *dbent.UserSubscription) {
	t.Helper()
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createCompletedRefundableOrderForTest(t, ctx, client, "subscription-hold")
	planRow, err := client.SubscriptionPlan.Create().SetName("refund plan").SetPrice(100).Save(ctx)
	require.NoError(t, err)
	order, err = client.PaymentOrder.UpdateOneID(order.ID).SetOrderType(payment.OrderTypeSubscription).SetPlanID(planRow.ID).Save(ctx)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Microsecond)
	sub, err := client.UserSubscription.Create().SetUserID(order.UserID).SetPlanID(planRow.ID).SetSourceOrderID(order.ID).
		SetStartsAt(now.Add(-time.Hour)).SetExpiresAt(now.Add(10 * 24 * time.Hour)).SetDailyUsageUsd(8).SetStatus(SubscriptionStatusActive).Save(ctx)
	require.NoError(t, err)
	next, err := client.UserSubscription.Create().SetUserID(order.UserID).SetPlanID(planRow.ID).
		SetStartsAt(sub.ExpiresAt).SetExpiresAt(sub.ExpiresAt.Add(30 * 24 * time.Hour)).SetStatus(SubscriptionStatusPending).Save(ctx)
	require.NoError(t, err)
	repo := &refundSubscriptionRepo{client: client}
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}, subscriptionSvc: NewSubscriptionService(groupRepoNoop{}, repo, nil, client, nil)}
	plan := &RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 100, GatewayAmount: 100, DeductBalance: true, DeductionType: payment.DeductionTypeSubscription, SubscriptionID: sub.ID, SubDaysToDeduct: 30}
	return svc, plan, sub, next
}

func TestRefundSubscriptionPendingHoldsThenRestoresOrRevokesExactlyOnce(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(fmt.Sprintf("success_%t", success), func(t *testing.T) {
			ctx := context.Background()
			svc, plan, original, next := newRefundSubscriptionFixture(t)
			status := payment.ProviderStatusFailed
			if success {
				status = payment.ProviderStatusSuccess
			}
			restore := replacePaymentProviderFactoryForTest(t, &pendingRefundProviderTestDouble{queryStatus: status})
			defer restore()
			result, err := svc.ExecuteRefund(ctx, plan)
			require.NoError(t, err)
			require.False(t, result.Success)
			held, err := svc.entClient.UserSubscription.Get(ctx, original.ID)
			require.NoError(t, err)
			require.Equal(t, SubscriptionStatusSuspended, held.Status)
			require.Nil(t, held.DeletedAt)
			require.True(t, held.ExpiresAt.Equal(original.ExpiresAt))
			require.Equal(t, original.DailyUsageUsd, held.DailyUsageUsd)
			require.False(t, refundSubscriptionEntity(held).IsActive())
			pendingNext, err := svc.entClient.UserSubscription.Get(ctx, next.ID)
			require.NoError(t, err)
			require.True(t, next.StartsAt.Equal(pendingNext.StartsAt), "渠道未确认前套餐链不应提前推进")
			result, err = svc.QueryAndFinalizeRefund(ctx, plan.OrderID)
			require.NoError(t, err)
			require.Equal(t, success, result.Success)
			// 成功退款会软删除原订阅；显式含软删除读取才能核对撤销和后续套餐推进。
			settled, err := svc.entClient.UserSubscription.Get(mixins.SkipSoftDelete(ctx), original.ID)
			require.NoError(t, err)
			settledNext, err := svc.entClient.UserSubscription.Get(ctx, next.ID)
			require.NoError(t, err)
			if success {
				require.NotNil(t, settled.DeletedAt)
				require.True(t, settledNext.StartsAt.Before(next.StartsAt))
			} else {
				require.Nil(t, settled.DeletedAt)
				require.Equal(t, original.Status, settled.Status)
				require.True(t, original.ExpiresAt.Equal(settled.ExpiresAt))
				require.Equal(t, original.DailyUsageUsd, settled.DailyUsageUsd)
				require.True(t, next.StartsAt.Equal(settledNext.StartsAt))
			}
			_, err = svc.QueryAndFinalizeRefund(ctx, plan.OrderID)
			require.Error(t, err)
			again, err := svc.entClient.UserSubscription.Get(ctx, next.ID)
			require.NoError(t, err)
			require.True(t, again.StartsAt.Equal(settledNext.StartsAt))
		})
	}
}

func TestRefundSubscriptionDoesNotRestoreChangedOrForeignHold(t *testing.T) {
	for _, change := range []string{"administrator", "foreign_order"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			svc, plan, original, _ := newRefundSubscriptionFixture(t)
			restore := replacePaymentProviderFactoryForTest(t, &pendingRefundProviderTestDouble{queryStatus: payment.ProviderStatusFailed})
			defer restore()
			_, err := svc.ExecuteRefund(ctx, plan)
			require.NoError(t, err)
			if change == "administrator" {
				_, err = svc.entClient.UserSubscription.UpdateOneID(original.ID).SetStatus(SubscriptionStatusExpired).Save(ctx)
			} else {
				_, err = svc.entClient.UserSubscription.UpdateOneID(original.ID).SetSourceOrderID(plan.OrderID + 999).Save(ctx)
			}
			require.NoError(t, err)
			result, err := svc.QueryAndFinalizeRefund(ctx, plan.OrderID)
			require.NoError(t, err)
			require.Contains(t, result.Warning, "rollback failed")
			stored, err := svc.entClient.UserSubscription.Get(ctx, original.ID)
			require.NoError(t, err)
			require.NotEqual(t, SubscriptionStatusActive, stored.Status)
			require.Equal(t, 1, countRefundAuditsForTest(t, ctx, svc.entClient, plan.OrderID, "REFUND_ROLLBACK_FAILED"))
		})
	}
}

// 先结清 active 阶段的重置次数，恢复时只推进 suspended 阶段水位。
func TestRefundSubscriptionHoldSettlesResetCountsAcrossStatusChanges(t *testing.T) {
	ctx := context.Background()
	svc, plan, original, _ := newRefundSubscriptionFixture(t)
	midnight := timezone.StartOfDay(time.Now())
	_, err := svc.entClient.UserSubscription.UpdateOneID(original.ID).
		SetStartsAt(midnight.AddDate(0, 0, -3)).SetExpiresAt(midnight.AddDate(0, 0, 10)).
		SetDailyLimitUsd(100).SetDailyWindowStart(midnight.AddDate(0, 0, -3)).
		SetDailyResetCount(7).SetResetCountedAt(midnight.Add(-12 * time.Hour)).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, svc.holdRefundSubscription(ctx, plan))
	held, err := svc.entClient.UserSubscription.Get(ctx, original.ID)
	require.NoError(t, err)
	require.EqualValues(t, 8, held.DailyResetCount)
	require.Equal(t, original.DailyUsageUsd, held.DailyUsageUsd)
	// 模拟暂停跨越一个统计边界且后台尚未推进水位，保留原业务版本。
	_, err = svc.entClient.UserSubscription.UpdateOneID(original.ID).SetResetCountedAt(midnight.Add(-12 * time.Hour)).SetUpdatedAt(plan.subscriptionHold.HeldAt).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, svc.rollbackHeldRefundSubscription(ctx, plan))
	restored, err := svc.entClient.UserSubscription.Get(ctx, original.ID)
	require.NoError(t, err)
	require.EqualValues(t, 8, restored.DailyResetCount)
	require.True(t, restored.ResetCountedAt.After(midnight))
	require.Equal(t, original.DailyUsageUsd, restored.DailyUsageUsd)
}

// 渠道长期 pending 后部分扣天的订阅已过期，必须转人工而不是从当前时间重新续期。
func TestRefundExpiredPartialSubscriptionReturnRequiresManualReview(t *testing.T) {
	ctx := context.Background()
	svc, plan, original, next := newRefundSubscriptionFixture(t)
	expiredAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	_, err := svc.entClient.UserSubscription.UpdateOneID(original.ID).SetStartsAt(expiredAt.AddDate(0, 0, -2)).SetExpiresAt(expiredAt).Save(ctx)
	require.NoError(t, err)
	_, err = svc.entClient.PaymentOrder.UpdateOneID(plan.OrderID).SetStatus(OrderStatusRefundPending).Save(ctx)
	require.NoError(t, err)
	plan.SubDaysToDeduct = 5
	plan.deductionHeld = true
	result, err := svc.finalizeRefundFailed(ctx, plan, errors.New("channel rejected refund"))
	require.NoError(t, err)
	require.Contains(t, result.Warning, "rollback failed")
	stored, err := svc.entClient.UserSubscription.Get(ctx, original.ID)
	require.NoError(t, err)
	require.True(t, stored.ExpiresAt.Equal(expiredAt))
	require.Equal(t, original.DailyUsageUsd, stored.DailyUsageUsd)
	nextStored, err := svc.entClient.UserSubscription.Get(ctx, next.ID)
	require.NoError(t, err)
	require.True(t, nextStored.StartsAt.Equal(next.StartsAt))
	require.Equal(t, 1, countRefundAuditsForTest(t, ctx, svc.entClient, plan.OrderID, "REFUND_ROLLBACK_FAILED"))
	require.Equal(t, "REFUND_MANUAL_REVIEW_REQUIRED", infraerrors.Reason(svc.validateRefundRetry(ctx, plan.OrderID)))
}

// 支付夹具必须保留时间版本的回读比较语义，同时仍拒绝旧版本覆盖。
func TestPaymentSubscriptionTimestampVersionRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, _, original, _ := newRefundSubscriptionFixture(t)
	read, err := svc.entClient.UserSubscription.Get(ctx, original.ID)
	require.NoError(t, err)
	changed, err := svc.entClient.UserSubscription.Update().Where(usersubscription.IDEQ(read.ID), usersubscription.UpdatedAtEQ(read.UpdatedAt)).
		SetNotes("version advanced").SetUpdatedAt(read.UpdatedAt.Add(time.Microsecond)).Save(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, changed)
	stale, err := svc.entClient.UserSubscription.Update().Where(usersubscription.IDEQ(read.ID), usersubscription.UpdatedAtEQ(read.UpdatedAt)).
		SetNotes("stale overwrite").Save(ctx)
	require.NoError(t, err)
	require.Zero(t, stale)
}
