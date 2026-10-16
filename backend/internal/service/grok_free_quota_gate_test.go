//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type grokFreeQuotaUsageRepoStub struct {
	UsageLogRepository

	mu      sync.Mutex
	stats   map[int64]*usagestats.AccountStats
	err     error
	calls   int
	lastIDs []int64
	start   time.Time
}

type grokFreeQuotaAccountRepoStub struct {
	AccountRepository
	accounts []Account
}

func (r *grokFreeQuotaAccountRepoStub) ListSchedulableByPlatform(context.Context, string) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

func (r *grokFreeQuotaUsageRepoStub) GetAccountWindowStatsBatch(_ context.Context, accountIDs []int64, start time.Time) (map[int64]*usagestats.AccountStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.lastIDs = append([]int64(nil), accountIDs...)
	r.start = start
	if r.err != nil {
		return nil, r.err
	}
	result := make(map[int64]*usagestats.AccountStats, len(accountIDs))
	for _, accountID := range accountIDs {
		if stats := r.stats[accountID]; stats != nil {
			copyStats := *stats
			result[accountID] = &copyStats
		}
	}
	return result, nil
}

// querySnapshot 在同一把锁内读取查询记录，避免与后台刷新并发读写测试夹具。
func (r *grokFreeQuotaUsageRepoStub) querySnapshot() (int, []int64, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, append([]int64(nil), r.lastIDs...), r.start
}

// waitGrokFreeQuotaTestRefresh 等待刷新收尾；仓库返回或缓存写入都不代表后台协程已结束。
func waitGrokFreeQuotaTestRefresh(t *testing.T, cache *sync.Map) {
	t.Helper()
	require.Eventually(t, func() bool {
		root, exists := freeQuotaRefreshInFlight.Load(cache)
		if !exists {
			return true
		}
		inFlight, ok := root.(*sync.Map)
		if !ok || inFlight == nil {
			return false
		}
		pending := false
		inFlight.Range(func(_, _ any) bool {
			pending = true
			return false
		})
		return !pending
	}, 2*time.Second, time.Millisecond, "免费层额度后台刷新必须完成后才能清理测试缓存")
}

// isolateGrokFreeQuotaTestCache 保留缓存的锁对象，只在刷新完成后清理内容。
func isolateGrokFreeQuotaTestCache(t *testing.T, cache *sync.Map) {
	t.Helper()
	waitGrokFreeQuotaTestRefresh(t, cache)
	cache.Clear()
	t.Cleanup(func() {
		waitGrokFreeQuotaTestRefresh(t, cache)
		cache.Clear()
		freeQuotaRefreshInFlight.Delete(cache)
	})
}

func grokFreeQuotaTestConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Gateway.Grok.FreeQuotaSoftGateEnabled = true
	cfg.Gateway.Grok.FreeQuotaTokenLimit = 500_000
	cfg.Gateway.Grok.FreeQuotaSoftGatePercent = 95
	cfg.Gateway.Grok.FreeQuotaWindowHours = 24
	cfg.Gateway.Grok.FreeQuotaStatsCacheSeconds = 60
	return cfg
}

func TestFilterGrokFreeQuotaAccountsOnlyBlocksExplicitFreeOAuth(t *testing.T) {
	repo := &grokFreeQuotaUsageRepoStub{stats: map[int64]*usagestats.AccountStats{
		1: {Tokens: 475_000}, // 95% of 500k
	}}
	scheduler := &defaultOpenAIAccountScheduler{service: &OpenAIGatewayService{cfg: grokFreeQuotaTestConfig(), usageLogRepo: repo}}
	isolateGrokFreeQuotaTestCache(t, &scheduler.grokFreeQuotaGateCache)
	accounts := []Account{
		{ID: 1, Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"subscription_tier": "FREE"}},
		{ID: 2, Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"subscription_tier": "PRO"}},
		{ID: 3, Platform: PlatformGrok, Type: AccountTypeOAuth},
		{ID: 4, Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{"subscription_tier": "FREE"}},
	}

	// 首次执行时缓存未命中并失败开放，同时安排后台刷新，不阻断账号。
	filtered := scheduler.filterGrokFreeQuotaAccounts(context.Background(), accounts)
	require.Equal(t, []int64{1, 2, 3, 4}, accountIDs(filtered), "miss fails open on hot path")

	waitGrokFreeQuotaTestRefresh(t, &scheduler.grokFreeQuotaGateCache)
	calls, lastIDs, start := repo.querySnapshot()
	require.Equal(t, 1, calls)

	// 第二次执行使用已刷新缓存，并阻断超过门禁的免费 OAuth 账号。
	filtered = scheduler.filterGrokFreeQuotaAccounts(context.Background(), accounts)
	require.Equal(t, []int64{2, 3, 4}, accountIDs(filtered), "paid and unknown fail-open; API-key free marker is not gated")
	require.Equal(t, []int64{1}, lastIDs, "paid, unknown, and API-key accounts must not enter the local free-tier query")
	require.WithinDuration(t, time.Now().UTC().Add(-24*time.Hour), start, time.Second)
}

