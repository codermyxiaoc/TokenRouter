//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 真实账务与管理员延期交错：旧窗口快照不能覆盖六十笔消费、重复计次或改变套餐。
func TestSubscriptionV028ExtensionWith60ConcurrentCharges(t *testing.T) {
	for _, operation := range []string{"extend", "set_validity", "bulk_extend"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			f := newResetCountFixture(t, true)
			seedElapsedResetCounts(t, f.sub.ID)
			expires := time.Now().Add(time.Hour).Truncate(time.Microsecond)
			_, err := integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET expires_at=$1, daily_usage_usd=9, weekly_usage_usd=9, monthly_usage_usd=9 WHERE id=$2`, expires, f.sub.ID)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE api_keys SET quota=100, rate_limit_5h=100, rate_limit_1d=100, rate_limit_7d=100 WHERE id=$1`, f.key.ID)
			require.NoError(t, err)
			old, err := f.repo.GetByID(ctx, f.sub.ID)
			require.NoError(t, err)
			commands := make([]service.UsageBillingCommand, 60)
			for i := range commands {
				commands[i] = *resetCountCommand(f, uuid.NewString(), .125)
				commands[i].APIKeyQuotaCost, commands[i].APIKeyRateLimitCost = .125, .125
			}

			// 单条延期停在首次写入前，强制六十笔结算先处理旧周期；批量已有用户锁，使用同时起跑避免人为锁死。
			paused := &focusPausedExpiryRepository{UserSubscriptionRepository: f.repo, entered: make(chan struct{}), resume: make(chan struct{})}
			svc := f.svc
			if operation != "bulk_extend" {
				svc = service.NewSubscriptionService(nil, paused, nil, integrationEntClient, nil)
			}
			finished := make(chan error, 1)
			startedAt := time.Now()
			go func() {
				var mutationErr error
				switch operation {
				case "extend":
					_, mutationErr = svc.ExtendSubscription(ctx, f.sub.ID, 65)
				case "set_validity":
					_, mutationErr = svc.SetSubscriptionValidityDays(ctx, f.sub.ID, 65)
				default:
					_, mutationErr = svc.BulkExtendSubscriptions(ctx, []int64{f.sub.ID}, 65)
				}
				finished <- mutationErr
			}()
			if operation != "bulk_extend" {
				select {
				case <-paused.entered:
				case <-ctx.Done():
					t.Fatal("延期未进入受控交错点")
				}
			}
			outcomes := billingFocusConcurrentApply(t, commands)
			if operation != "bulk_extend" {
				close(paused.resume)
			}
			require.NoError(t, <-finished)
			for i, outcome := range outcomes {
				require.NoError(t, outcome.err, "第 %d 笔结算", i)
				require.True(t, outcome.result.Applied)
				require.Equal(t, .125, outcome.result.SubscriptionAmountUSD)
				require.Zero(t, outcome.result.BalanceAmountUSD)
			}

			// 旧快照再并发维护与同请求重试均不得抹去或再次扣取新窗口消费。
			var wg sync.WaitGroup
			errors := make(chan error, 60)
			for range 60 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					copy := *old
					_, maintenanceErr := f.svc.EnsureWindowMaintenance(ctx, &copy)
					errors <- maintenanceErr
				}()
			}
			wg.Wait()
			close(errors)
			for maintenanceErr := range errors {
				require.NoError(t, maintenanceErr)
			}
			for _, outcome := range billingFocusConcurrentApply(t, commands) {
				require.NoError(t, outcome.err)
				require.False(t, outcome.result.Applied)
			}
			got, err := f.repo.GetByID(ctx, f.sub.ID)
			require.NoError(t, err)
			require.Equal(t, old.PlanID, got.PlanID)
			require.Equal(t, service.SubscriptionStatusActive, got.Status)
			require.Equal(t, []int64{37, 8, 5}, resetCountValues(got))
			if operation == "set_validity" {
				require.WithinDuration(t, startedAt.AddDate(0, 0, 65), got.ExpiresAt, 2*time.Second)
			} else {
				require.True(t, expires.AddDate(0, 0, 65).Equal(got.ExpiresAt))
			}
			for _, kind := range []string{"daily", "weekly", "monthly"} {
				billingFocusAssertDecimal(t, "7.5", fmt.Sprintf(`SELECT %s_usage_usd::text FROM user_subscriptions WHERE id=$1`, kind), got.ID)
			}
			billingFocusAssertDecimal(t, "100", `SELECT balance::text FROM users WHERE id=$1`, got.UserID)
			billingFocusAssertKey(t, f.key.ID, 60, "7.5", commands)
			t.Log("60 笔各 0.125：订阅三个窗口及 Key 均为 7.5，余额不变，重试未重复扣费，旧快照未抹消费")
		})
	}
}
