//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/ent/paymentorder"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// 充值后再用余额购买订阅不会重复统计外部收入，但两笔订单都能查询。
func TestPaymentDashboardExcludesWalletTransfersAndKeepsOrders(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().SetEmail("wallet-stats@example.com").
		SetPasswordHash("hash").SetUsername("wallet-stats").Save(ctx)
	require.NoError(t, err)
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	monthlyLimit := 200.0
	for _, seed := range []paymentStatsOrderSeed{
		{paymentType: payment.TypeStripe, orderType: payment.OrderTypeBalance, amount: 100, tradeNo: "wallet-stats-recharge"},
		{paymentType: PaymentTypeWallet, orderType: payment.OrderTypeSubscription, amount: 40, tradeNo: "wallet-stats-subscription", monthlyLimit: &monthlyLimit},
	} {
		seed.userID, seed.userEmail, seed.userName = user.ID, user.Email, user.Username
		seed.paidAt, seed.status = now, OrderStatusCompleted
		createPaidPaymentStatsOrder(t, ctx, client, seed)
	}
	// 两笔交易均使用下单时的 USD 币种快照，避免历史无快照订单的 CNY 回退。
	_, err = client.PaymentOrder.Update().Where(paymentorder.UserIDEQ(user.ID)).
		SetProviderSnapshot(map[string]any{"currency": "USD"}).Save(ctx)
	require.NoError(t, err)
	svc := &PaymentService{entClient: client}
	stats, err := svc.GetDashboardStatsWithRange(ctx, dayStart, dayStart.AddDate(0, 0, 1))
	require.NoError(t, err)
	require.Equal(t, CurrencyAmounts{"USD": 100}, stats.TotalAmount)
	require.Equal(t, CurrencyAmounts{"USD": 100}, stats.TodayAmount)
	require.Equal(t, 1, stats.TotalCount)
	require.Equal(t, 1, stats.TodayCount)
	require.Equal(t, 1, stats.ReasoningPointPurchaseOrderCount)
	require.Equal(t, 1.0, stats.AvgReasoningPointPurchaseUnitPrice)
	require.Equal(t, []DailyStats{{Date: dayStart.Format("2006-01-02"), Amount: CurrencyAmounts{"USD": 100}, Count: 1}}, stats.DailySeries)
	require.Equal(t, []PaymentMethodStat{{Type: payment.TypeStripe, Amount: CurrencyAmounts{"USD": 100}, Count: 1}}, stats.PaymentMethods)
	require.Len(t, stats.PurchaseDistribution, 1)
	require.Equal(t, payment.OrderTypeBalance, stats.PurchaseDistribution[0].Type)
	require.Len(t, stats.TopUsers["USD"], 1)
	require.Equal(t, 100.0, stats.TopUsers["USD"][0].Amount)

	orders, total, err := svc.GetUserOrders(ctx, user.ID, OrderListParams{})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, orders, 2)
	orders, total, err = svc.AdminListOrders(ctx, user.ID, OrderListParams{})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, orders, 2)
}

// 钱包交易明确返回不支持退款，不能因缺少外部渠道实例而返回服务器故障。
func TestPrepareRefundRejectsWalletPaymentWithoutProviderLookup(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().SetEmail("wallet-refund@example.com").
		SetPasswordHash("hash").SetUsername("wallet-refund").Save(ctx)
	require.NoError(t, err)
	createPaidPaymentStatsOrder(t, ctx, client, paymentStatsOrderSeed{
		userID: user.ID, userEmail: user.Email, userName: user.Username,
		paymentType: PaymentTypeWallet, orderType: payment.OrderTypeSubscription,
		status: OrderStatusCompleted, amount: 40, tradeNo: "wallet-refund", paidAt: time.Now(),
	})
	order, err := client.PaymentOrder.Query().Where(paymentorder.OutTradeNoEQ("wallet-refund")).Only(ctx)
	require.NoError(t, err)
	order, err = order.Update().SetProviderKey(PaymentTypeWallet).
		SetProviderSnapshot(map[string]any{"provider_key": PaymentTypeWallet, "currency": "USD"}).Save(ctx)
	require.NoError(t, err)
	svc := &PaymentService{entClient: client}
	plan, result, err := svc.PrepareRefund(ctx, order.ID, 40, "退款测试", true, true)
	require.Error(t, err)
	require.Equal(t, "WALLET_REFUND_UNSUPPORTED", infraerrors.Reason(err))
	require.Nil(t, plan)
	require.Nil(t, result)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
}