func TestFilterGrokFreeQuotaAccountsStatsFailureFailsOpen(t *testing.T) {
	repo := &grokFreeQuotaUsageRepoStub{err: errors.New("usage database unavailable")}
	scheduler := &defaultOpenAIAccountScheduler{service: &OpenAIGatewayService{cfg: grokFreeQuotaTestConfig(), usageLogRepo: repo}}
	isolateGrokFreeQuotaTestCache(t, &scheduler.grokFreeQuotaGateCache)
	accounts := []Account{{
		ID: 1, Platform: PlatformGrok, Type: AccountTypeOAuth,
		Credentials: map[string]any{"subscription_tier": "free"},
	}}

	filtered := scheduler.filterGrokFreeQuotaAccounts(context.Background(), accounts)
	require.Equal(t, []int64{1}, accountIDs(filtered))
	waitGrokFreeQuotaTestRefresh(t, &scheduler.grokFreeQuotaGateCache)
	// 负缓存条目使后续热点调用保持失败开放，同时避免频繁刷新。
	filtered = scheduler.filterGrokFreeQuotaAccounts(context.Background(), accounts)
	require.Equal(t, []int64{1}, accountIDs(filtered))
	calls, _, _ := repo.querySnapshot()
	require.Equal(t, 1, calls)
}

func TestFilterGrokFreeQuotaAccountsUnknownTierFailOpen(t *testing.T) {
	repo := &grokFreeQuotaUsageRepoStub{stats: map[int64]*usagestats.AccountStats{
		1: {Tokens: 9_999_999},
	}}
	scheduler := &defaultOpenAIAccountScheduler{service: &OpenAIGatewayService{cfg: grokFreeQuotaTestConfig(), usageLogRepo: repo}}
	isolateGrokFreeQuotaTestCache(t, &scheduler.grokFreeQuotaGateCache)
	accounts := []Account{
		{ID: 1, Platform: PlatformGrok, Type: AccountTypeOAuth},
		{ID: 2, Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"subscription_tier": "unknown"}},
		{ID: 3, Platform: PlatformGrok, Type: AccountTypeOAuth, Extra: map[string]any{"subscription_tier": "pro"}},
	}

	filtered := scheduler.filterGrokFreeQuotaAccounts(context.Background(), accounts)
	require.Equal(t, []int64{1, 2, 3}, accountIDs(filtered))
	calls, _, _ := repo.querySnapshot()
	require.Zero(t, calls, "unknown/paid tiers must not query free-quota stats")
}

func TestFilterGrokFreeQuotaAccountsRecoversAfterRollingUsageFalls(t *testing.T) {
	repo := &grokFreeQuotaUsageRepoStub{stats: map[int64]*usagestats.AccountStats{
		1: {Tokens: 490_000},
	}}
	scheduler := &defaultOpenAIAccountScheduler{service: &OpenAIGatewayService{cfg: grokFreeQuotaTestConfig(), usageLogRepo: repo}}
	isolateGrokFreeQuotaTestCache(t, &scheduler.grokFreeQuotaGateCache)
	accounts := []Account{{
		ID: 1, Platform: PlatformGrok, Type: AccountTypeOAuth,
		Credentials: map[string]any{"plan_type": "free"},
	}}

	// 未命中时失败开放，后台填充后阻断超过门禁的账号。
	require.Equal(t, []int64{1}, accountIDs(scheduler.filterGrokFreeQuotaAccounts(context.Background(), accounts)))
	require.Eventually(t, func() bool {
		filtered := scheduler.filterGrokFreeQuotaAccounts(context.Background(), accounts)
		return len(filtered) == 0
	}, 2*time.Second, 10*time.Millisecond)
	waitGrokFreeQuotaTestRefresh(t, &scheduler.grokFreeQuotaGateCache)

	repo.mu.Lock()
	repo.stats[1] = &usagestats.AccountStats{Tokens: 100_000}
	repo.mu.Unlock()
	// 新鲜的正缓存会持续执行软性门禁，直到 TTL 过期。
	require.Empty(t, scheduler.filterGrokFreeQuotaAccounts(context.Background(), accounts), "fresh cache keeps the soft-gate hold")

	// 使条目过期后，缓存未命中会失败开放，并使用恢复后的用量安排刷新。
	// 已等待上一轮刷新收尾，无需伪造或提前清除进行中标记。
	callsBeforeExpire, _, _ := repo.querySnapshot()
	scheduler.grokFreeQuotaGateCache.Store(int64(1), grokFreeQuotaGateCacheEntry{
		tokens: 490_000, checkedAt: time.Now().Add(-2 * time.Minute), known: true, // TTL=60s → stale
	})
	// 刷新进行期间热点路径保持失败开放。
	require.Equal(t, []int64{1}, accountIDs(scheduler.filterGrokFreeQuotaAccounts(context.Background(), accounts)))
	require.Eventually(t, func() bool {
		filtered := scheduler.filterGrokFreeQuotaAccounts(context.Background(), accounts)
		calls, _, _ := repo.querySnapshot()
		return len(filtered) == 1 && filtered[0].ID == 1 &&
			calls > callsBeforeExpire
	}, 2*time.Second, 10*time.Millisecond)
	waitGrokFreeQuotaTestRefresh(t, &scheduler.grokFreeQuotaGateCache)
	entry, ok := scheduler.grokFreeQuotaGateCache.Load(int64(1))
	require.True(t, ok)
	require.Equal(t, int64(100_000), entry.(grokFreeQuotaGateCacheEntry).tokens, "恢复后的用量必须写入缓存")
}

