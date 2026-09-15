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

// 概览展示充值、余额购买及续费的订单交易量，余额消费不重复计入余额单位购入成本。
func TestPaymentDashboardIncludesWalletPurchasesAndRenewals(t *testing.T) {
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
		{paymentType: PaymentTypeWallet, orderType: payment.OrderTypeSubscription, amount: 40, tradeNo: "wallet-stats-renewal", monthlyLimit: &monthlyLimit},
	} {
		seed.userID, seed.userEmail, seed.userName = user.ID, user.Email, user.Username
		seed.paidAt, seed.status = now, OrderStatusCompleted
		createPaidPaymentStatsOrder(t, ctx, client, seed)
	}
	// 所有交易均使用下单时的 USD 币种快照，避免历史无快照订单的 CNY 回退。
	_, err = client.PaymentOrder.Update().Where(paymentorder.UserIDEQ(user.ID)).
		SetProviderSnapshot(map[string]any{"currency": "USD"}).Save(ctx)
	require.NoError(t, err)
	svc := &PaymentService{entClient: client}
	stats, err := svc.GetDashboardStatsWithRange(ctx, dayStart, dayStart.AddDate(0, 0, 1))
	require.NoError(t, err)
	require.Equal(t, CurrencyAmounts{"USD": 180}, stats.TotalAmount)
	require.Equal(t, CurrencyAmounts{"USD": 180}, stats.TodayAmount)
	require.Equal(t, CurrencyAmounts{"USD": 60}, stats.AvgAmount)
	require.Equal(t, 3, stats.TotalCount)
	require.Equal(t, 3, stats.TodayCount)
	require.Equal(t, 1, stats.ReasoningPointPurchaseOrderCount)
	require.Equal(t, 1.0, stats.AvgReasoningPointPurchaseUnitPrice)
	require.Equal(t, []DailyStats{{Date: dayStart.Format("2006-01-02"), Amount: CurrencyAmounts{"USD": 180}, Count: 3}}, stats.DailySeries)
	require.Equal(t, []PaymentMethodStat{
		{Type: PaymentTypeWallet, Amount: CurrencyAmounts{"USD": 80}, Count: 2},
		{Type: payment.TypeStripe, Amount: CurrencyAmounts{"USD": 100}, Count: 1},
	}, stats.PaymentMethods)
	require.Len(t, stats.PurchaseDistribution, 2)
	require.Equal(t, payment.OrderTypeBalance, stats.PurchaseDistribution[0].Type)
	require.Equal(t, "USD", stats.PurchaseDistribution[0].Currency)
	require.Equal(t, payment.OrderTypeSubscription, stats.PurchaseDistribution[1].Type)
	require.Equal(t, "USD", stats.PurchaseDistribution[1].Currency)
	require.Equal(t, 80.0, stats.PurchaseDistribution[1].Amount)
	require.Equal(t, 2, stats.PurchaseDistribution[1].Count)
	require.Len(t, stats.TopUsers["USD"], 1)
	require.Equal(t, 180.0, stats.TopUsers["USD"][0].Amount)

	orders, total, err := svc.GetUserOrders(ctx, user.ID, OrderListParams{})
	require.NoError(t, err)
	require.Equal(t, 3, total)
	require.Len(t, orders, 3)
	orders, total, err = svc.AdminListOrders(ctx, user.ID, OrderListParams{})
	require.NoError(t, err)
	require.Equal(t, 3, total)
	require.Len(t, orders, 3)
	// 支付方式 balance 表示余额扣款，与订单类型 balance（充值）保持独立。
	orders, total, err = svc.AdminListOrders(ctx, user.ID, OrderListParams{PaymentType: PaymentTypeWallet, OrderType: payment.OrderTypeSubscription})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, orders, 2)
	for _, order := range orders {
		require.Equal(t, PaymentTypeWallet, order.PaymentType)
		require.Equal(t, payment.OrderTypeSubscription, order.OrderType)
	}
}

