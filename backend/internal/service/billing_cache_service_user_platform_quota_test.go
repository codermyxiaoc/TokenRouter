//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// fakeIncrCache 仅记录 IncrUserPlatformQuotaUsageCache 被调用的参数。
type fakeIncrCache struct {
	BillingCache
	calls []incrCall
}

type incrCall struct {
	userID    int64
	platform  string
	cost      float64
	ttl       time.Duration
	markDirty bool
}

func (f *fakeIncrCache) IncrUserPlatformQuotaUsageCache(ctx context.Context, userID int64, platform string, cost float64, ttl time.Duration, markDirty bool) error {
	f.calls = append(f.calls, incrCall{userID, platform, cost, ttl, markDirty})
	return nil
}

// 旧入口即使配置开启也不能再次增加缓存或脏集。
func TestIncrementUserPlatformQuotaUsage_LegacyEntryDoesNotWrite(t *testing.T) {
	fake := &fakeIncrCache{}
	svc := &BillingCacheService{cache: fake, cfg: &config.Config{}}
	svc.cfg.Database.UserPlatformQuotaFlusherEnabled = true
	for _, platform := range AllowedQuotaPlatforms {
		svc.IncrementUserPlatformQuotaUsage(101, platform, .5)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("旧入口仍写入平台统计: %v", fake.calls)
	}
}

// ── T6 tests: checkUserPlatformQuotaEligibility ──────────────────────────────

// fakeQuotaRepo 实现 UserPlatformQuotaRepository 最小子集
type fakeQuotaRepo struct {
	rec *UserPlatformQuotaRecord
	err error
}

func (f *fakeQuotaRepo) GetByUserPlatform(_ context.Context, _ int64, _ string) (*UserPlatformQuotaRecord, error) {
	return f.rec, f.err
}

func (f *fakeQuotaRepo) BulkInsertInitial(_ context.Context, _ []UserPlatformQuotaRecord) error {
	return nil
}

func (f *fakeQuotaRepo) IncrementUsageWithReset(_ context.Context, _ int64, _ string, _ float64, _ time.Time) error {
	return nil
}

func (f *fakeQuotaRepo) ListByUser(_ context.Context, _ int64) ([]UserPlatformQuotaRecord, error) {
	return nil, nil
}

func (f *fakeQuotaRepo) UpsertForUser(_ context.Context, _ int64, _ []UserPlatformQuotaRecord) error {
	return nil
}

func (f *fakeQuotaRepo) ResetExpiredWindow(_ context.Context, _ int64, _ string, _ string, _ time.Time) error {
	return nil
}

func (f *fakeQuotaRepo) BatchSnapshotUsage(_ context.Context, _ []UserPlatformQuotaSnapshot, _ time.Time) error {
	return nil
}

// fakeFullCache 同时支持 Get + Set + Incr + Delete + Pop/Readd/BatchGet（脏集读写）。
// mu 保护 entry 和 deleteCalls，防止异步 goroutine 与主 goroutine 之间的 data race。
type fakeFullCache struct {
	BillingCache
	mu          sync.Mutex
	entry       *UserPlatformQuotaCacheEntry
	deleteCalls int
	setCalls    int           // SetUserPlatformQuotaCache 调用次数
	lastSetTTL  time.Duration // 最近一次 Set 的 ttl
	getErr      error         // 非 nil 时 Get 先返回 (nil,false,getErr)
	setErr      error         // 非 nil 时 Set 返回该 err(setCalls 仍+1)
	// dirty 模拟脏集，供 flusher 测试使用。
	dirty map[UserPlatformQuotaKey]struct{}
}

// getDeleteCalls 线程安全地读取 deleteCalls。
func (f *fakeFullCache) getDeleteCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deleteCalls
}

// getEntry 线程安全地读取 entry。
func (f *fakeFullCache) getEntry() *UserPlatformQuotaCacheEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.entry
}

// getSetCalls 线程安全地读取 setCalls。
func (f *fakeFullCache) getSetCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.setCalls
}

// getLastSetTTL 线程安全地读取 lastSetTTL。
func (f *fakeFullCache) getLastSetTTL() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastSetTTL
}

func (f *fakeFullCache) GetUserPlatformQuotaCache(_ context.Context, _ int64, _ string) (*UserPlatformQuotaCacheEntry, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	if f.entry == nil {
		return nil, false, nil
	}
	return f.entry, true, nil
}

