//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 用真实仓储生成凭据，清理时先删使用记录，避免与fixture的用户清理相互依赖。
func dailyActivityCreditCode(t *testing.T, kind string, amount float64, maxUses int) *service.RedeemCode {
	t.Helper()
	code := &service.RedeemCode{Code: uuid.NewString()[:32], Type: kind, Value: amount, MaxUses: maxUses, Status: service.StatusUnused}
	require.NoError(t, NewRedeemCodeRepository(integrationEntClient).Create(context.Background(), code))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM redeem_codes WHERE id=$1`, code.ID)
	})
	return code
}

func dailyActivityCreditUsage(t *testing.T, uid int64, kind string, amount float64) *service.RedeemCodeUsage {
	t.Helper()
	code := dailyActivityCreditCode(t, kind, amount, 1)
	usage := &service.RedeemCodeUsage{RedeemCodeID: code.ID, UserID: uid, UsedAt: time.Now()}
	require.NoError(t, NewRedeemCodeRepository(integrationEntClient).CreateUsage(context.Background(), usage))
	return usage
}

func dailyActivityRedeemer() *service.RedeemService {
	return service.NewRedeemService(NewRedeemCodeRepository(integrationEntClient), NewUserRepository(integrationEntClient, integrationDB), nil, nil, nil, integrationEntClient, nil, nil)
}

func TestDailyActivityCreditSourceCombinationsAndTypeExclusions(t *testing.T) {
	for _, flags := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprint(flags), func(t *testing.T) {
			repo, uid, cfg := dailyActivityFixture(t)
			cfg.IncludeRedeemBalance = flags[0]
			cfg.IncludeAdminBalance = flags[1]
			_, err := repo.SaveConfig(context.Background(), *cfg)
			require.NoError(t, err)
			dailyActivityCreditUsage(t, uid, service.RedeemTypeBalance, 2.12345678)
			dailyActivityCreditUsage(t, uid, service.AdjustmentTypeAdminBalance, 3)
			for _, kind := range []string{service.RedeemTypeConcurrency, service.RedeemTypeSubscription, service.AdjustmentTypeAdminConcurrency} {
				dailyActivityCreditUsage(t, uid, kind, 100)
			}
			dailyActivityCreditUsage(t, uid, service.RedeemTypeBalance, 0)
			dailyActivityCreditUsage(t, uid, service.RedeemTypeBalance, -100)
			dailyActivityCreditUsage(t, uid, service.AdjustmentTypeAdminBalance, -100)
			var want float64
			if flags[0] {
				want += 2.12345678
			}
			if flags[1] {
				want += 3
			}
			status, err := repo.Status(context.Background(), uid)
			require.NoError(t, err)
			require.Equal(t, flags[0], status.IncludeRedeemBalance)
			require.Equal(t, flags[1], status.IncludeAdminBalance)
			require.InDelta(t, want, status.Activities[0].EligibleAmount, 1e-9)
			require.InDelta(t, want, status.Activities[1].EligibleAmount, 1e-9)
			require.Zero(t, status.Activities[2].EligibleAmount)
		})
	}
}

func TestDailyActivityCreditStartsOnlyWhenSourceEnabledAndPersistsAcrossDays(t *testing.T) {
	repo, uid, cfg := dailyActivityFixture(t)
	ctx := context.Background()
	redeemer := dailyActivityRedeemer()
	// 首次启用活动之前即使来源为开，也不能记录；模拟尚未启用的默认配置。
	raw := `{"enabled":false,"include_redeem_balance":true,"daily_gift":{"threshold":0.1,"reward_max":0.1},"recharge":{"threshold":0.1,"reward_max":0.1},"consumption":{"threshold":0.1,"reward_max":0.1}}`
	_, err := integrationDB.ExecContext(ctx, `UPDATE settings SET value=$1 WHERE key=$2`, raw, service.DailyActivitySettingKey)
	require.NoError(t, err)
	beforeStart := dailyActivityCreditCode(t, service.RedeemTypeBalance, 1, 1)
	_, err = redeemer.Redeem(ctx, uid, beforeStart.Code)
	require.NoError(t, err)
	cfg, err = repo.SaveConfig(ctx, *cfg)
	require.NoError(t, err)
	beforeSource := dailyActivityCreditCode(t, service.RedeemTypeBalance, 2, 1)
	_, err = redeemer.Redeem(ctx, uid, beforeSource.Code)
	require.NoError(t, err)
	cfg.IncludeRedeemBalance = true
	cfg, err = repo.SaveConfig(ctx, *cfg)
	require.NoError(t, err)
	first := dailyActivityCreditCode(t, service.RedeemTypeBalance, .3, 2)
	_, err = redeemer.Redeem(ctx, uid, first.Code)
	require.NoError(t, err)
	other := mustCreateUser(t, integrationEntClient, &service.User{})
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, other.ID) })
	_, err = redeemer.Redeem(ctx, other.ID, first.Code)
	require.NoError(t, err)
	claimInput := service.DailyActivityClaimInput{ActivityType: "recharge", All: true, IdempotencyKey: uuid.NewString()}
	result, err := repo.Claim(ctx, uid, claimInput)
	require.NoError(t, err)
	require.Len(t, result.Items, 3)
	cfg.IncludeRedeemBalance = false
	cfg, err = repo.SaveConfig(ctx, *cfg)
	require.NoError(t, err)
	off := dailyActivityCreditCode(t, service.RedeemTypeBalance, 100, 1)
	_, err = redeemer.Redeem(ctx, uid, off.Code)
	require.NoError(t, err)
	status, err := repo.Status(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, .3, status.Activities[1].EligibleAmount)
	require.Equal(t, int64(3), status.Activities[1].ClaimedCount)
	cfg.IncludeRedeemBalance = true
	cfg.Enabled = false
	cfg, err = repo.SaveConfig(ctx, *cfg)
	require.NoError(t, err)
	// 活动暂停只停止新领取，已首次启用且来源开启时继续累计新增充值。
	onAgain := dailyActivityCreditCode(t, service.RedeemTypeBalance, .2, 1)
	_, err = redeemer.Redeem(ctx, uid, onAgain.Code)
	require.NoError(t, err)
	_, err = repo.Claim(ctx, uid, service.DailyActivityClaimInput{ActivityType: "recharge", IdempotencyKey: uuid.NewString()})
	require.ErrorIs(t, err, service.ErrDailyActivityDisabled)
	replay, err := repo.Claim(ctx, uid, claimInput)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	cfg.Enabled = true
	cfg, err = repo.SaveConfig(ctx, *cfg)
	require.NoError(t, err)
	status, err = repo.Status(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, .5, status.Activities[1].EligibleAmount)
	require.Equal(t, int64(2), status.Activities[1].AvailableCount)
	next, err := dailyActivityStatus(ctx, integrationDB, uid, cfg, dailyActivityDBNow(t).Add(48*time.Hour))
	require.NoError(t, err)
	require.Zero(t, next.Activities[0].EligibleAmount)
	require.Equal(t, .5, next.Activities[1].EligibleAmount)
	status, err = repo.Status(ctx, other.ID)
	require.NoError(t, err)
	require.Equal(t, .3, status.Activities[1].EligibleAmount)
}

func TestDailyActivityCreditInternalPaymentCodesNeverDoubleCount(t *testing.T) {
	repo, uid, cfg := dailyActivityFixture(t)
	ctx := context.Background()
	cfg.IncludeRedeemBalance = true
	cfg.IncludeAdminBalance = true
	_, err := repo.SaveConfig(ctx, *cfg)
	require.NoError(t, err)
	for _, status := range []string{"PENDING", "PROCESSING", "PAID", "RECHARGING", "COMPLETED", "PARTIALLY_REFUNDED", "REFUND_FAILED", "FAILED", "EXPIRED", "CANCELLED", "REFUND_REQUESTED", "REFUNDING", "REFUND_PENDING", "REFUNDED"} {
		code := dailyActivityCreditCode(t, service.RedeemTypeBalance, 10, 1)
		_, err = integrationDB.ExecContext(ctx, `INSERT INTO payment_orders(user_id,amount,pay_amount,bonus_amount,status,order_type,payment_type,completed_at,expires_at,recharge_code) VALUES($1,10,8,2,$2,'balance','alipay',clock_timestamp(),NOW()+INTERVAL '1 day',$3)`, uid, status, code.Code)
		require.NoError(t, err)
		_, err = dailyActivityRedeemer().Redeem(ctx, uid, code.Code)
		require.NoError(t, err)
	}
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM daily_activity_credit_events WHERE user_id=$1`, uid).Scan(&count))
	require.Zero(t, count)
	status, err := repo.Status(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, 56.0, status.Activities[1].EligibleAmount, "完成及六个退款状态各计原本金8，不包含内部兑换或赠金")
}

