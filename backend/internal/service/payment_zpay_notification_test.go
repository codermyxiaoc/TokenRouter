//go:build unit

package service

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/paymentauditlog"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	"github.com/TokenFlux/TokenRouter/internal/payment/provider"
	"github.com/stretchr/testify/require"
)

const (
	zpayNotificationTestPID = "zpay-test-merchant"
	zpayNotificationTestKey = "zpay-test-signing-key"
)

type zpayNotificationFixture struct {
	service    *PaymentService
	provider   *provider.EasyPay
	order      *dbent.PaymentOrder
	userRepo   *mockUserRepo
	redeemRepo *paymentOrderLifecycleRedeemRepo
}

// 复用生命周期测试仓储，并将真实 EasyPay 实例绑定到订单快照。
func newZPayNotificationFixture(t *testing.T) *zpayNotificationFixture {
	t.Helper()
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)
	order := createPaymentOrderLifecycleOrder(t, ctx, client, OrderStatusPending, time.Now().Add(time.Hour))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse local query: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Path != "/api.php" || r.Form.Get("act") != "order" ||
			r.Form.Get("out_trade_no") != order.OutTradeNo ||
			r.Form.Get("pid") != zpayNotificationTestPID || r.Form.Get("key") != zpayNotificationTestKey {
			t.Error("unexpected local payment query")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":1,"status":0,"money":"88.00"}`))
	}))
	t.Cleanup(server.Close)
	config := map[string]string{
		"pid": zpayNotificationTestPID, "pkey": zpayNotificationTestKey,
		"apiBase": server.URL, "notifyUrl": "https://example.com/notify", "returnUrl": "https://example.com/return",
	}
	instance, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeEasyPay).
		SetName("zpay-notification-test").
		SetConfig(encryptWebhookProviderConfig(t, config)).
		SetSupportedTypes(payment.TypeAlipay).
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)
	instanceID := strconv.FormatInt(instance.ID, 10)
	order, err = client.PaymentOrder.UpdateOneID(order.ID).
		SetProviderInstanceID(instanceID).
		SetProviderKey(payment.TypeEasyPay).
		SetProviderSnapshot(map[string]any{
			"schema_version": 1, "provider_instance_id": instanceID,
			"provider_key": payment.TypeEasyPay, "merchant_id": zpayNotificationTestPID, "currency": "CNY",
		}).
		Save(ctx)
	require.NoError(t, err)
	easypay, err := provider.NewEasyPay(instanceID, config)
	require.NoError(t, err)
	registry := payment.NewRegistry()
	registry.Register(easypay)
	userRepo := &mockUserRepo{getByIDUser: &User{ID: order.UserID, Email: order.UserEmail, Username: order.UserName}}
	userRepo.updateBalanceFn = func(_ context.Context, userID int64, amount float64) error {
		require.Equal(t, order.UserID, userID)
		userRepo.getByIDUser.Balance += amount
		return nil
	}
	// 测试仓储不实现创建兑换码，预置未使用码后仍走完整的兑换和余额调整路径。
	redeemRepo := &paymentOrderLifecycleRedeemRepo{codesByCode: map[string]*RedeemCode{
		order.RechargeCode: {ID: 1, Code: order.RechargeCode, Type: RedeemTypeBalance, Value: order.Amount, Status: StatusUnused},
	}}
	svc := &PaymentService{
		entClient: client, registry: registry, providersLoaded: true, userRepo: userRepo,
		loadBalancer:  newWebhookProviderTestLoadBalancer(client),
		redeemService: NewRedeemService(redeemRepo, userRepo, nil, nil, nil, client, nil, nil),
	}
	return &zpayNotificationFixture{service: svc, provider: easypay, order: order, userRepo: userRepo, redeemRepo: redeemRepo}
}

// 按公开协议构造固定字段顺序的签名，独立于提供商内部的签名辅助函数。
func signedZPayNotification(order *dbent.PaymentOrder, pid, money string) url.Values {
	name := "余额充值 & 权益"
	tradeNo := "zpay-trade-" + strconv.FormatInt(order.ID, 10)
	canonical := "money=" + money + "&name=" + name + "&out_trade_no=" + order.OutTradeNo +
		"&pid=" + pid + "&trade_no=" + tradeNo + "&trade_status=TRADE_SUCCESS&type=alipay"
	signature := md5.Sum([]byte(canonical + zpayNotificationTestKey))
	return url.Values{
		"pid": {pid}, "type": {payment.TypeAlipay}, "out_trade_no": {order.OutTradeNo},
		"trade_no": {tradeNo}, "trade_status": {"TRADE_SUCCESS"}, "money": {money}, "name": {name},
		"sign": {hex.EncodeToString(signature[:])}, "sign_type": {"MD5"}, "param": {""},
	}
}