// 自定义历史范围包含余额交易，但今日卡片仍只展示当前自然日的已支付订单。
func TestPaymentDashboardWalletRangeCurrencyAndStatusBoundaries(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().SetEmail("wallet-range@example.com").
		SetPasswordHash("hash").SetUsername("wallet-range").Save(ctx)
	require.NoError(t, err)
	plan, err := client.SubscriptionPlan.Create().SetName("同一订阅套餐").SetPrice(40).Save(ctx)
	require.NoError(t, err)
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	start := today.AddDate(0, 0, -2)
	end := today.AddDate(0, 0, -1)
	seeds := []paymentStatsOrderSeed{
		{paymentType: PaymentTypeWallet, currency: "USD", status: OrderStatusCompleted, amount: 40, paidAt: start, tradeNo: "wallet-start"},
		{paymentType: PaymentTypeWallet, currency: "USD", status: OrderStatusCompleted, amount: 40, paidAt: end.Add(-time.Second), tradeNo: "wallet-before-end"},
		{paymentType: payment.TypeAlipay, currency: "CNY", status: OrderStatusCompleted, amount: 280, paidAt: start.Add(time.Hour), tradeNo: "external-same-plan"},
		{paymentType: PaymentTypeWallet, currency: "USD", status: OrderStatusCompleted, amount: 99, paidAt: start.Add(-time.Second), tradeNo: "wallet-before-start"},
		{paymentType: PaymentTypeWallet, currency: "USD", status: OrderStatusCompleted, amount: 99, paidAt: end, tradeNo: "wallet-at-end"},
		{paymentType: PaymentTypeWallet, currency: "USD", status: OrderStatusCompleted, amount: 25, paidAt: now, tradeNo: "wallet-today"},
	}
	// 无效终态和未支付订单即使留有 paid_at 也不能进入已支付统计。
	for _, status := range []string{OrderStatusPending, OrderStatusProcessing, OrderStatusFailed, OrderStatusCancelled, OrderStatusExpired, OrderStatusRefunded} {
		for _, at := range []time.Time{start, now} {
			seeds = append(seeds, paymentStatsOrderSeed{
				paymentType: PaymentTypeWallet, currency: "USD", status: status, amount: 999,
				paidAt: at, tradeNo: "wallet-excluded-" + status + "-" + at.Format("20060102"),
			})
		}
	}
	for _, seed := range seeds {
		seed.userID, seed.userEmail, seed.userName = user.ID, user.Email, user.Username
		seed.orderType, seed.planID = payment.OrderTypeSubscription, &plan.ID
		createPaidPaymentStatsOrder(t, ctx, client, seed)
	}
	svc := &PaymentService{entClient: client}
	stats, err := svc.GetDashboardStatsWithRange(ctx, start, end)
	require.NoError(t, err)
	require.Equal(t, 3, stats.TotalCount)
	require.Equal(t, CurrencyAmounts{"USD": 80, "CNY": 280}, stats.TotalAmount)
	require.Equal(t, CurrencyAmounts{"USD": 40, "CNY": 280}, stats.AvgAmount)
	require.Equal(t, 1, stats.TodayCount)
	require.Equal(t, CurrencyAmounts{"USD": 25}, stats.TodayAmount)
	require.Equal(t, []DailyStats{{Date: start.Format("2006-01-02"), Amount: CurrencyAmounts{"USD": 80, "CNY": 280}, Count: 3}}, stats.DailySeries)
	require.Equal(t, []PaymentMethodStat{
		{Type: payment.TypeAlipay, Amount: CurrencyAmounts{"CNY": 280}, Count: 1},
		{Type: PaymentTypeWallet, Amount: CurrencyAmounts{"USD": 80}, Count: 2},
	}, stats.PaymentMethods)
	require.Equal(t, []PurchaseDistributionStat{
		{Type: payment.OrderTypeSubscription, Label: plan.Name, PlanID: &plan.ID, Currency: "CNY", Amount: 280, Count: 1},
		{Type: payment.OrderTypeSubscription, Label: plan.Name, PlanID: &plan.ID, Currency: "USD", Amount: 80, Count: 2},
	}, stats.PurchaseDistribution)
	require.Equal(t, TopUsersByCurrency{
		"CNY": {{UserID: user.ID, Email: user.Email, Amount: 280}},
		"USD": {{UserID: user.ID, Email: user.Email, Amount: 80}},
	}, stats.TopUsers)

	// 只有余额支付的范围也应正常显示，无外部余额购入时成本指标保持为零。
	stats, err = svc.GetDashboardStatsWithRange(ctx, today, today.AddDate(0, 0, 1))
	require.NoError(t, err)
	require.Equal(t, 1, stats.TotalCount)
	require.Equal(t, CurrencyAmounts{"USD": 25}, stats.TotalAmount)
	require.Equal(t, 0, stats.ReasoningPointPurchaseOrderCount)
	require.Zero(t, stats.AvgReasoningPointPurchaseUnitPrice)
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