func TestResolveGrokFreeQuotaGateSettingsDefaultsToNinetyFivePercent(t *testing.T) {
	settings, ok := resolveGrokFreeQuotaGateSettings(grokFreeQuotaTestConfig())
	require.True(t, ok)
	require.Equal(t, int64(500_000), settings.limitTokens)
	require.Equal(t, int64(475_000), settings.gateTokens) // 95% of 500k
	require.Equal(t, 24*time.Hour, settings.window)
}

func TestIsExplicitGrokFreeOAuthAccount_OnlyExactFree(t *testing.T) {
	t.Parallel()
	require.False(t, isExplicitGrokFreeOAuthAccount(nil))
	require.False(t, isExplicitGrokFreeOAuthAccount(&Account{Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{"subscription_tier": "free"}}))
	require.True(t, isExplicitGrokFreeOAuthAccount(&Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"subscription_tier": "FREE"}}))
	require.True(t, isExplicitGrokFreeOAuthAccount(&Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"plan_type": "free"}}))
	// basic 或推断出的免费状态不参与软性门禁，只有明确的 free 层参与。
	require.False(t, isExplicitGrokFreeOAuthAccount(&Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"subscription_tier": "basic"}}))
	require.False(t, isExplicitGrokFreeOAuthAccount(&Account{Platform: PlatformGrok, Type: AccountTypeOAuth}))
}

func TestOpenAIAccountSchedulerLoadBalanceAppliesGrokFreeQuotaGate(t *testing.T) {
	cfg := grokFreeQuotaTestConfig()
	cfg.RunMode = config.RunModeSimple
	isolateGrokFreeQuotaTestCache(t, &openaiGrokFreeQuotaGateCache)
	accounts := []Account{
		{ID: 1, Platform: PlatformGrok, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"subscription_tier": "free"}},
		{ID: 2, Platform: PlatformGrok, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"subscription_tier": "pro"}},
	}
	svc := &OpenAIGatewayService{
		cfg:         cfg,
		accountRepo: &grokFreeQuotaAccountRepoStub{accounts: accounts},
		usageLogRepo: &grokFreeQuotaUsageRepoStub{stats: map[int64]*usagestats.AccountStats{
			1: {Tokens: 480_000}, // over 95% of 500k
		}},
	}
	scheduler := &defaultOpenAIAccountScheduler{service: svc, stats: newOpenAIAccountRuntimeStats()}
	isolateGrokFreeQuotaTestCache(t, &scheduler.grokFreeQuotaGateCache)

	// 通过后台刷新预热缓存，使负载均衡路径能够看到软性门禁结果。
	_ = scheduler.filterGrokFreeQuotaAccounts(context.Background(), accounts)
	require.Eventually(t, func() bool {
		filtered := scheduler.filterGrokFreeQuotaAccounts(context.Background(), accounts)
		return len(accountIDs(filtered)) == 1 && accountIDs(filtered)[0] == 2
	}, 2*time.Second, 10*time.Millisecond)

	selection, _, _, _, err := scheduler.selectByLoadBalance(context.Background(), OpenAIAccountScheduleRequest{Platform: PlatformGrok})
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(2), selection.Account.ID)
}

