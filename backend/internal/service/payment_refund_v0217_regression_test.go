//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/paymentauditlog"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// 协议异常不能证明退款失败，只有明确 failed 才能归还已保留的权益。
func TestPendingRefundProtocolErrorsKeepHeldDeduction(t *testing.T) {
	for _, response := range []*payment.RefundResponse{nil, {Status: "unknown"}, {Status: ""}} {
		t.Run(fmt.Sprintf("response_%v", response), func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderWithDetailForTest(t, ctx, client, "bad-response", `{"refundID":"rf_test","deductionHeld":true,"deductionType":"balance","balanceDeducted":60}`)
			wallet := &refundHoldWallet{balance: 40}
			provider := &refundQueryProviderTestDouble{refundResponse: response}
			restore := replacePaymentProviderFactoryForTest(t, provider)
			defer restore()
			svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}, userRepo: wallet.repo(order.UserID)}
			result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.Error(t, err)
			require.Nil(t, result)
			require.Zero(t, wallet.restoreCalls)
			require.Zero(t, wallet.deductCalls)
			stored, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusRefundPending, stored.Status)
		})
	}
}

// 使用真实事务内的数据库余额写入，注入审计失败，验证归还与订单状态一并回滚。
func TestHeldRefundReturnRollsBackWhenAuditFails(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderWithDetailForTest(t, ctx, client, "atomic-return-audit", `{"refundID":"rf_test","deductionHeld":true,"deductionType":"balance","balanceDeducted":60}`)
	_, err := client.User.UpdateOneID(order.UserID).SetBalance(40).Save(ctx)
	require.NoError(t, err)
	repo := &mockUserRepo{addBalanceFn: func(ctx context.Context, id int64, amount float64) error {
		tx := dbent.TxFromContext(ctx)
		require.NotNil(t, tx)
		return tx.User.UpdateOneID(id).AddBalance(amount).Exec(ctx)
	}}
	client.PaymentAuditLog.Use(func(next dbent.Mutator) dbent.Mutator {
		return dbent.MutateFunc(func(ctx context.Context, mutation dbent.Mutation) (dbent.Value, error) {
			if audit, ok := mutation.(*dbent.PaymentAuditLogMutation); ok {
				if action, exists := audit.Action(); exists && action == "REFUND_FAILED" {
					return nil, errors.New("injected refund audit failure")
				}
			}
			return next.Mutate(ctx, mutation)
		})
	})
	svc := &PaymentService{entClient: client, userRepo: repo}
	detail, err := svc.latestRefundPendingDetail(ctx, order.ID)
	require.NoError(t, err)
	result, err := svc.finalizeRefundFailed(ctx, svc.refundFinalizePlan(order, detail), errors.New("gateway failed"))
	require.ErrorContains(t, err, "injected refund audit failure")
	require.Nil(t, result)
	user, err := client.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Equal(t, 40.0, user.Balance)
	stored, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, stored.Status)
}

// 多个终态查询竞争同一条订单时，只允许一次归还或一次成功结算。
func TestPendingRefundConcurrentFinalizationOnlyOnce(t *testing.T) {
	for _, succeed := range []bool{false, true} {
		t.Run(fmt.Sprintf("success_%t", succeed), func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderWithDetailForTest(t, ctx, client, "concurrent-terminal", `{"refundID":"rf_test","deductionHeld":true,"deductionType":"balance","balanceDeducted":60}`)
			_, err := client.User.UpdateOneID(order.UserID).SetBalance(40).Save(ctx)
			require.NoError(t, err)
			var restores, successful atomic.Int32
			svc := &PaymentService{entClient: client, userRepo: &mockUserRepo{addBalanceFn: func(ctx context.Context, id int64, amount float64) error {
				tx := dbent.TxFromContext(ctx)
				if tx == nil {
					return errors.New("missing transaction")
				}
				if err := tx.User.UpdateOneID(id).AddBalance(amount).Exec(ctx); err != nil {
					return err
				}
				restores.Add(1)
				return nil
			}}}
			detail, err := svc.latestRefundPendingDetail(ctx, order.ID)
			require.NoError(t, err)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < 64; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					plan := svc.refundFinalizePlan(order, detail)
					var err error
					if succeed {
						_, err = svc.finalizePendingRefundSuccess(ctx, plan)
					} else {
						_, err = svc.finalizeRefundFailed(ctx, plan, errors.New("gateway failed"))
					}
					if err == nil {
						successful.Add(1)
					}
				}()
			}
			close(start)
			wg.Wait()
			require.EqualValues(t, 1, successful.Load())
			user, err := client.User.Get(ctx, order.UserID)
			require.NoError(t, err)
			action, wantBalance, wantRestores := "REFUND_FAILED", 100.0, int32(1)
			if succeed {
				action, wantBalance, wantRestores = "REFUND_SUCCESS", 40.0, 0
			}
			require.Equal(t, wantBalance, user.Balance)
			require.Equal(t, wantRestores, restores.Load())
			count, err := client.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ(action)).Count(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, count)
		})
	}
}