func TestDailyActivityCreditConcurrentRedeemDeduplicates(t *testing.T) {
	repo, uid, cfg := dailyActivityFixture(t)
	ctx := context.Background()
	cfg.IncludeRedeemBalance = true
	_, err := repo.SaveConfig(ctx, *cfg)
	require.NoError(t, err)
	code := dailyActivityCreditCode(t, service.RedeemTypeBalance, .3, 100)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := dailyActivityRedeemer().Redeem(ctx, uid, code.Code); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), successes.Load())
	usage, err := NewRedeemCodeRepository(integrationEntClient).GetUsageByRedeemCodeAndUser(ctx, code.ID, uid)
	require.NoError(t, err)
	// 事件层独立幂等保障恢复重入不会重复增加金额。
	require.NoError(t, recordDailyActivityCredit(ctx, integrationEntClient, usage))
	status, err := repo.Status(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, .3, status.Activities[1].EligibleAmount)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM daily_activity_credit_events WHERE user_id=$1`, uid).Scan(&count))
	require.Equal(t, 1, count)
	user, err := NewUserRepository(integrationEntClient, integrationDB).GetByID(ctx, uid)
	require.NoError(t, err)
	require.InDelta(t, 5.3, user.Balance, 1e-9)
}

func TestDailyActivityCreditFailureRollsBackRealRedemption(t *testing.T) {
	repo, uid, cfg := dailyActivityFixture(t)
	ctx := context.Background()
	cfg.IncludeRedeemBalance = true
	_, err := repo.SaveConfig(ctx, *cfg)
	require.NoError(t, err)
	code := dailyActivityCreditCode(t, service.RedeemTypeBalance, 3, 1)
	constraint := "daily_credit_test_reject_" + fmt.Sprint(uid)
	_, err = integrationDB.ExecContext(ctx, `ALTER TABLE daily_activity_credit_events ADD CONSTRAINT `+constraint+` CHECK(user_id<>`+fmt.Sprint(uid)+`)`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `ALTER TABLE daily_activity_credit_events DROP CONSTRAINT IF EXISTS `+constraint)
	})
	_, err = dailyActivityRedeemer().Redeem(ctx, uid, code.Code)
	require.ErrorContains(t, err, "record daily activity credit")
	user, err := NewUserRepository(integrationEntClient, integrationDB).GetByID(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, 5.0, user.Balance)
	usage, err := NewRedeemCodeRepository(integrationEntClient).GetUsageByRedeemCodeAndUser(ctx, code.ID, uid)
	require.NoError(t, err)
	require.Nil(t, usage)
	saved, err := NewRedeemCodeRepository(integrationEntClient).GetByID(ctx, code.ID)
	require.NoError(t, err)
	require.Zero(t, saved.UsedCount)
	_, err = integrationDB.ExecContext(ctx, `ALTER TABLE daily_activity_credit_events DROP CONSTRAINT `+constraint)
	require.NoError(t, err)
	_, err = dailyActivityRedeemer().Redeem(ctx, uid, code.Code)
	require.NoError(t, err)
	status, err := repo.Status(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, 3.0, status.Activities[1].EligibleAmount)
}