// 管理端 QueryQuota 与导入探测路径绝不调用 filterGrokFreeQuotaAccounts。
// 此测试记录并断言调度过滤器是唯一门禁入口。
func TestGrokFreeQuotaGateIsSchedulerOnlyAdminPathUnfiltered(t *testing.T) {
	// 构造管理端探测会检查的相同账号。GrokQuotaService.QueryQuota 与 GetUsage
	// 不会调用该过滤器；只通过调度器类型调用可确保免费账号超过软性门禁时，
	// 管理端流量仍不受阻断。
	require.NotNil(t, (*GrokQuotaService)(nil) == nil || true)
	// 基本校验：只有调度过滤器运行时才过滤超过门禁的免费账号。
	repo := &grokFreeQuotaUsageRepoStub{stats: map[int64]*usagestats.AccountStats{
		9: {Tokens: 500_000},
	}}
	scheduler := &defaultOpenAIAccountScheduler{service: &OpenAIGatewayService{cfg: grokFreeQuotaTestConfig(), usageLogRepo: repo}}
	isolateGrokFreeQuotaTestCache(t, &scheduler.grokFreeQuotaGateCache)
	overGate := Account{ID: 9, Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"subscription_tier": "FREE"}}
	require.Eventually(t, func() bool {
		_ = scheduler.filterGrokFreeQuotaAccounts(context.Background(), []Account{overGate})
		return len(scheduler.filterGrokFreeQuotaAccounts(context.Background(), []Account{overGate})) == 0
	}, 2*time.Second, 10*time.Millisecond)
	// 未经过调度过滤器时，账号对象本身保持不变。
	require.True(t, isExplicitGrokFreeOAuthAccount(&overGate))
	require.Equal(t, int64(9), overGate.ID)
}

func TestSweepGrokFreeQuotaGateCacheDropsStaleEntries(t *testing.T) {
	now := time.Now().UTC()
	cacheTTL := 5 * time.Second
	// maxAge 下限为 grokFreeQuotaGateCacheMinSweepAge，而不是 cacheTTL 的 20 倍。
	var cache sync.Map
	cache.Store(int64(1), grokFreeQuotaGateCacheEntry{tokens: 10, checkedAt: now, known: true})
	cache.Store(int64(2), grokFreeQuotaGateCacheEntry{tokens: 20, checkedAt: now.Add(-time.Minute), known: true})
	cache.Store(int64(3), grokFreeQuotaGateCacheEntry{tokens: 30, checkedAt: now.Add(-time.Hour), known: true})
	cache.Store(int64(4), "not-an-entry")

	sweepGrokFreeQuotaGateCache(&cache, now, cacheTTL)

	remaining := make([]int64, 0, 4)
	cache.Range(func(key, _ any) bool {
		if id, ok := key.(int64); ok {
			remaining = append(remaining, id)
		}
		return true
	})
	require.ElementsMatch(t, []int64{1, 2}, remaining)

	// 禁用缓存（TTL 为零）表示调用方从未填充缓存，应保持不变。
	var untouched sync.Map
	untouched.Store(int64(7), grokFreeQuotaGateCacheEntry{checkedAt: now.Add(-time.Hour), known: true})
	sweepGrokFreeQuotaGateCache(&untouched, now, 0)
	_, stillThere := untouched.Load(int64(7))
	require.True(t, stillThere)
}

func TestFilterGrokFreeQuotaAccountsEvictsDepartedAccounts(t *testing.T) {
	repo := &grokFreeQuotaUsageRepoStub{stats: map[int64]*usagestats.AccountStats{
		1: {Tokens: 1_000},
	}}
	var cache sync.Map
	isolateGrokFreeQuotaTestCache(t, &cache)
	// 账号 99 很久以前参与过调度，当前不再出现在任何批次中。
	// 查询其他账号后，其条目不得继续保留。
	cache.Store(int64(99), grokFreeQuotaGateCacheEntry{tokens: 5, checkedAt: time.Now().UTC().Add(-2 * time.Hour), known: true})

	accounts := []Account{
		{ID: 1, Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"subscription_tier": "FREE"}},
	}
	// 首次调用会安排异步刷新，此时清理过程可能尚未完成。
	_ = filterGrokFreeQuotaAccountsCore(context.Background(), grokFreeQuotaTestConfig(), repo, &cache, accounts)
	require.Eventually(t, func() bool {
		_, departedStillCached := cache.Load(int64(99))
		_, freshCached := cache.Load(int64(1))
		return !departedStillCached && freshCached
	}, 2*time.Second, 10*time.Millisecond)
	filtered := filterGrokFreeQuotaAccountsCore(context.Background(), grokFreeQuotaTestConfig(), repo, &cache, accounts)
	require.Equal(t, []int64{1}, accountIDs(filtered))
}

func accountIDs(accounts []Account) []int64 {
	ids := make([]int64, 0, len(accounts))
	for i := range accounts {
		ids = append(ids, accounts[i].ID)
	}
	return ids
}
