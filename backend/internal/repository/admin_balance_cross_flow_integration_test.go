//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 同时执行不同入口的余额变动，用落库的真实增量核对守恒，避免 set 覆盖并发加款或扣费。
func TestAdminBalanceCrossFlowConcurrentConservation(t *testing.T) {
	activity, uid, cfg := dailyActivityFixture(t)
	cfg.IncludeAdminBalance = true
	cfg.IncludeRedeemBalance = true
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, err := activity.SaveConfig(ctx, *cfg)
	require.NoError(t, err)
	admin := newAdminBalanceTestService(NewUserRepository(integrationEntClient, integrationDB), NewRedeemCodeRepository(integrationEntClient), nil)
	_, err = admin.UpdateUserBalance(ctx, uid, 10, "add", "并发测试准备资格")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE users SET frozen_balance=.25 WHERE id=$1`, uid)
	require.NoError(t, err)
	key := mustCreateApiKey(t, integrationEntClient, &service.APIKey{UserID: uid, Key: "sk-cross-" + uuid.NewString(), Name: "混合并发"})
	billing := NewUsageBillingRepository(integrationEntClient, integrationDB)
	redeemer := dailyActivityRedeemer()

	var work []func() error
	for range 5 {
		for _, adjustment := range []struct {
			op     string
			amount float64
		}{{"add", .3}, {"subtract", .1}, {"set", 20}} {
			work = append(work, func() error {
				_, err := admin.UpdateUserBalance(ctx, uid, adjustment.amount, adjustment.op, "多入口并发调账")
				return err
			})
		}
	}
	var applied, claims, replays, redemptions atomic.Int32
	for range 8 {
		command := service.UsageBillingCommand{RequestID: "cross:" + uuid.NewString(), APIKeyID: key.ID, UserID: uid, ActorUserID: uid, BillableAmountUSD: .2, APIKeyBillingMode: service.APIKeyBillingModeBalance}
		for range 2 {
			work = append(work, func() error {
				copy := command
				result, err := billing.Apply(ctx, &copy)
				if err == nil && result.Applied {
					applied.Add(1)
				}
				return err
			})
		}
	}
	for range 10 {
		input := service.DailyActivityClaimInput{ActivityType: "recharge", IdempotencyKey: uuid.NewString()}
		for range 2 {
			work = append(work, func() error {
				result, err := activity.Claim(ctx, uid, input)
				if err == nil {
					if result.Replayed {
						replays.Add(1)
					} else {
						claims.Add(1)
					}
				}
				return err
			})
		}
	}
	for range 4 {
		code := dailyActivityCreditCode(t, service.RedeemTypeBalance, .5, 1)
		for range 2 {
			work = append(work, func() error {
				_, err := redeemer.Redeem(ctx, uid, code.Code)
				if err == nil {
					redemptions.Add(1)
				} else if errors.Is(err, service.ErrRedeemCodeAlreadyUsed) || errors.Is(err, service.ErrRedeemCodeMaxUsed) {
					return nil
				}
				return err
			})
		}
	}
	adminBalanceRunConcurrent(t, work)
	require.EqualValues(t, 8, applied.Load())
	require.EqualValues(t, 10, claims.Load())
	require.EqualValues(t, 10, replays.Load())
	require.EqualValues(t, 4, redemptions.Load())

	var balance, frozen, adminDelta, adminPositive, redeemed, rewarded float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance,frozen_balance FROM users WHERE id=$1`, uid).Scan(&balance, &frozen))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE(SUM(rc.value) FILTER(WHERE rc.type='admin_balance'),0),COALESCE(SUM(GREATEST(rc.value,0)) FILTER(WHERE rc.type='admin_balance'),0),COALESCE(SUM(rc.value) FILTER(WHERE rc.type='balance'),0) FROM redeem_code_usages ru JOIN redeem_codes rc ON rc.id=ru.redeem_code_id WHERE ru.user_id=$1`, uid).Scan(&adminDelta, &adminPositive, &redeemed))
	var claimCount, batchCount, creditCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(amount),0) FROM daily_activity_claims WHERE user_id=$1`, uid).Scan(&claimCount, &rewarded))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM daily_activity_claim_batches WHERE user_id=$1`, uid).Scan(&batchCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM daily_activity_credit_events WHERE user_id=$1 AND source_type='redeem_balance'`, uid).Scan(&creditCount))
	require.Equal(t, 10, claimCount)
	require.Equal(t, 10, batchCount)
	require.Equal(t, 4, creditCount)
	require.InDelta(t, 1, rewarded, 1e-9)
	require.InDelta(t, 2, redeemed, 1e-9)
	require.Equal(t, .25, frozen)
	assertDailyActivitySpending(t, uid, 8, 1.6)
	require.InDelta(t, 5+adminDelta+redeemed+rewarded-1.6, balance, 1e-8)
	status, err := activity.Status(ctx, uid)
	require.NoError(t, err)
	// 活动奖励本身不能产生二次充值资格；扣费只记录最终一次实际消费。
	require.InDelta(t, adminPositive+redeemed, status.Activities[1].EligibleAmount, 1e-8)
	require.InDelta(t, 1.6, status.Activities[2].EligibleAmount, 1e-8)
}

