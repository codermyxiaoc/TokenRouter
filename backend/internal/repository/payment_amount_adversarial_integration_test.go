//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 使用独立 PostgreSQL 测试容器核对实付金额往返，避免 SQLite 掩盖小数位截断。
func TestAdversarialPaymentAmountPrecisionSurvivesPostgres(t *testing.T) {
	client := testEntClient(t)
	ctx := context.Background()
	user := mustCreateUser(t, client, &service.User{Email: "payment-precision-" + uuid.NewString() + "@example.com", PasswordHash: "test"})
	for _, tc := range []struct {
		currency string
		paid     float64
	}{
		{currency: "CNY", paid: 0.67},
		{currency: "JPY", paid: 1},
		{currency: "KWD", paid: 0.667},
		{currency: "BHD", paid: 0.333},
	} {
		t.Run(tc.currency, func(t *testing.T) {
			order, err := client.PaymentOrder.Create().SetUserID(user.ID).
				SetUserEmail(user.Email).SetUserName("precision-test").
				SetAmount(1).SetBonusAmount(0.33).SetPayAmount(tc.paid).
				SetFeeFixed(tc.paid).SetFeeRateAmount(tc.paid).SetFeeAmount(tc.paid).SetRefundAmount(tc.paid).
				SetRechargeCode("test-" + uuid.NewString()).SetOutTradeNo("test-" + uuid.NewString()).
				SetPaymentType("stripe").SetPaymentTradeNo("").SetClientIP("127.0.0.1").
				SetSrcHost("localhost").SetExpiresAt(time.Now().Add(time.Hour)).Save(ctx)
			require.NoError(t, err)
			stored, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, tc.paid, stored.PayAmount, "保存后的实付金额必须与发送给支付供应商的金额一致")
			require.Equal(t, tc.paid, stored.FeeFixed)
			require.Equal(t, tc.paid, stored.FeeRateAmount)
			require.Equal(t, tc.paid, stored.FeeAmount)
			require.Equal(t, tc.paid, stored.RefundAmount)
		})
	}
}

// 生产由 SQL 迁移建表，金额精度必须与 Ent 定义一致且不能缩小历史整数容量。
func TestPaymentOrderCurrencyPrecisionMigration(t *testing.T) {
	for _, column := range []string{"amount", "pay_amount", "fee_fixed", "fee_rate_amount", "fee_amount", "bonus_amount", "refund_amount"} {
		var precision, scale int
		err := integrationDB.QueryRowContext(context.Background(), `SELECT numeric_precision, numeric_scale
FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'payment_orders' AND column_name = $1`, column).Scan(&precision, &scale)
		require.NoError(t, err)
		require.Equal(t, 21, precision, column)
		require.Equal(t, 3, scale, column)
	}
}