func zpayNotificationAuditCount(t *testing.T, fixture *zpayNotificationFixture, action string) int {
	t.Helper()
	count, err := fixture.service.entClient.PaymentAuditLog.Query().Where(
		paymentauditlog.OrderIDEQ(strconv.FormatInt(fixture.order.ID, 10)),
		paymentauditlog.ActionEQ(action),
	).Count(context.Background())
	require.NoError(t, err)
	return count
}

func TestZPaySignedNotificationFulfillsOnceAfterCancellation(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel_first_%t", cancelFirst), func(t *testing.T) {
			ctx := context.Background()
			fixture := newZPayNotificationFixture(t)
			if cancelFirst {
				outcome, err := fixture.service.CancelOrder(ctx, fixture.order.ID, fixture.order.UserID)
				require.NoError(t, err)
				require.Equal(t, checkPaidResultCancelled, outcome)
				cancelled, err := fixture.service.entClient.PaymentOrder.Get(ctx, fixture.order.ID)
				require.NoError(t, err)
				require.Equal(t, OrderStatusCancelled, cancelled.Status)
			}

			params := signedZPayNotification(fixture.order, zpayNotificationTestPID, "88.00")
			// 两次通知均经过真实验签；第二次不得再次兑换或重复写成功审计。
			for i := 0; i < 2; i++ {
				notification, err := fixture.provider.VerifyNotification(ctx, params.Encode(), nil)
				require.NoError(t, err)
				require.NoError(t, fixture.service.HandlePaymentNotification(ctx, notification, fixture.provider.ProviderKey()))
			}
			reloaded, err := fixture.service.entClient.PaymentOrder.Get(ctx, fixture.order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusCompleted, reloaded.Status)
			require.Equal(t, params.Get("trade_no"), reloaded.PaymentTradeNo)
			require.NotNil(t, reloaded.PaidAt)
			require.Equal(t, fixture.order.Amount, fixture.userRepo.getByIDUser.Balance)
			require.Len(t, fixture.redeemRepo.useCalls, 1)
			require.Equal(t, 1, zpayNotificationAuditCount(t, fixture, "ORDER_PAID"))
			require.Equal(t, 1, zpayNotificationAuditCount(t, fixture, "RECHARGE_SUCCESS"))
			if cancelFirst {
				require.Equal(t, 1, zpayNotificationAuditCount(t, fixture, "ORDER_CANCELLED"))
				require.Equal(t, 1, zpayNotificationAuditCount(t, fixture, "ORDER_RECOVERED"))
			} else {
				require.Zero(t, zpayNotificationAuditCount(t, fixture, "ORDER_RECOVERED"))
			}
		})
	}
}

func TestZPayTamperedNotificationCannotCreditBalance(t *testing.T) {
	fixture := newZPayNotificationFixture(t)
	params := signedZPayNotification(fixture.order, zpayNotificationTestPID, "88.00")
	// 保留原签名并篡改金额，模拟被修改的 GET 回调查询参数。
	params.Set("money", "99.00")
	notification, err := fixture.provider.VerifyNotification(context.Background(), params.Encode(), nil)
	require.ErrorContains(t, err, "invalid signature")
	require.Nil(t, notification)
	require.Zero(t, fixture.userRepo.getByIDUser.Balance)
	require.Empty(t, fixture.redeemRepo.useCalls)
	require.Zero(t, zpayNotificationAuditCount(t, fixture, "ORDER_PAID"))
}

func TestZPaySignedNotificationRejectsWrongAmountOrMerchant(t *testing.T) {
	for _, tt := range []struct {
		name   string
		pid    string
		money  string
		err    string
		action string
	}{
		{name: "wrong amount", pid: zpayNotificationTestPID, money: "87.00", err: "amount mismatch", action: "PAYMENT_AMOUNT_MISMATCH"},
		{name: "wrong merchant", pid: "another-merchant", money: "88.00", err: "easypay pid mismatch", action: "PAYMENT_PROVIDER_METADATA_MISMATCH"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newZPayNotificationFixture(t)
			params := signedZPayNotification(fixture.order, tt.pid, tt.money)
			notification, err := fixture.provider.VerifyNotification(ctx, params.Encode(), nil)
			require.NoError(t, err)
			err = fixture.service.HandlePaymentNotification(ctx, notification, fixture.provider.ProviderKey())
			require.ErrorContains(t, err, tt.err)
			reloaded, err := fixture.service.entClient.PaymentOrder.Get(ctx, fixture.order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusPending, reloaded.Status)
			require.Nil(t, reloaded.PaidAt)
			require.Zero(t, fixture.userRepo.getByIDUser.Balance)
			require.Empty(t, fixture.redeemRepo.useCalls)
			require.Equal(t, 1, zpayNotificationAuditCount(t, fixture, tt.action))
			require.Zero(t, zpayNotificationAuditCount(t, fixture, "ORDER_PAID"))
		})
	}
}
