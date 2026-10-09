//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/payment"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// 使用本地合成输入检查订单类型白名单，不调用支付供应商或创建真实订单。
func TestAdversarialPaymentInputRejectsUnknownOrderTypes(t *testing.T) {
	for _, orderType := range []string{"unknown", "BALANCE", "balance ", "subscription_typo"} {
		t.Run(orderType, func(t *testing.T) {
			_, err := (&PaymentService{}).validateOrderInput(context.Background(),
				CreateOrderRequest{OrderType: orderType, Amount: 10},
				&PaymentConfig{Enabled: true, BalanceDisabled: true, BalanceRechargeMultiplier: 0.14})
			require.Error(t, err, "未知订单类型应在支付调用与落库前拒绝")
			_, err = (&PaymentService{}).CreateOrder(context.Background(), CreateOrderRequest{OrderType: orderType, Amount: 10})
			require.Equal(t, "INVALID_ORDER_TYPE", infraerrors.Reason(err), "创建入口不应读取配置或触发支付")
			_, _, _, err = calculateCreateOrderPayAmountForOrderType(10, payment.FeeConfig{}, "CNY", orderType, 7)
			require.Equal(t, "INVALID_ORDER_TYPE", infraerrors.Reason(err), "报价不能将未知类型按余额处理")
		})
	}
}

// 省略类型仍按余额充值处理，必须经过余额充值开关，不能绕到订阅或钱包分支。
func TestPaymentOrderEmptyTypeDefaultsToBalance(t *testing.T) {
	svc := &PaymentService{configService: &PaymentConfigService{settingRepo: &paymentConfigSettingRepoStub{values: map[string]string{
		SettingPaymentEnabled: "true", SettingBalancePayDisabled: "true",
	}}}}
	_, err := svc.CreateOrder(context.Background(), CreateOrderRequest{Amount: 10, PaymentType: payment.TypeAlipay})
	require.Equal(t, "BALANCE_PAYMENT_DISABLED", infraerrors.Reason(err))
}

// 历史异常订单与跨类型直接调用均不能获得履约租约或修改余额、订阅。
func TestAdversarialPaymentFulfillmentRejectsWrongOrderTypes(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentService{entClient: client}
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusPaid, time.Now())
	for _, orderType := range []string{"", "unknown", "BALANCE", "balance ", "subscription_typo"} {
		_, err := client.PaymentOrder.UpdateOneID(order.ID).SetOrderType(orderType).Save(ctx)
		require.NoError(t, err)
		for _, fulfill := range []func(context.Context, int64) error{svc.executeFulfillment, svc.ExecuteBalanceFulfillment, svc.ExecuteSubscriptionFulfillment} {
			require.Equal(t, "INVALID_ORDER_TYPE", infraerrors.Reason(fulfill(ctx, order.ID)))
		}
		stored, err := client.PaymentOrder.Get(ctx, order.ID)
		require.NoError(t, err)
		require.Equal(t, OrderStatusPaid, stored.Status)
	}
	_, err := client.PaymentOrder.UpdateOneID(order.ID).SetOrderType(payment.OrderTypeSubscription).Save(ctx)
	require.NoError(t, err)
	require.Equal(t, "INVALID_ORDER_TYPE", infraerrors.Reason(svc.ExecuteBalanceFulfillment(ctx, order.ID)))
	_, err = client.PaymentOrder.UpdateOneID(order.ID).SetOrderType(payment.OrderTypeBalance).Save(ctx)
	require.NoError(t, err)
	require.Equal(t, "INVALID_ORDER_TYPE", infraerrors.Reason(svc.ExecuteSubscriptionFulfillment(ctx, order.ID)))
}
