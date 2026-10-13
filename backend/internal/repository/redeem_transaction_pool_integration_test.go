//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 其他仓储套件可能清空设置表，仅补齐本测试缺失的种子并在结束后恢复原先缺省状态。
func prepareRedeemPoolActivitySetting(t *testing.T) {
	t.Helper()
	initial, err := json.Marshal(service.DefaultDailyActivityConfig())
	require.NoError(t, err)
	result, err := integrationDB.ExecContext(context.Background(),
		`INSERT INTO settings(key,value,updated_at) VALUES($1,$2,NOW()) ON CONFLICT (key) DO NOTHING`,
		service.DailyActivitySettingKey, string(initial))
	require.NoError(t, err)
	inserted, err := result.RowsAffected()
	require.NoError(t, err)
	if inserted > 0 {
		t.Cleanup(func() {
			_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM settings WHERE key=$1`, service.DailyActivitySettingKey)
		})
	}
}

// 单连接池让事务内任何意外的第二次借连接都确定性超时，覆盖三类兑换权益。
func TestRedeemTransactionSingleConnectionPool(t *testing.T) {
	for _, kind := range []string{service.RedeemTypeBalance, service.RedeemTypeConcurrency, service.RedeemTypeSubscription} {
		t.Run(kind, func(t *testing.T) {
			prepareRedeemPoolActivitySetting(t)
			activityRepo, uid, cfg := dailyActivityFixture(t)
			cfg.IncludeRedeemBalance = true
			_, err := activityRepo.SaveConfig(context.Background(), *cfg)
			require.NoError(t, err)
			userRepo := NewUserRepository(integrationEntClient, integrationDB)
			redeemRepo := NewRedeemCodeRepository(integrationEntClient)
			subRepo := NewUserSubscriptionRepository(integrationEntClient)
			subService := service.NewSubscriptionService(nil, subRepo, nil, integrationEntClient, nil)
			redeemer := service.NewRedeemService(redeemRepo, userRepo, subService, nil, nil, integrationEntClient, nil, nil)
			before, err := userRepo.GetByID(context.Background(), uid)
			require.NoError(t, err)

			code := &service.RedeemCode{
				Code: uuid.NewString()[:32], Type: kind, Value: 2, MaxUses: 1, Status: service.StatusUnused,
			}
			if kind == service.RedeemTypeSubscription {
				plan, err := integrationEntClient.SubscriptionPlan.Create().
					SetName("single-connection-" + uuid.NewString()).
					SetDescription("单连接兑换事务回归").
					SetPrice(9.9).
					SetValidityDays(30).
					SetValidityUnit("day").
					SetDailyLimitUsd(1).
					SetMonthlyLimitUsd(30).
					Save(context.Background())
				require.NoError(t, err)
				code.PlanID = &plan.ID
				// 订阅先于套餐清理，用户及活动记录继续交由公共 fixture 清理。
				t.Cleanup(func() {
					_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM user_subscriptions WHERE user_id=$1 AND plan_id=$2`, uid, plan.ID)
					_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM subscription_plans WHERE id=$1`, plan.ID)
				})
			}
			require.NoError(t, redeemRepo.Create(context.Background(), code))
			t.Cleanup(func() {
				_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM redeem_codes WHERE id=$1`, code.ID)
			})

			// 准备完成后才收紧连接池；清理时首先恢复上限，不影响其他集成测试。
			dailyActivityStressPool(t, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := redeemer.Redeem(ctx, uid, code.Code)
			require.NoError(t, err, "事务查询必须复用唯一的数据库连接")
			require.Equal(t, 1, out.UsedCount)
			require.Equal(t, service.StatusUsed, out.Status)
			require.NotNil(t, out.UsedBy)
			require.Equal(t, uid, *out.UsedBy)
			usage, err := redeemRepo.GetUsageByRedeemCodeAndUser(ctx, code.ID, uid)
			require.NoError(t, err)
			require.NotNil(t, usage)
			require.NotZero(t, usage.ID)

			// 重复请求必须被拒绝，不能再发权益或新增使用记录。
			_, err = redeemer.Redeem(ctx, uid, code.Code)
			require.ErrorIs(t, err, service.ErrRedeemCodeAlreadyUsed)
			after, err := userRepo.GetByID(ctx, uid)
			require.NoError(t, err)
			wantBalance, wantConcurrency := before.Balance, before.Concurrency
			wantCredit, wantSubscriptions := 0.0, 0
			switch kind {
			case service.RedeemTypeBalance:
				wantBalance += code.Value
				wantCredit = code.Value
			case service.RedeemTypeConcurrency:
				wantConcurrency += int(code.Value)
			case service.RedeemTypeSubscription:
				wantSubscriptions = 1
				sub, err := subRepo.GetLatestByUserIDAndPlanID(ctx, uid, *code.PlanID)
				require.NoError(t, err)
				require.Equal(t, service.SubscriptionStatusActive, sub.Status)
				require.Equal(t, uid, sub.UserID)
				require.Equal(t, *code.PlanID, sub.PlanID)
				require.True(t, sub.ExpiresAt.Equal(sub.StartsAt.AddDate(0, 0, 30)))
				require.NotNil(t, sub.DailyLimitUSD)
				require.Equal(t, 1.0, *sub.DailyLimitUSD)
				require.NotNil(t, sub.MonthlyLimitUSD)
				require.Equal(t, 30.0, *sub.MonthlyLimitUSD)
			}
			require.Equal(t, wantBalance, after.Balance)
			require.Equal(t, wantConcurrency, after.Concurrency)
			var usages, subscriptions, credits int
			var creditAmount float64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM redeem_code_usages WHERE redeem_code_id=$1 AND user_id=$2`, code.ID, uid).Scan(&usages))
			require.Equal(t, 1, usages)
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM user_subscriptions WHERE user_id=$1`, uid).Scan(&subscriptions))
			require.Equal(t, wantSubscriptions, subscriptions)
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(amount),0) FROM daily_activity_credit_events WHERE user_id=$1`, uid).Scan(&credits, &creditAmount))
			require.Equal(t, wantCredit, creditAmount)
			if kind == service.RedeemTypeBalance {
				require.Equal(t, 1, credits)
			} else {
				require.Zero(t, credits)
			}
		})
	}
}