func (f *fakeFullCache) SetUserPlatformQuotaCache(_ context.Context, _ int64, _ string, e *UserPlatformQuotaCacheEntry, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCalls++
	if f.setErr != nil {
		return f.setErr
	}
	f.entry = e
	f.lastSetTTL = ttl
	return nil
}

func (f *fakeFullCache) DeleteUserPlatformQuotaCache(_ context.Context, _ int64, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	f.entry = nil
	return nil
}

func (f *fakeFullCache) PopDirtyUserPlatformQuotaKeys(_ context.Context, n int) ([]UserPlatformQuotaKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.dirty) == 0 {
		return nil, nil
	}
	keys := make([]UserPlatformQuotaKey, 0, n)
	for k := range f.dirty {
		if len(keys) >= n {
			break
		}
		keys = append(keys, k)
		delete(f.dirty, k)
	}
	return keys, nil
}

func (f *fakeFullCache) ReaddDirtyUserPlatformQuotaKeys(_ context.Context, keys []UserPlatformQuotaKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dirty == nil {
		f.dirty = make(map[UserPlatformQuotaKey]struct{})
	}
	for _, k := range keys {
		f.dirty[k] = struct{}{}
	}
	return nil
}

// BatchGetUserPlatformQuotaCache 对每个 key 返回 f.entry（MISS → nil），
// 保持与输入 keys 顺序/长度对齐。注意此处所有 key 共享同一个 entry，
// 仅用于测试场景。
func (f *fakeFullCache) BatchGetUserPlatformQuotaCache(_ context.Context, keys []UserPlatformQuotaKey) ([]*UserPlatformQuotaCacheEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	results := make([]*UserPlatformQuotaCacheEntry, len(keys))
	for i := range keys {
		results[i] = f.entry
	}
	return results, nil
}

func newServiceForPreflight(t *testing.T, repo UserPlatformQuotaRepository, cache BillingCache) *BillingCacheService {
	t.Helper()
	cfg := &config.Config{}
	cfg.Billing.UserPlatformQuotaCacheTTLSeconds = 60
	return &BillingCacheService{
		cache:                 cache,
		cfg:                   cfg,
		userPlatformQuotaRepo: repo,
	}
}

// currentDayStart 返回全局时区当天 0 点（与生产 timezone.StartOfDay 同口径，确保窗口有效）。
func currentDayStart() *time.Time {
	s := timezone.StartOfDay(time.Now())
	return &s
}

