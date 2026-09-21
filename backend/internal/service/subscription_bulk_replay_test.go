//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 成功响应丢失后的同键确认只能读结果，不能重复延期，也不能抹掉首次重置后新产生的消费。
func TestBulkSubscriptionsSuccessfulReplayPreservesExpiryAndNewUsage(t *testing.T) {
	for _, action := range []string{"extend", "reset"} {
		t.Run(action, func(t *testing.T) {
			svc, repo, ids, _ := newBulkSubscriptionFixture(t)
			ctx := context.Background()
			before, err := repo.GetByID(ctx, ids[0])
			require.NoError(t, err)
			coordinator := NewIdempotencyCoordinator(newInMemoryIdempotencyRepo(), DefaultIdempotencyConfig())
			opts := IdempotencyExecuteOptions{
				Scope: "admin.subscriptions.bulk_" + action, ActorScope: "admin:1", Method: "POST", Route: "/bulk/" + action,
				IdempotencyKey: "confirmed-bulk", RequireKey: true, Payload: map[string]any{"subscription_ids": ids, "days": 3},
			}
			calls := 0
			execute := func(ctx context.Context) (any, error) {
				calls++
				if action == "extend" {
					return svc.BulkExtendSubscriptions(ctx, ids, 3)
				}
				return svc.BulkResetSubscriptionQuota(ctx, ids, true, false, false)
			}
			first, err := coordinator.Execute(ctx, opts, execute)
			require.NoError(t, err)
			require.False(t, first.Replayed)
			afterFirst, err := repo.GetByID(ctx, ids[0])
			require.NoError(t, err)
			if action == "extend" {
				require.True(t, before.ExpiresAt.AddDate(0, 0, 3).Equal(afterFirst.ExpiresAt))
			} else {
				require.Zero(t, afterFirst.DailyUsageUSD)
				require.True(t, before.ExpiresAt.Equal(afterFirst.ExpiresAt))
				// 首次重置后已经产生的新消费，不能因为确认旧请求而再次清零。
				require.NoError(t, repo.client.UserSubscription.UpdateOneID(ids[0]).SetDailyUsageUsd(1.25).Exec(ctx))
			}
			replay, err := coordinator.Execute(ctx, opts, execute)
			require.NoError(t, err)
			require.True(t, replay.Replayed)
			require.Equal(t, 1, calls)
			afterReplay, err := repo.GetByID(ctx, ids[0])
			require.NoError(t, err)
			require.True(t, afterFirst.ExpiresAt.Equal(afterReplay.ExpiresAt))
			require.Equal(t, before.WeeklyUsageUSD, afterReplay.WeeklyUsageUSD)
			require.Equal(t, before.MonthlyUsageUSD, afterReplay.MonthlyUsageUSD)
			if action == "reset" {
				require.Equal(t, 1.25, afterReplay.DailyUsageUSD)
			}
		})
	}
}
