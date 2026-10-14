package service

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
)

// quotaDirtyCache 是 flusher 依赖的窄接口（来自 BillingCache）。
type quotaDirtyCache interface {
	PopDirtyUserPlatformQuotaKeys(ctx context.Context, n int) ([]UserPlatformQuotaKey, error)
	ReaddDirtyUserPlatformQuotaKeys(ctx context.Context, keys []UserPlatformQuotaKey) error
	BatchGetUserPlatformQuotaCache(ctx context.Context, keys []UserPlatformQuotaKey) ([]*UserPlatformQuotaCacheEntry, error)
}

// quotaSnapshotWriter 是 flusher 依赖的 DB 写入窄接口。
// 使用 service 层的 UserPlatformQuotaSnapshot，避免与 repository 包形成循环依赖；
// 实际实现由 repository adapter 在 B7 注入。
type quotaSnapshotWriter interface {
	BatchSnapshotUsage(ctx context.Context, snapshots []UserPlatformQuotaSnapshot, now time.Time) error
}

// FlusherMetrics 保留旧监控接口；数据库权威模式不再产生刷写计数。
type FlusherMetrics struct {
	FlushSuccessTotal   atomic.Int64
	FlushErrorTotal     atomic.Int64
	FlushBatchSizeTotal atomic.Int64
	FlushLatencyMsMax   atomic.Int64
	DirtyReaddTotal     atomic.Int64
	// DirtyLostTotal 保留旧脏集失败指标，不能作为当前用量的恢复依据。
	DirtyLostTotal        atomic.Int64
	FlushFKViolationTotal atomic.Int64
}

// maxFlushBatchSize 保留旧配置的归一化边界，不再控制任何数据库刷写。
const maxFlushBatchSize = 6000

// defaultFlushBatchSize 是配置 flush_batch_size 非法(≤0)时的回退值。
const defaultFlushBatchSize = 1000

// UserPlatformQuotaUsageFlusher 保留旧装配与指标接口，数据库权威模式下停止所有快照刷写。
// 不读取历史脏集，也不在退出时回写；旧缓存无法证明逐请求扣费及重置代次。
type UserPlatformQuotaUsageFlusher struct {
	cache       quotaDirtyCache
	quotaRepo   quotaSnapshotWriter
	timingWheel *TimingWheelService
	// enabled 固定为 false，旧配置不能重新启用快照写回。
	enabled      bool
	interval     time.Duration
	batchSize    int
	flushTimeout time.Duration
	metrics      *FlusherMetrics
	stopped      atomic.Bool
}

// NewUserPlatformQuotaUsageFlusher 创建 UserPlatformQuotaUsageFlusher。
// cache(BillingCache) 隐式满足 quotaDirtyCache；quotaRepo(UserPlatformQuotaRepository) 隐式满足 quotaSnapshotWriter。
func NewUserPlatformQuotaUsageFlusher(cfg *config.Config, cache BillingCache, quotaRepo UserPlatformQuotaRepository, tw *TimingWheelService) *UserPlatformQuotaUsageFlusher {
	batchSize := cfg.Database.UserPlatformQuotaFlushBatchSize
	if batchSize <= 0 {
		batchSize = defaultFlushBatchSize
	}
	if batchSize > maxFlushBatchSize {
		logger.LegacyPrintf("quota_flusher",
			"[QuotaFlusher] 兼容配置 flush_batch_size %d 超过上限 %d,已归一化（快照刷写已停用）",
			cfg.Database.UserPlatformQuotaFlushBatchSize, maxFlushBatchSize)
		batchSize = maxFlushBatchSize
	}
	interval := time.Duration(cfg.Database.UserPlatformQuotaFlushIntervalMs) * time.Millisecond
	if interval <= 0 {
		logger.LegacyPrintf("quota_flusher", "[QuotaFlusher] flush_interval_ms %d 非法,回退 2000ms", cfg.Database.UserPlatformQuotaFlushIntervalMs)
		interval = 2 * time.Second
	}
	return &UserPlatformQuotaUsageFlusher{
		cache:        cache,
		quotaRepo:    quotaRepo,
		timingWheel:  tw,
		enabled:      false, // 兼容旧配置，但不能重新开启不具备幂等证据的快照写回。
		interval:     interval,
		batchSize:    batchSize,
		flushTimeout: 3 * time.Second,
		metrics:      &FlusherMetrics{},
	}
}

// updateLatencyMax 用 CAS 单调更新最大延迟。
func (s *UserPlatformQuotaUsageFlusher) updateLatencyMax(ms int64) {
	for {
		old := s.metrics.FlushLatencyMsMax.Load()
		if ms <= old {
			return
		}
		if s.metrics.FlushLatencyMsMax.CompareAndSwap(old, ms) {
			return
		}
	}
}

// flushOneBatch 不再消费历史脏集，任何直接调用也不能覆盖数据库计数。
func (s *UserPlatformQuotaUsageFlusher) flushOneBatch(context.Context) bool { return false }

// flush 保留旧调用兼容，禁止后台快照回写。
func (s *UserPlatformQuotaUsageFlusher) flush() {}

// tick 保留已注册回调兼容，升级后不再执行旧快照写入。
func (s *UserPlatformQuotaUsageFlusher) tick() {}

// Start 保留依赖注入兼容；即使旧配置开启也不注册刷写任务。
func (s *UserPlatformQuotaUsageFlusher) Start() {}

// Stop 只取消旧定时器，绝不执行最终 flush，以免陈旧缓存覆盖已提交用量。
func (s *UserPlatformQuotaUsageFlusher) Stop() {
	if s == nil {
		return
	}
	s.stopped.Store(true)
	if s.timingWheel != nil {
		s.timingWheel.Cancel("deferred:platform_quota")
	}
}