func TestCheckUserPlatformQuotaEligibility_AllowsWhenUnderLimit(t *testing.T) {
	daily := 10.0
	repo := &fakeQuotaRepo{rec: &UserPlatformQuotaRecord{
		UserID: 1, Platform: "anthropic", DailyLimitUSD: &daily,
	}}
	cache := &fakeFullCache{entry: &UserPlatformQuotaCacheEntry{
		DailyUsageUSD:    4.5,
		DailyLimitUSD:    &daily,
		DailyWindowStart: currentDayStart(),
		SchemaVersion:    UserPlatformQuotaCacheSchemaV1,
	}}
	s := newServiceForPreflight(t, repo, cache)
	if err := s.checkUserPlatformQuotaEligibility(context.Background(), 1, "anthropic"); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestCheckUserPlatformQuotaEligibility_DailyExhausted(t *testing.T) {
	daily := 5.0
	repo := &fakeQuotaRepo{rec: &UserPlatformQuotaRecord{
		UserID: 1, Platform: "anthropic", DailyLimitUSD: &daily, DailyUsageUSD: 5, DailyWindowStart: currentDayStart(),
	}}
	cache := &fakeFullCache{entry: &UserPlatformQuotaCacheEntry{
		DailyUsageUSD:    5.0,
		DailyLimitUSD:    &daily,
		DailyWindowStart: currentDayStart(),
		SchemaVersion:    UserPlatformQuotaCacheSchemaV1,
	}}
	s := newServiceForPreflight(t, repo, cache)
	err := s.checkUserPlatformQuotaEligibility(context.Background(), 1, "anthropic")
	if !errors.Is(err, ErrUserPlatformDailyQuotaExhausted) {
		t.Errorf("expected ErrUserPlatformDailyQuotaExhausted, got %v", err)
	}
}

func TestCheckUserPlatformQuotaEligibility_NilLimitMeansUnlimited(t *testing.T) {
	repo := &fakeQuotaRepo{rec: &UserPlatformQuotaRecord{
		UserID: 1, Platform: "anthropic",
	}}
	cache := &fakeFullCache{entry: &UserPlatformQuotaCacheEntry{
		DailyUsageUSD:    999,
		DailyWindowStart: currentDayStart(),
		SchemaVersion:    UserPlatformQuotaCacheSchemaV1,
		// DailyLimitUSD nil → 无限额
	}}
	s := newServiceForPreflight(t, repo, cache)
	if err := s.checkUserPlatformQuotaEligibility(context.Background(), 1, "anthropic"); err != nil {
		t.Errorf("nil limits should be unlimited, got %v", err)
	}
}

func TestCheckUserPlatformQuotaEligibility_ZeroLimitImmediateBlock(t *testing.T) {
	zero := 0.0
	repo := &fakeQuotaRepo{rec: &UserPlatformQuotaRecord{
		UserID: 1, Platform: "anthropic", DailyLimitUSD: &zero,
	}}
	cache := &fakeFullCache{entry: &UserPlatformQuotaCacheEntry{
		DailyUsageUSD:    0,
		DailyLimitUSD:    &zero,
		DailyWindowStart: currentDayStart(),
		SchemaVersion:    UserPlatformQuotaCacheSchemaV1,
	}}
	s := newServiceForPreflight(t, repo, cache)
	err := s.checkUserPlatformQuotaEligibility(context.Background(), 1, "anthropic")
	if !errors.Is(err, ErrUserPlatformDailyQuotaExhausted) {
		t.Errorf("expected daily exhausted for limit=0, got %v", err)
	}
}

func TestCheckUserPlatformQuotaEligibility_NoRecordMeansUnlimited(t *testing.T) {
	repo := &fakeQuotaRepo{rec: nil}
	cache := &fakeFullCache{}
	s := newServiceForPreflight(t, repo, cache)
	if err := s.checkUserPlatformQuotaEligibility(context.Background(), 1, "anthropic"); err != nil {
		t.Errorf("no record = unlimited, got %v", err)
	}
}

// 旧版缓存不再参与判断，数据库中的有效窗口与额度必须直接生效。
func TestCheckUserPlatformQuotaEligibility_OldSchemaCacheMissTriggersDB(t *testing.T) {
	daily := 5.0
	dayStart := currentDayStart()
	repo := &fakeQuotaRepo{rec: &UserPlatformQuotaRecord{
		UserID: 1, Platform: "anthropic", DailyLimitUSD: &daily, DailyUsageUSD: 6.0,
		DailyWindowStart: dayStart,
	}}
	// SchemaVersion=0（旧 entry），应走 DB 路径
	cache := &fakeFullCache{entry: &UserPlatformQuotaCacheEntry{DailyUsageUSD: 1.0}}
	s := newServiceForPreflight(t, repo, cache)
	err := s.checkUserPlatformQuotaEligibility(context.Background(), 1, "anthropic")
	if !errors.Is(err, ErrUserPlatformDailyQuotaExhausted) {
		t.Errorf("旧版 entry 应走 DB 路径并报 daily exhausted, got %v", err)
	}
}

// 数据库旧窗口的历史用量只在当前判断中归零。
func TestCheckUserPlatformQuotaEligibility_WindowExpiredInDatabase(t *testing.T) {
	daily := 5.0
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC) // 远古窗口起始，肯定已过期
	repo := &fakeQuotaRepo{rec: &UserPlatformQuotaRecord{
		UserID: 1, Platform: "anthropic", DailyLimitUSD: &daily, DailyUsageUSD: 10, DailyWindowStart: &past,
	}}
	cache := &fakeFullCache{entry: &UserPlatformQuotaCacheEntry{
		DailyUsageUSD:    10.0, // 超限，但窗口已过期
		DailyLimitUSD:    &daily,
		DailyWindowStart: &past,
		SchemaVersion:    UserPlatformQuotaCacheSchemaV1,
	}}
	s := newServiceForPreflight(t, repo, cache)
	err := s.checkUserPlatformQuotaEligibility(context.Background(), 1, "anthropic")
	if err != nil {
		t.Errorf("过期窗口应归零放行, got %v", err)
	}
}

