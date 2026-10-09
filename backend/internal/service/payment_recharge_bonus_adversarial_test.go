//go:build unit

package service

import (
	"context"
	"math"
	"sync"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/payment"
	"github.com/stretchr/testify/require"
)

// 用屏障固定两个服务实例同时进入更新，事务替身在读取现值前持锁。
type adversarialBonusSettings struct {
	SettingRepository
	mu      sync.Mutex
	values  map[string]string
	readers sync.WaitGroup
	release chan struct{}
}

func (r *adversarialBonusSettings) UpdateMultiple(_ context.Context, keys []string, update func(map[string]string) (map[string]string, error)) error {
	r.readers.Done()
	<-r.release
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		out[key] = r.values[key]
	}
	values, err := update(out)
	if err != nil {
		return err
	}
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}

// 模式与档位是同一个业务不变量；并发成功的更新也必须得到合法组合。
func TestAdversarialRechargeBonusConcurrentPatchPreservesInvariant(t *testing.T) {
	repo := &adversarialBonusSettings{
		values: map[string]string{
			SettingRechargeBonusMode:  RechargeBonusModeBonus,
			SettingRechargeBonusTiers: `[{"min_amount":100,"bonus_percent":20}]`,
		},
		release: make(chan struct{}),
	}
	repo.readers.Add(2)
	services := []*PaymentConfigService{{settingRepo: repo}, {settingRepo: repo}}
	discount := RechargeBonusModeDiscount
	tiers := []RechargeBonusTier{{MinAmount: 100, BonusPercent: 200}}
	errors := make(chan error, 2)
	go func() {
		errors <- services[0].UpdatePaymentConfig(context.Background(), UpdatePaymentConfigRequest{RechargeBonusMode: &discount})
	}()
	go func() {
		errors <- services[1].UpdatePaymentConfig(context.Background(), UpdatePaymentConfigRequest{RechargeBonusTiers: &tiers})
	}()
	repo.readers.Wait()
	close(repo.release)
	successes := 0
	for range 2 {
		if err := <-errors; err == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes, "后到的冲突 PATCH 应根据已提交配置校验失败")
	cfg := services[0].parsePaymentConfig(repo.values)
	q := quoteRechargeBonus(cfg, 100, "CNY")
	t.Logf("并发保存结果：mode=%s tiers=%+v quote=%+v", cfg.RechargeBonusMode, cfg.RechargeBonusTiers, q)
	require.NoError(t, ValidateRechargeBonusTiersForMode(cfg.RechargeBonusMode, cfg.RechargeBonusTiers),
		"两个成功 PATCH 落库后不应产生 discount + 200%；当前报价会静默取消全部优惠")
}

// 用合法配置验证所有入口数值边界，不能因折扣舍入把非法原始金额变成合法金额。
func TestAdversarialRechargeBonusRejectsInvalidAmountsAndTiers(t *testing.T) {
	cfg := &PaymentConfig{BalanceRechargeMultiplier: 0.14, RechargeBonusMode: RechargeBonusModeDiscount,
		RechargeBonusTiers: []RechargeBonusTier{{MinAmount: 0, BonusPercent: 99.99}}}
	for _, amount := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, 0, 0.001, 1.001, math.MaxFloat64} {
		require.NotPanics(t, func() {
			_, err := (&PaymentService{}).validateOrderInput(context.Background(), CreateOrderRequest{OrderType: payment.OrderTypeBalance, Amount: amount}, cfg)
			if err == nil {
				_, err = quoteRechargeBonusForOrder(cfg, amount, "CNY")
			}
			require.Error(t, err, "amount=%v", amount)
		})
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.01, 1.001} {
		_, err := NormalizeRechargeBonusTiers([]RechargeBonusTier{{MinAmount: value, BonusPercent: 20}})
		require.Error(t, err)
		_, err = NormalizeRechargeBonusTiers([]RechargeBonusTier{{MinAmount: 100, BonusPercent: value}})
		require.Error(t, err)
	}
	_, err := NormalizeRechargeBonusTiers([]RechargeBonusTier{{MinAmount: 0, BonusPercent: 20}, {MinAmount: math.Copysign(0, -1), BonusPercent: 30}})
	require.Error(t, err, "正负零阈值必须视为重复档位")
}

