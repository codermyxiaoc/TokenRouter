package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
)

// ---------------------------------------------------------------------------
// 模拟 quotaDirtyCache。
// ---------------------------------------------------------------------------

type mockQuotaDirtyCache struct {
	// popSequence：第 0 次 Pop 返回 popSequence[0]，之后返回 nil（空集）。
	popSequence [][]UserPlatformQuotaKey
	popCallIdx  int

	// getEntries：BatchGetUserPlatformQuotaCache 返回的 entries（与 keys 对齐）。
	getEntries []*UserPlatformQuotaCacheEntry
	getErr     error

	// readdCalled：记录 Readd 收到的 keys（累积所有次调用）。
	readdCalled [][]UserPlatformQuotaKey
	readdErr    error
}

func (m *mockQuotaDirtyCache) PopDirtyUserPlatformQuotaKeys(_ context.Context, _ int) ([]UserPlatformQuotaKey, error) {
	if m.popCallIdx < len(m.popSequence) {
		keys := m.popSequence[m.popCallIdx]
		m.popCallIdx++
		return keys, nil
	}
	// 超出序列 → 空集（模拟脏集已清空）
	return nil, nil
}

func (m *mockQuotaDirtyCache) ReaddDirtyUserPlatformQuotaKeys(_ context.Context, keys []UserPlatformQuotaKey) error {
	m.readdCalled = append(m.readdCalled, keys)
	return m.readdErr
}

func (m *mockQuotaDirtyCache) BatchGetUserPlatformQuotaCache(_ context.Context, _ []UserPlatformQuotaKey) ([]*UserPlatformQuotaCacheEntry, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.getEntries, nil
}

// ---------------------------------------------------------------------------
// 模拟 quotaSnapshotWriter。
// ---------------------------------------------------------------------------

type mockQuotaSnapshotWriter struct {
	receivedSnaps []UserPlatformQuotaSnapshot
	returnErr     error
}

func (m *mockQuotaSnapshotWriter) BatchSnapshotUsage(_ context.Context, snaps []UserPlatformQuotaSnapshot, _ time.Time) error {
	m.receivedSnaps = append(m.receivedSnaps, snaps...)
	return m.returnErr
}

// ---------------------------------------------------------------------------
// 测试辅助：构造窗口起始时间（非 nil）。
// ---------------------------------------------------------------------------

func flusherPtrTime(t time.Time) *time.Time { return &t }

func makeEntry(daily, weekly, monthly float64) *UserPlatformQuotaCacheEntry {
	now := time.Now().UTC()
	return &UserPlatformQuotaCacheEntry{
		DailyUsageUSD:      daily,
		WeeklyUsageUSD:     weekly,
		MonthlyUsageUSD:    monthly,
		DailyWindowStart:   flusherPtrTime(now),
		WeeklyWindowStart:  flusherPtrTime(now),
		MonthlyWindowStart: flusherPtrTime(now),
	}
}

// ---------------------------------------------------------------------------
// newTestFlusher 直接构造 struct（跳过构造函数，B7 才注入）。
// ---------------------------------------------------------------------------

func newTestFlusher(cache quotaDirtyCache, writer quotaSnapshotWriter) *UserPlatformQuotaUsageFlusher {
	return &UserPlatformQuotaUsageFlusher{
		cache:        cache,
		quotaRepo:    writer,
		timingWheel:  nil, // 单测不启动 TimingWheel
		interval:     5 * time.Second,
		batchSize:    100,
		flushTimeout: 5 * time.Second,
		metrics:      &FlusherMetrics{},
	}
}

// 无论旧开关、入口、缓存内容或故障如何，旧快照都不能再参与写回。
func TestFlusherAllEntrypointsRejectLegacySnapshots(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			cache := &mockQuotaDirtyCache{popSequence: [][]UserPlatformQuotaKey{{{UserID: 1, Platform: PlatformOpenAI}}}, getEntries: []*UserPlatformQuotaCacheEntry{makeEntry(999, 999, 999)}}
			writer := &mockQuotaSnapshotWriter{}
			if fail {
				cache.getErr = errors.New("redis unavailable")
				writer.returnErr = errors.New("database unavailable")
			}
			f := newTestFlusher(cache, writer)
			f.enabled = enabled
			f.Start()
			f.tick()
			f.flush()
			if f.flushOneBatch(context.Background()) {
				t.Fatal("旧刷写入口不能继续消费")
			}
			f.Stop()
			f.Stop()
			if cache.popCallIdx != 0 || len(cache.readdCalled) != 0 || len(writer.receivedSnaps) != 0 {
				t.Fatal("退出或直接调用仍接触旧快照")
			}
		}
	}
}

func TestNewUserPlatformQuotaUsageFlusher_EnabledField(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := &config.Config{}
		cfg.Database.UserPlatformQuotaFlusherEnabled = enabled
		f := NewUserPlatformQuotaUsageFlusher(cfg, nil, nil, nil)
		if f.enabled {
			t.Fatal("旧配置不能重新开启快照刷写")
		}
	}
}

func TestFlusher_NilSafe(t *testing.T) {
	var f *UserPlatformQuotaUsageFlusher
	f.Start()
	f.Stop()
	f.flush()
	f.tick()
	if f.flushOneBatch(context.Background()) {
		t.Fatal("nil兼容入口应停止")
	}
}
func TestNewUserPlatformQuotaUsageFlusher_IntervalFallback(t *testing.T) {
	cases := []struct {
		name   string
		inMs   int
		wantDu time.Duration
	}{
		{"零值回退 2s", 0, 2 * time.Second},
		{"负数回退 2s", -100, 2 * time.Second},
		{"正常 2000ms 保留", 2000, 2 * time.Second},
		{"正常 500ms 保留", 500, 500 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Database.UserPlatformQuotaFlushIntervalMs = tc.inMs
			f := NewUserPlatformQuotaUsageFlusher(cfg, nil, nil, nil)
			if f.interval != tc.wantDu {
				t.Fatalf("interval = %v, want %v", f.interval, tc.wantDu)
			}
		})
	}
}

func TestNewUserPlatformQuotaUsageFlusher_ClampsBatchSize(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"超上限被 clamp", 7000, maxFlushBatchSize},
		{"恰好上限保留", maxFlushBatchSize, maxFlushBatchSize},
		{"零回退默认", 0, defaultFlushBatchSize},
		{"负数回退默认", -5, defaultFlushBatchSize},
		{"正常值保留", 500, 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Database.UserPlatformQuotaFlushBatchSize = tc.in
			f := NewUserPlatformQuotaUsageFlusher(cfg, nil, nil, nil)
			if f.batchSize != tc.want {
				t.Fatalf("batchSize = %d, want %d", f.batchSize, tc.want)
			}
		})
	}
}
