//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVideoPlatformQuotaUsesDatabaseAndDoesNotMirror(t *testing.T) {
	limit := 1.0
	repo := &fakeQuotaRepo{rec: &UserPlatformQuotaRecord{UserID: 1, Platform: PlatformVideo,
		DailyLimitUSD: &limit, DailyUsageUSD: 1, DailyWindowStart: currentDayStart()}}
	// 即便缓存显示仍有额度，Video 也必须采用已提交的数据库账本。
	cache := &fakeFullCache{entry: &UserPlatformQuotaCacheEntry{SchemaVersion: UserPlatformQuotaCacheSchemaV1,
		DailyLimitUSD: &limit, DailyUsageUSD: 0, DailyWindowStart: currentDayStart()}}
	svc := newServiceForPreflight(t, repo, cache)
	require.ErrorIs(t, svc.checkUserPlatformQuotaEligibility(context.Background(), 1, PlatformVideo), ErrUserPlatformDailyQuotaExhausted)
	require.Zero(t, cache.setCalls)
	incrementCache := &fakeIncrCache{}
	svc.cache = incrementCache
	svc.IncrementUserPlatformQuotaUsage(1, PlatformVideo, .5)
	require.Empty(t, incrementCache.calls)
	require.True(t, IsAllowedQuotaPlatform(PlatformVideo))
}

func TestVideoQuotaFlusherCannotOverwriteDatabaseLedger(t *testing.T) {
	cache := &mockQuotaDirtyCache{
		popSequence: [][]UserPlatformQuotaKey{{{UserID: 1, Platform: PlatformVideo}, {UserID: 2, Platform: PlatformOpenAI}}},
		getEntries:  []*UserPlatformQuotaCacheEntry{makeEntry(999, 999, 999), makeEntry(1, 2, 3)},
	}
	writer := &mockQuotaSnapshotWriter{}
	flusher := newTestFlusher(cache, writer)
	flusher.flushOneBatch(context.Background())
	require.Len(t, writer.receivedSnaps, 1)
	require.Equal(t, PlatformOpenAI, writer.receivedSnaps[0].Platform)
}