// 准备退款之后的并发消费不能把普通退款变为少扣权益的强制退款。
func TestRefundNonForceRejectsConcurrentBalanceShortfallBeforeGateway(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createCompletedRefundableOrderForTest(t, ctx, client, "non-force-race")
	wallet := &refundHoldWallet{balance: 100}
	provider := &pendingRefundProviderTestDouble{}
	restore := replacePaymentProviderFactoryForTest(t, provider)
	defer restore()
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}, userRepo: wallet.repo(order.UserID)}
	plan, early, err := svc.PrepareRefund(ctx, order.ID, 100, "refund", false, true)
	require.NoError(t, err)
	require.Nil(t, early)
	wallet.balance = 20
	result, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.True(t, result.RequireForce)
	require.False(t, result.Success)
	require.Equal(t, 20.0, wallet.balance)
	require.Zero(t, wallet.deductCalls)
	require.Zero(t, wallet.restoreCalls)
	require.Zero(t, provider.refundCalls)
	stored, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, stored.Status)
}

// 第一次审计检查之后、认领订单之前写入的回滚失败标记也必须阻止渠道重提。
func TestRefundRechecksRollbackFailureAfterClaim(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createCompletedRefundableOrderForTest(t, ctx, client, "claim-audit-race")
	wallet := &refundHoldWallet{balance: 100}
	provider := &pendingRefundProviderTestDouble{}
	restore := replacePaymentProviderFactoryForTest(t, provider)
	defer restore()
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}, userRepo: wallet.repo(order.UserID)}
	plan, early, err := svc.PrepareRefund(ctx, order.ID, 100, "refund", true, true)
	require.NoError(t, err)
	require.Nil(t, early)
	var once sync.Once
	client.PaymentOrder.Use(func(next dbent.Mutator) dbent.Mutator {
		return dbent.MutateFunc(func(ctx context.Context, mutation dbent.Mutation) (dbent.Value, error) {
			if m, ok := mutation.(*dbent.PaymentOrderMutation); ok {
				if status, exists := m.Status(); exists && status == OrderStatusRefunding {
					once.Do(func() {
						require.NoError(t, createPaymentAuditLogTx(ctx, client, order.ID, "REFUND_ROLLBACK_FAILED", map[string]any{"balanceDeducted": 100}))
					})
				}
			}
			return next.Mutate(ctx, mutation)
		})
	})
	result, err := svc.ExecuteRefund(ctx, plan)
	require.Nil(t, result)
	require.Equal(t, "REFUND_MANUAL_REVIEW_REQUIRED", infraerrors.Reason(err))
	require.Zero(t, wallet.deductCalls)
	require.Zero(t, provider.refundCalls)
	require.Equal(t, 100.0, plan.BalanceToDeduct, "旧快照不能被清零")
}

// 失败退款只是归还已扣余额，不能再增加历史充值总额。
func TestRefundReturnDoesNotCountAsRecharge(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending_%t", pending), func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderWithDetailForTest(t, ctx, client, "return-no-recharge", `{"deductionHeld":true,"deductionType":"balance","balanceDeducted":60}`)
			_, err := client.User.UpdateOneID(order.UserID).SetBalance(40).SetTotalRecharged(100).Save(ctx)
			require.NoError(t, err)
			repo := &mockUserRepo{
				updateBalanceFn: func(context.Context, int64, float64) error { return errors.New("refund must not call recharge update") },
				addBalanceFn: func(ctx context.Context, id int64, amount float64) error {
					db := client
					if tx := dbent.TxFromContext(ctx); tx != nil {
						db = tx.Client()
					}
					return db.User.UpdateOneID(id).AddBalance(amount).Exec(ctx)
				},
			}
			svc := &PaymentService{entClient: client, userRepo: repo}
			detail, err := svc.latestRefundPendingDetail(ctx, order.ID)
			require.NoError(t, err)
			plan := svc.refundFinalizePlan(order, detail)
			if pending {
				result, err := svc.finalizeRefundFailed(ctx, plan, errors.New("channel rejected"))
				require.NoError(t, err)
				require.NotContains(t, result.Warning, "rollback failed")
			} else {
				require.True(t, svc.RollbackRefund(ctx, plan, errors.New("channel rejected")))
			}
			stored, err := client.User.Get(ctx, order.UserID)
			require.NoError(t, err)
			require.Equal(t, 100.0, stored.Balance)
			require.Equal(t, 100.0, stored.TotalRecharged)
		})
	}
}