// 小额、半分边界与不同币种都必须保持原到账退款比例，不重复赠送。
func TestAdversarialRechargeBonusRefundAndRebateConsistency(t *testing.T) {
	for _, currency := range []string{"CNY", "USD", "JPY", "KWD"} {
		for _, mode := range []string{RechargeBonusModeBonus, RechargeBonusModeDiscount} {
			cfg := &PaymentConfig{BalanceRechargeMultiplier: 0.14, RechargeBonusMode: mode,
				RechargeBonusTiers: []RechargeBonusTier{{MinAmount: 0, BonusPercent: 33.33}, {MinAmount: 100, BonusPercent: 99.99}}}
			for _, amount := range []float64{1, 2, 33, 99, 100, 101, 99999} {
				q, err := quoteRechargeBonusForOrder(cfg, amount, currency)
				require.NoError(t, err)
				require.Greater(t, q.PayBase, 0.0)
				require.GreaterOrEqual(t, q.Bonus, 0.0)
				require.LessOrEqual(t, q.Bonus, q.Credited)
				fees, _, paid, err := calculateCreateOrderPayAmount(q.PayBase, payment.FeeConfig{FixedFee: 2, FeeRate: 3}, currency)
				require.NoError(t, err)
				require.Equal(t, fees.PayAmount, paid)
				require.Equal(t, paid, calculateGatewayRefundAmount(q.Credited, paid, q.Credited, currency))
				partial := calculateGatewayRefundAmount(q.Credited, paid, q.Credited/2, currency)
				require.InDelta(t, paid/2, partial, math.Pow10(-payment.CurrencyMaxFractionDigits(currency))/2+1e-9)
			}
		}
	}
}

// 相同成功通知与失败通知交错时，只发放订单快照中的总额一次。
func TestAdversarialRechargeBonusMixedReplayCreditsSnapshotOnce(t *testing.T) {
	f := newZPayNotificationFixture(t)
	ctx := context.Background()
	order, err := f.service.entClient.PaymentOrder.UpdateOneID(f.order.ID).
		SetAmount(120).SetBonusAmount(20).SetPayAmount(100).Save(ctx)
	require.NoError(t, err)
	f.redeemRepo.codesByCode[order.RechargeCode].Value = 120
	for _, amount := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -100, 0, 99, 101} {
		err := f.service.HandlePaymentNotification(ctx, &payment.PaymentNotification{
			OrderID: order.OutTradeNo, TradeNo: "synthetic-invalid-amount", Amount: amount,
			Status: payment.ProviderStatusSuccess, Metadata: map[string]string{"pid": zpayNotificationTestPID},
		}, payment.TypeEasyPay)
		require.Error(t, err)
		require.Zero(t, f.userRepo.getByIDUser.Balance)
	}
	for i := 0; i < 24; i++ {
		n, err := f.provider.VerifyNotification(ctx, signedZPayNotification(order, zpayNotificationTestPID, "100.00").Encode(), nil)
		require.NoError(t, err)
		if i%3 == 2 {
			n.Status = payment.ProviderStatusFailed
		}
		require.NoError(t, f.service.HandlePaymentNotification(ctx, n, payment.TypeEasyPay))
	}
	require.Equal(t, 120.0, f.userRepo.getByIDUser.Balance)
	require.Len(t, f.redeemRepo.useCalls, 1)
	stored, err := f.service.entClient.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, stored.Status)
	points, ok := paymentOrderPurchasedReasoningPoints(stored)
	require.True(t, ok)
	require.Equal(t, 120.0, points, "返利保持 fork 按到账金额计算的规则")
}

// 活动调整和畸形的客户端充值金额不能改变已完成的站内钱包扣款。
func TestAdversarialRechargeBonusWalletIsolationAndReplay(t *testing.T) {
	f := newWalletPaymentFixture(t, 200, 500)
	f.settings.values[SettingRechargeBonusMode] = RechargeBonusModeBonus
	f.settings.values[SettingRechargeBonusTiers] = `[{"min_amount":0,"bonus_percent":1000}]`
	ctx := context.Background()
	req := f.request(1)
	req.Amount = 999999
	first, err := f.service.CreateOrder(ctx, req)
	require.NoError(t, err)
	f.settings.values[SettingRechargeBonusMode] = RechargeBonusModeDiscount
	f.settings.values[SettingRechargeBonusTiers] = `[{"min_amount":0,"bonus_percent":99.99}]`
	for _, amount := range []float64{-1, 0, math.Inf(1), math.NaN(), 0.001, 999999} {
		req.Amount = amount
		replayed, err := f.service.CreateOrder(ctx, req)
		require.NoError(t, err)
		require.Equal(t, first.OrderID, replayed.OrderID)
		require.Equal(t, 80.25, replayed.PayAmount)
		require.Zero(t, replayed.BonusAmount)
	}
	user, err := f.client.User.Get(ctx, f.user.ID)
	require.NoError(t, err)
	require.Equal(t, 119.75, user.Balance)
	require.Equal(t, 500.0, user.FrozenBalance)
	count, err := f.client.UserSubscription.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}