// 过期窗口只影响当前判断，不能把旧 Redis 内容刷新后重新当作权威。
func TestCheckUserPlatformQuotaEligibility_ExpiredDatabaseWindowDoesNotRefreshCache(t *testing.T) {
	daily := 5.0
	past := time.Now().AddDate(0, 0, -2)
	repo := &fakeQuotaRepo{rec: &UserPlatformQuotaRecord{DailyLimitUSD: &daily, DailyUsageUSD: 10, DailyWindowStart: &past}}
	cache := &fakeFullCache{entry: &UserPlatformQuotaCacheEntry{DailyLimitUSD: &daily, DailyUsageUSD: 999, DailyWindowStart: currentDayStart(), SchemaVersion: UserPlatformQuotaCacheSchemaV1}}
	svc := newServiceForPreflight(t, repo, cache)
	if err := svc.checkUserPlatformQuotaEligibility(context.Background(), 1, "openai"); err != nil {
		t.Fatal(err)
	}
	if cache.getSetCalls() != 0 || cache.getEntry().DailyUsageUSD != 999 {
		t.Fatal("不应读取后刷新旧缓存")
	}
}

// ── C-NEW-1: monthlyQuotaWindowExpired 30 天滚动测试 ─────────────────────────

func TestMonthlyQuotaWindowExpired_NilStart(t *testing.T) {
	if !monthlyQuotaWindowExpired(nil, time.Now().UTC()) {
		t.Error("nil start should be considered expired")
	}
}

func TestMonthlyQuotaWindowExpired_Expired(t *testing.T) {
	start := time.Now().UTC().Add(-30 * 24 * time.Hour)
	if !monthlyQuotaWindowExpired(&start, time.Now().UTC()) {
		t.Error("start exactly 30 days ago should be expired")
	}
}

func TestMonthlyQuotaWindowExpired_Active(t *testing.T) {
	start := time.Now().UTC().Add(-29 * 24 * time.Hour)
	if monthlyQuotaWindowExpired(&start, time.Now().UTC()) {
		t.Error("start 29 days ago should NOT be expired")
	}
}

// TestMonthlyQuotaWindowExpired_CrossMonthBoundary 验证跨自然月时 30 天未满不视为过期。
func TestMonthlyQuotaWindowExpired_CrossMonthBoundary(t *testing.T) {
	// 窗口起始 4 月 20 日；5 月 1 日只过了 11 天，不足 30 天
	start := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if monthlyQuotaWindowExpired(&start, now) {
		t.Error("11 days into window should NOT be expired (30-day rolling, not calendar month)")
	}
}

// TestNextMonthlyResetFrom 验证 30 天滚动重置时间计算。
func TestNextMonthlyResetFrom_WithStart(t *testing.T) {
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	want := start.Add(30 * 24 * time.Hour)
	now := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	got := nextMonthlyResetFrom(&start, now)
	if !got.Equal(want) {
		t.Errorf("nextMonthlyResetFrom = %v, want %v", got, want)
	}
}

func TestNextMonthlyResetFrom_NilStart(t *testing.T) {
	now := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	got := nextMonthlyResetFrom(nil, now)
	want := now.Add(30 * 24 * time.Hour)
	if !got.Equal(want) {
		t.Errorf("nextMonthlyResetFrom(nil) = %v, want now+30d=%v", got, want)
	}
}

func TestNextMonthlyResetFrom_NilStart_NotEqualToNow(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	got := nextMonthlyResetFrom(nil, now)
	want := now.Add(30 * 24 * time.Hour)
	if !got.Equal(want) {
		t.Errorf("nextMonthlyResetFrom(nil) = %v, want %v (now+30d)", got, want)
	}
	if got.Equal(now) {
		t.Error("nextMonthlyResetFrom(nil) must not return now (should be now+30d)")
	}
}

// TestNextMonthlyResetFrom_ExpiredStart 验证窗口已过期（now-start >= 30d）时，
// 下次重置时间为 now+30d，而非 start+30d（后者已是过去时间，会让 Retry-After 落回
// fallback 并触发客户端紧凑重试）。
func TestNextMonthlyResetFrom_ExpiredStart(t *testing.T) {
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) // 距 start 61 天，已过期
	got := nextMonthlyResetFrom(&start, now)
	want := now.Add(30 * 24 * time.Hour)
	if !got.Equal(want) {
		t.Errorf("nextMonthlyResetFrom(expired) = %v, want now+30d=%v", got, want)
	}
	if !got.After(now) {
		t.Error("expired window 的下次重置必须在 now 之后，不能是过去时间")
	}
}

