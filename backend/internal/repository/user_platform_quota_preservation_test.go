//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/ent/userplatformquota"
	"github.com/stretchr/testify/require"
)

func TestPlatformQuotaSparseConfigurationPreservesPendingUsage(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	userID := mustCreateUserForQuota(t, client)
	repo := NewUserPlatformQuotaRepository(client)
	zero, limit := 0.0, 100.0
	require.NoError(t, repo.BulkInsertInitial(ctx, []UserPlatformQuotaRecord{
		{UserID: userID, Platform: "openai"},
		{UserID: userID, Platform: "minimax", DailyLimitUSD: &zero},
		{UserID: userID, Platform: "opencode_go", MonthlyLimitUSD: &limit},
	}))
	recs, err := repo.ListByUser(ctx, userID)
	require.NoError(t, err)
	require.Len(t, recs, 2)
	missing, err := repo.GetByUserPlatform(ctx, userID, "openai")
	require.NoError(t, err)
	require.Nil(t, missing)
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, repo.IncrementUsageWithReset(ctx, userID, "openai", 3, now))
	missing, err = repo.GetByUserPlatform(ctx, userID, "openai")
	require.NoError(t, err)
	require.Nil(t, missing)

	// 模拟 flusher 已读到快照，但管理员先清空限额；旧行必须继续承接最后一次快照。
	require.NoError(t, repo.UpsertForUser(ctx, userID, []UserPlatformQuotaRecord{{Platform: "openai"}}))
	snapshots := []UserPlatformQuotaSnapshot{
		{UserID: userID, Platform: "opencode_go", DailyUsageUSD: 2, WeeklyUsageUSD: 4, MonthlyUsageUSD: 6, DailyWindowStart: now, WeeklyWindowStart: now, MonthlyWindowStart: now},
		{UserID: userID, Platform: "openai", DailyUsageUSD: 9, DailyWindowStart: now, WeeklyWindowStart: now, MonthlyWindowStart: now},
		{UserID: -1, Platform: "minimax", DailyUsageUSD: 9, DailyWindowStart: now, WeeklyWindowStart: now, MonthlyWindowStart: now},
	}
	require.NoError(t, repo.BatchSnapshotUsage(ctx, snapshots, now))
	kept, err := repo.GetByUserPlatform(ctx, userID, "opencode_go")
	require.NoError(t, err)
	require.NotNil(t, kept)
	require.False(t, kept.HasAnyLimit())
	require.Equal(t, 6.0, kept.MonthlyUsageUSD)
	require.True(t, kept.MonthlyWindowStart.Equal(now))
	missing, err = repo.GetByUserPlatform(ctx, userID, "openai")
	require.NoError(t, err)
	require.Nil(t, missing)

	// 已通过结算守卫的在途 delta 不能因限额刚被取消而丢失；重配仍沿用保留的用量。
	require.NoError(t, repo.IncrementUsageWithReset(ctx, userID, "opencode_go", 2, now))
	require.NoError(t, repo.UpsertForUser(ctx, userID, []UserPlatformQuotaRecord{{Platform: "opencode_go", MonthlyLimitUSD: &limit}}))
	kept, err = repo.GetByUserPlatform(ctx, userID, "opencode_go")
	require.NoError(t, err)
	require.Equal(t, 8.0, kept.MonthlyUsageUSD)
	require.Equal(t, limit, *kept.MonthlyLimitUSD)

	// 真实软删记录不可由异步快照复活。
	_, err = client.UserPlatformQuota.Update().Where(userplatformquota.UserIDEQ(userID), userplatformquota.PlatformEQ("opencode_go")).SetDeletedAt(now).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, repo.BatchSnapshotUsage(ctx, snapshots, now.Add(time.Second)))
	missing, err = repo.GetByUserPlatform(ctx, userID, "opencode_go")
	require.NoError(t, err)
	require.Nil(t, missing)
}