// 多用户复用多个兑换码并重放请求，成功次数与资格都必须受真实使用记录约束。
func TestAdminBalanceCrossFlowMultiUserMultiCode(t *testing.T) {
	for _, maximum := range []int{8, 4} {
		t.Run(fmt.Sprintf("max_uses_%d", maximum), func(t *testing.T) {
			activity, firstUID, cfg := dailyActivityFixture(t)
			cfg.IncludeRedeemBalance = true
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			_, err := activity.SaveConfig(ctx, *cfg)
			require.NoError(t, err)
			ids := []int64{firstUID}
			for range 7 {
				// 连续创建不依赖宿主时钟精度，避免测试邮箱偶发重复。
				user := mustCreateUser(t, integrationEntClient, &service.User{Email: "activity-cross-" + uuid.NewString() + "@example.com", Balance: 5})
				ids = append(ids, user.ID)
				t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM users WHERE id=$1`, user.ID) })
			}
			codes := []*service.RedeemCode{
				dailyActivityCreditCode(t, service.RedeemTypeBalance, .11, maximum),
				dailyActivityCreditCode(t, service.RedeemTypeBalance, .23, maximum),
				dailyActivityCreditCode(t, service.RedeemTypeBalance, .37, maximum),
			}
			var work []func() error
			var successes atomic.Int32
			for _, uid := range ids {
				for _, code := range codes {
					for range 2 {
						work = append(work, func() error {
							_, err := dailyActivityRedeemer().Redeem(ctx, uid, code.Code)
							if err == nil {
								successes.Add(1)
							} else if errors.Is(err, service.ErrRedeemCodeAlreadyUsed) || errors.Is(err, service.ErrRedeemCodeMaxUsed) {
								return nil
							}
							return err
						})
					}
				}
			}
			adminBalanceRunConcurrent(t, work)
			require.EqualValues(t, maximum*3, successes.Load())
			var total float64
			for _, uid := range ids {
				var balance, added float64
				var usages, credits int
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(rc.value),0) FROM redeem_code_usages ru JOIN redeem_codes rc ON rc.id=ru.redeem_code_id WHERE ru.user_id=$1`, uid).Scan(&usages, &added))
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM daily_activity_credit_events WHERE user_id=$1`, uid).Scan(&credits))
				require.Equal(t, usages, credits)
				require.LessOrEqual(t, usages, 3)
				require.InDelta(t, 5+added, balance, 1e-9)
				status, err := activity.Status(ctx, uid)
				require.NoError(t, err)
				require.InDelta(t, added, status.Activities[1].EligibleAmount, 1e-9)
				total += added
			}
			require.InDelta(t, float64(maximum)*.71, total, 1e-9)
			for _, code := range codes {
				var usedCount, usageCount int
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT used_count,(SELECT COUNT(*) FROM redeem_code_usages WHERE redeem_code_id=$1) FROM redeem_codes WHERE id=$1`, code.ID).Scan(&usedCount, &usageCount))
				require.Equal(t, maximum, usedCount)
				require.Equal(t, maximum, usageCount)
			}
		})
	}
}

// 共用起跑屏障，提高锁交错覆盖；错误留到主测试线程断言，避免 goroutine 中中断测试。
func adminBalanceRunConcurrent(t *testing.T, work []func() error) {
	t.Helper()
	previousMax := integrationDB.Stats().MaxOpenConnections
	// 此处专测混合事务的余额与资格守恒，单连接和连接池满载由独立回归覆盖。
	integrationDB.SetMaxOpenConns(64)
	t.Cleanup(func() { integrationDB.SetMaxOpenConns(previousMax) })
	start := make(chan struct{})
	errs := make(chan error, len(work))
	var wg sync.WaitGroup
	for _, run := range work {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- run()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}