func TestIncrementUserPlatformQuotaUsage_GuardsAgainstEmpty(t *testing.T) {
	fake := &fakeIncrCache{}
	cfg := &config.Config{}
	cfg.Billing.UserPlatformQuotaCacheTTLSeconds = 60
	s := &BillingCacheService{
		cache: fake,
		cfg:   cfg,
	}

	s.IncrementUserPlatformQuotaUsage(1, "", 0.5)        // empty platform → noop
	s.IncrementUserPlatformQuotaUsage(1, "openai", 0)    // zero cost → noop
	s.IncrementUserPlatformQuotaUsage(1, "openai", -0.1) // negative → noop

	if len(fake.calls) != 0 {
		t.Errorf("expected 0 calls (all guarded), got %d", len(fake.calls))
	}
}

// ── C-NEW-2: 订阅模式豁免 user×platform quota 检查 ──────────────────────────
// 通过直接调用 checkUserPlatformQuotaEligibility 验证：
// 1. standard 模式下 limit=0 → 拦截
// 2. 订阅模式豁免通过 isSubscriptionMode 守卫体现 — 逻辑已在 CheckBillingEligibility 里加 !isSubscriptionMode 条件
// 此处用单元测试直接验证底层 checkUserPlatformQuotaEligibility 的行为（quota 超限确实拦截），
// 而 subscription bypass 逻辑则在 CheckBillingEligibility 中通过条件判断保证，不绕过 sub eligibility 内部复杂依赖。

// fakeZeroQuotaCache 模拟 cache 命中且 daily limit=0（quota 耗尽）。
type fakeZeroQuotaCache struct {
	BillingCache
	called bool
}

func (f *fakeZeroQuotaCache) GetUserPlatformQuotaCache(_ context.Context, _ int64, _ string) (*UserPlatformQuotaCacheEntry, bool, error) {
	f.called = true
	daily := 0.0
	entry := &UserPlatformQuotaCacheEntry{
		DailyUsageUSD:    0,
		DailyLimitUSD:    &daily,
		DailyWindowStart: func() *time.Time { t := time.Now().UTC(); return &t }(),
		SchemaVersion:    UserPlatformQuotaCacheSchemaV1,
	}
	return entry, true, nil
}

func (f *fakeZeroQuotaCache) DeleteUserPlatformQuotaCache(_ context.Context, _ int64, _ string) error {
	return nil
}

// SetUserPlatformQuotaCache 保留测试替身的旧接口，生产准入不再调用。
func (f *fakeZeroQuotaCache) SetUserPlatformQuotaCache(_ context.Context, _ int64, _ string, _ *UserPlatformQuotaCacheEntry, _ time.Duration) error {
	return nil
}

// TestCheckUserPlatformQuotaEligibility_StandardMode_BlocksWhenLimitZero 验证：
// standard 模式下 limit=0 的 platform quota 确实会被拦截（守卫底层逻辑正确）。
func TestCheckUserPlatformQuotaEligibility_StandardMode_BlocksWhenLimitZero(t *testing.T) {
	zero := 0.0
	fake := &fakeZeroQuotaCache{}
	svc := newServiceForPreflight(t, &fakeQuotaRepo{rec: &UserPlatformQuotaRecord{DailyLimitUSD: &zero}}, fake)
	err := svc.checkUserPlatformQuotaEligibility(context.Background(), 1, "anthropic")
	if !errors.Is(err, ErrUserPlatformDailyQuotaExhausted) {
		t.Fatalf("数据库零额度应阻断: %v", err)
	}
	if fake.called {
		t.Fatal("平台准入不得查询Redis")
	}
}

// TestCheckBillingEligibility_SubscriptionMode_BypassesPlatformQuota 验证（C-NEW-2）：
// 订阅模式即使数据库平台额度为零也不被拦截，旧缓存同样不应被调用。
func TestCheckBillingEligibility_SubscriptionMode_BypassesPlatformQuota(t *testing.T) {
	fake := &fakeZeroQuotaCache{}
	zero := 0.0
	cfg := &config.Config{}
	cfg.Billing.UserPlatformQuotaCacheTTLSeconds = 60
	s := &BillingCacheService{
		cache:                 fake,
		cfg:                   cfg,
		userPlatformQuotaRepo: &fakeQuotaRepo{rec: &UserPlatformQuotaRecord{DailyLimitUSD: &zero}},
	}

	subGroup := &Group{ID: 10, Status: "active"}
	now := time.Now()
	sub := &UserSubscription{
		Status:    SubscriptionStatusActive,
		StartsAt:  now.Add(-time.Hour),
		ExpiresAt: now.Add(time.Hour),
	}
	user := &User{ID: 42}

	err := s.CheckBillingEligibility(context.Background(), user, nil, subGroup, sub, "anthropic")
	if err != nil {
		t.Errorf("有效订阅应正常放行，不受数据库零平台额度影响: %v", err)
	}
	// GetUserPlatformQuotaCache 不应被调用
	if fake.called {
		t.Error("GetUserPlatformQuotaCache must NOT be called in subscription mode (C-NEW-2)")
	}
}

