//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

// 在真实 SQL 中同时放入旧套餐与额度充足的新套餐，验证异步任务只消费创建时的资金范围。
func TestUsageBillingRepositorySubscriptionScope(t *testing.T) {
	for _, tc := range []struct {
		name         string
		mode         string
		expired      bool
		scoped       bool
		wantOldUsage float64
		wantNewUsage float64
		wantBalance  float64
		wantErr      error
	}{
		{"auto_only_original_plus_balance", service.APIKeyBillingModeAuto, false, true, 10, 0, 8.8, nil},
		{"auto_expired_original_uses_balance", service.APIKeyBillingModeAuto, true, true, 9.8, 0, 8, nil},
		{"subscription_expired_remains_rejected", service.APIKeyBillingModeSubscription, true, true, 9.8, 0, 10, service.ErrPreferredSubscriptionInsufficient},
		{"subscription_overflow_remains_allowed", service.APIKeyBillingModeSubscription, false, true, 10, 0, 8.8, nil},
		{"ordinary_auto_still_uses_multiple_subscriptions", service.APIKeyBillingModeAuto, false, false, 10, 0.3, 10, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			repo := NewUsageBillingRepository(client, integrationDB)
			user := mustCreateUser(t, client, &service.User{Email: "scope-" + uuid.NewString() + "@example.com", Balance: 10})
			key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-scope-" + uuid.NewString(), Name: "scope"})
			now := time.Now()
			window := now.Add(-time.Hour)
			createSubscription := func(usage float64, expiry time.Time) *service.UserSubscription {
				plan := mustCreatePlan(t, client, &service.SubscriptionPlan{
					Name: "scope-" + uuid.NewString(), Price: 10, ValidityDays: 30, ValidityUnit: "day", ForSale: true,
				})
				return mustCreateSubscription(t, client, &service.UserSubscription{
					UserID: user.ID, PlanID: plan.ID, StartsAt: now.Add(-24 * time.Hour), ExpiresAt: expiry,
					DailyWindowStart: &window, WeeklyWindowStart: &window, MonthlyWindowStart: &window,
					DailyLimitUSD: float64Ptr(10), WeeklyLimitUSD: float64Ptr(10), MonthlyLimitUSD: float64Ptr(10),
					DailyUsageUSD: usage, WeeklyUsageUSD: usage, MonthlyUsageUSD: usage,
				})
			}
			oldSub := createSubscription(9.8, now.Add(24*time.Hour))
			newSub := createSubscription(0, now.Add(48*time.Hour))
			// 模拟创建视频之后旧订阅过期，且此时用户已有另一个可用的新套餐。
			if tc.expired {
				_, err := integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, oldSub.ID)
				require.NoError(t, err)
			}
			cmd := &service.UsageBillingCommand{
				RequestID: "scope-task-" + uuid.NewString(), APIKeyID: key.ID, UserID: user.ID,
				APIKeyBillingMode: tc.mode, BillableAmountUSD: 0.5, BaseAmountUSD: 1,
				SubscriptionRateMultiplier: 0.5, SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: 2,
			}
			if tc.scoped {
				cmd.SubscriptionScopeID = &oldSub.ID
			}
			if tc.mode == service.APIKeyBillingModeSubscription {
				cmd.PreferredSubscriptionID = &oldSub.ID
			}
			result, err := repo.Apply(ctx, cmd)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
				require.True(t, result.Applied)
				retry, err := repo.Apply(ctx, cmd)
				require.NoError(t, err)
				require.False(t, retry.Applied, "重复轮询不能重复扣费")
				if tc.scoped && tc.mode == service.APIKeyBillingModeAuto {
					changed := *cmd
					changed.RequestFingerprint = ""
					changed.SubscriptionScopeID = &newSub.ID
					_, err := repo.Apply(ctx, &changed)
					require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict, "同一任务不能变更订阅范围")
				}
			}
			var oldUsage, newUsage, balance float64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1`, oldSub.ID).Scan(&oldUsage))
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1`, newSub.ID).Scan(&newUsage))
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id = $1`, user.ID).Scan(&balance))
			require.InDelta(t, tc.wantOldUsage, oldUsage, 1e-8)
			require.InDelta(t, tc.wantNewUsage, newUsage, 1e-8)
			require.InDelta(t, tc.wantBalance, balance, 1e-8)
		})
	}
}
