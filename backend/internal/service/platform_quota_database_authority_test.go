//go:build unit

package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// quotaAuthorityCache 记录所有额度缓存访问，防止新路径偷偷回退旧快照。
type quotaAuthorityCache struct {
	BillingCache
	entry *UserPlatformQuotaCacheEntry
	calls atomic.Int64
}

func (c *quotaAuthorityCache) GetUserPlatformQuotaCache(context.Context, int64, string) (*UserPlatformQuotaCacheEntry, bool, error) {
	c.calls.Add(1)
	return c.entry, true, nil
}

func (c *quotaAuthorityCache) SetUserPlatformQuotaCache(context.Context, int64, string, *UserPlatformQuotaCacheEntry, time.Duration) error {
	c.calls.Add(1)
	return nil
}

// 每个平台都应忽略两种相反的陈旧缓存：旧缓存放行不能漏拦截，旧缓存超限不能误拦截。
func TestPlatformQuotaDatabaseAuthorityAllPlatformsIgnoreStaleCache(t *testing.T) {
	for _, platform := range AllowedQuotaPlatforms {
		t.Run(platform, func(t *testing.T) {
			limit := 1.0
			repo := &fakeQuotaRepo{rec: &UserPlatformQuotaRecord{DailyLimitUSD: &limit, DailyUsageUSD: 1, DailyWindowStart: currentDayStart()}}
			cache := &quotaAuthorityCache{entry: &UserPlatformQuotaCacheEntry{SchemaVersion: UserPlatformQuotaCacheSchemaV1}}
			svc := newServiceForPreflight(t, repo, cache)
			require.ErrorIs(t, svc.checkUserPlatformQuotaEligibility(context.Background(), 10, platform), ErrUserPlatformDailyQuotaExhausted)
			require.True(t, svc.HasUserPlatformQuotaLimit(context.Background(), 10, platform))
			cache.entry = &UserPlatformQuotaCacheEntry{DailyLimitUSD: &limit, DailyUsageUSD: 99, DailyWindowStart: currentDayStart(), SchemaVersion: UserPlatformQuotaCacheSchemaV1}
			repo.rec = &UserPlatformQuotaRecord{DailyLimitUSD: &limit, DailyUsageUSD: .5, DailyWindowStart: currentDayStart()}
			require.NoError(t, svc.checkUserPlatformQuotaEligibility(context.Background(), 10, platform))
			require.Zero(t, cache.calls.Load())
		})
	}
}

// 两个实例都按新查询结果判断，管理员修改限额或重置无需等待 Redis 失效。
func TestPlatformQuotaDatabaseAuthorityInstancesObserveLimitChanges(t *testing.T) {
	limit := 1.0
	repo := &fakeQuotaRepo{rec: &UserPlatformQuotaRecord{DailyLimitUSD: &limit, DailyUsageUSD: 1, DailyWindowStart: currentDayStart()}}
	first := newServiceForPreflight(t, repo, &quotaAuthorityCache{})
	second := newServiceForPreflight(t, repo, &quotaAuthorityCache{})
	for _, svc := range []*BillingCacheService{first, second} {
		require.ErrorIs(t, svc.checkUserPlatformQuotaEligibility(context.Background(), 1, PlatformOpenAI), ErrUserPlatformDailyQuotaExhausted)
	}
	// 模拟已提交的管理员重置。
	repo.rec = &UserPlatformQuotaRecord{DailyLimitUSD: &limit, DailyUsageUSD: 0, DailyWindowStart: currentDayStart()}
	for _, svc := range []*BillingCacheService{first, second} {
		require.NoError(t, svc.checkUserPlatformQuotaEligibility(context.Background(), 1, PlatformOpenAI))
	}
	zero := 0.0
	repo.rec = &UserPlatformQuotaRecord{WeeklyLimitUSD: &zero}
	for _, svc := range []*BillingCacheService{first, second} {
		require.ErrorIs(t, svc.checkUserPlatformQuotaEligibility(context.Background(), 1, PlatformOpenAI), ErrUserPlatformWeeklyQuotaExhausted)
	}
	repo.rec = nil
	for _, svc := range []*BillingCacheService{first, second} {
		require.NoError(t, svc.checkUserPlatformQuotaEligibility(context.Background(), 1, PlatformOpenAI))
	}
}

type blockingQuotaAuthorityRepo struct {
	UserPlatformQuotaRepository
	started chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int64
	rec     *UserPlatformQuotaRecord
}

func (r *blockingQuotaAuthorityRepo) GetByUserPlatform(ctx context.Context, _ int64, _ string) (*UserPlatformQuotaRecord, error) {
	r.calls.Add(1)
	r.once.Do(func() { close(r.started) })
	select {
	case <-r.release:
		return r.rec, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// 首个调用者取消不能取消合并查询；后续请求仍须读取数据库而不是持久缓存结果。
func TestPlatformQuotaDatabaseAuthorityCancelledCallerDoesNotPoisonQuery(t *testing.T) {
	zero := 0.0
	repo := &blockingQuotaAuthorityRepo{started: make(chan struct{}), release: make(chan struct{}), rec: &UserPlatformQuotaRecord{MonthlyLimitUSD: &zero}}
	svc := newServiceForPreflight(t, repo, &quotaAuthorityCache{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() { first <- svc.checkUserPlatformQuotaEligibility(ctx, 1, PlatformOpenAI) }()
	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("数据库查询未开始")
	}
	cancel()
	require.NoError(t, <-first)
	second := make(chan error, 1)
	go func() { second <- svc.checkUserPlatformQuotaEligibility(context.Background(), 1, PlatformOpenAI) }()
	close(repo.release)
	require.ErrorIs(t, <-second, ErrUserPlatformMonthlyQuotaExhausted)
	before := repo.calls.Load()
	require.ErrorIs(t, svc.checkUserPlatformQuotaEligibility(context.Background(), 1, PlatformOpenAI), ErrUserPlatformMonthlyQuotaExhausted)
	require.Greater(t, repo.calls.Load(), before)
}