// TestCheckBillingEligibility_NonSubscriptionGroup_AppliesQuota 验证：
// 非订阅余额路径的数据库平台额度超限时被拦截，旧缓存不参与判断。
func TestCheckBillingEligibility_NonSubscriptionGroup_AppliesQuota(t *testing.T) {
	zero := 0.0
	fake := &fakeZeroQuotaCache{}
	svc := newServiceForPreflight(t, &fakeQuotaRepo{rec: &UserPlatformQuotaRecord{DailyLimitUSD: &zero}}, fake)
	err := svc.checkUserPlatformQuotaEligibility(context.Background(), 1, "anthropic")
	if !errors.Is(err, ErrUserPlatformDailyQuotaExhausted) {
		t.Fatalf("数据库零额度应阻断: %v", err)
	}
	if fake.called {
		t.Fatal("平台准入不得查询Redis")
	}
}

// ── B-3: monthlyQuotaWindowExpired 30 天边界表驱动测试 ────────────────────────
// 覆盖 4 个必须场景:
//  1. 恰好 30 天 → expired
//  2. 30*24h - 1ns → not expired
//  3. 跨月末（2024-02-28 → 2024-03-29T00:00:01Z）→ expired
//  4. 跨年（2024-12-15 → 2025-01-14T00:00:01Z）→ expired
//
// repo 层 monthlyMaybeReset 不可导出，通过 service 层 monthlyQuotaWindowExpired 间接覆盖。
func TestMonthlyQuotaWindowExpired_BoundaryTable(t *testing.T) {
	const thirtyDays = 30 * 24 * time.Hour

	cases := []struct {
		name    string
		start   time.Time
		now     time.Time
		expired bool
	}{
		{
			name:    "exactly 30 days → expired",
			start:   time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			now:     time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(thirtyDays),
			expired: true,
		},
		{
			name:    "30d minus 1ns → not expired",
			start:   time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			now:     time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(thirtyDays - 1),
			expired: false,
		},
		{
			name:    "cross month-end (Feb→Mar, 29d+1s) → expired",
			start:   time.Date(2024, 2, 28, 0, 0, 0, 0, time.UTC),
			now:     time.Date(2024, 3, 29, 0, 0, 1, 0, time.UTC),
			expired: true,
		},
		{
			name:    "cross year boundary (Dec→Jan, 30d+1s) → expired",
			start:   time.Date(2024, 12, 15, 0, 0, 0, 0, time.UTC),
			now:     time.Date(2025, 1, 14, 0, 0, 1, 0, time.UTC),
			expired: true,
		},
	}

	for _, tc := range cases {
		tc := tc // capture range variable
		t.Run(tc.name, func(t *testing.T) {
			got := monthlyQuotaWindowExpired(&tc.start, tc.now)
			if got != tc.expired {
				t.Errorf("monthlyQuotaWindowExpired(start=%v, now=%v) = %v, want %v",
					tc.start, tc.now, got, tc.expired)
			}
		})
	}
}

// 缺失行无限额，但不留下可能遮蔽管理员新配置的 Redis 占位。
func TestCheckUserPlatformQuotaEligibility_NoRowDoesNotWriteSentinel(t *testing.T) {
	repo := &fakeQuotaRepo{}
	cache := &fakeFullCache{}
	svc := newServiceForPreflight(t, repo, cache)
	if err := svc.checkUserPlatformQuotaEligibility(context.Background(), 1, "openai"); err != nil {
		t.Fatal(err)
	}
	if cache.getSetCalls() != 0 || cache.getEntry() != nil {
		t.Fatal("缺失行不得缓存为无限额")
	}
	zero := 0.0
	repo.rec = &UserPlatformQuotaRecord{DailyLimitUSD: &zero}
	if err := svc.checkUserPlatformQuotaEligibility(context.Background(), 1, "openai"); !errors.Is(err, ErrUserPlatformDailyQuotaExhausted) {
		t.Fatalf("新限额应立即生效: %v", err)
	}
}

// Redis 故障不影响数据库无限额判断，也不会触发缓存回填。
func TestCheckUserPlatformQuotaEligibility_RedisGetError_NoSentinelBackfill(t *testing.T) {
	repo := &fakeQuotaRepo{rec: nil}
	cache := &fakeFullCache{getErr: errors.New("redis get down")}
	svc := newServiceForPreflight(t, repo, cache)
	svc.cfg.Billing.UserPlatformQuotaSentinelTTLSeconds = 3600

	if err := svc.checkUserPlatformQuotaEligibility(context.Background(), 1, "anthropic"); err != nil {
		t.Fatalf("redis 故障应 fail-open, got %v", err)
	}
	if cache.getSetCalls() != 0 {
		t.Errorf("redis-get-error 时不应回填 sentinel, got %d set calls", cache.getSetCalls())
	}
}

// 数据库故障保留已有放行语义，Redis 陈旧超限快照不能替代本次查询结果。
func TestCheckUserPlatformQuotaEligibility_DatabaseFailurePreservesFailOpen(t *testing.T) {
	zero := 0.0
	cache := &fakeFullCache{entry: &UserPlatformQuotaCacheEntry{DailyLimitUSD: &zero, SchemaVersion: UserPlatformQuotaCacheSchemaV1}}
	svc := newServiceForPreflight(t, &fakeQuotaRepo{err: errors.New("database unavailable")}, cache)
	if err := svc.checkUserPlatformQuotaEligibility(context.Background(), 1, "openai"); err != nil {
		t.Fatal(err)
	}
	if cache.getSetCalls() != 0 {
		t.Fatal("数据库失败不能写入缓存")
	}
}

// 旧无限额占位跨窗口后仍被忽略，不再进行刷新或延长 TTL。
func TestCheckUserPlatformQuotaEligibility_SentinelCrossDay_NoRefresh(t *testing.T) {
	yesterday := timezone.StartOfDay(time.Now().AddDate(0, 0, -1))
	lastWeek := timezone.StartOfWeek(time.Now().AddDate(0, 0, -7))
	monthAgoOK := time.Now().AddDate(0, 0, -5) // <30d, monthly 不过期
	sentinel := &UserPlatformQuotaCacheEntry{
		SchemaVersion:      UserPlatformQuotaCacheSchemaV1,
		DailyWindowStart:   &yesterday, // 跨日 → daily windowExpired = true
		WeeklyWindowStart:  &lastWeek,  // 跨周 → weekly windowExpired = true
		MonthlyWindowStart: &monthAgoOK,
		// limits 全 nil → sentinel
	}
	cache := &fakeFullCache{entry: sentinel} // entry 非 nil → Get HIT
	svc := newServiceForPreflight(t, &fakeQuotaRepo{}, cache)

	if err := svc.checkUserPlatformQuotaEligibility(context.Background(), 1, "anthropic"); err != nil {
		t.Fatalf("sentinel = no limit, expected nil, got %v", err)
	}
	if cache.getSetCalls() != 0 {
		t.Errorf("sentinel cross-window must NOT trigger refresh SetCache, got %d calls", cache.getSetCalls())
	}
}

// ── TestHasUserPlatformQuotaLimit ────────────────────────────────────────────

func TestHasUserPlatformQuotaLimit(t *testing.T) {
	zero := 0.0
	cases := []struct {
		name string
		rec  *UserPlatformQuotaRecord
		err  error
		want bool
	}{
		{"缺失行", nil, nil, false}, {"全空限额", &UserPlatformQuotaRecord{}, nil, false},
		{"零额度", &UserPlatformQuotaRecord{DailyLimitUSD: &zero}, nil, true},
		{"数据库故障", nil, errors.New("database unavailable"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newServiceForPreflight(t, &fakeQuotaRepo{rec: tc.rec, err: tc.err}, &fakeFullCache{entry: &UserPlatformQuotaCacheEntry{}})
			if got := svc.HasUserPlatformQuotaLimit(context.Background(), 1, "openai"); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			svc.cfg.RunMode = config.RunModeSimple
			if svc.HasUserPlatformQuotaLimit(context.Background(), 1, "openai") {
				t.Fatal("简易模式必须豁免")
			}
		})
	}
}
