package repository

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/redis/go-redis/v9"
)

const imageModelCooldownLocalLimit = 4096

// 图片健康状态单独隔离，不改账号持久启用状态或用户的智能路由冷却配置。
type imageModelCooldownLocal struct {
	mu               sync.Mutex
	entries          map[string]time.Time
	unavailableUntil time.Time
	// knownClear 记录调度阶段刚确认无冷却的组合，短时间内不再逐个访问 Redis。
	knownClear map[string]time.Time
}

// imageModelKnownClearTTL 是“已确认无冷却”的本地缓存时长；本实例写入冷却时立即失效，
// 其它实例写入的冷却最多晚这么久被看到。
const imageModelKnownClearTTL = time.Second

func (s *imageModelCooldownLocal) isKnownClear(key string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if until, ok := s.knownClear[key]; ok {
		if until.After(now) {
			return true
		}
		delete(s.knownClear, key)
	}
	return false
}

func (s *imageModelCooldownLocal) markKnownClear(key string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.knownClear == nil {
		s.knownClear = make(map[string]time.Time)
	}
	if len(s.knownClear) >= imageModelCooldownLocalLimit {
		// 条目只存活一秒，超限时整体丢弃即可，不做逐条淘汰。
		s.knownClear = make(map[string]time.Time)
	}
	s.knownClear[key] = now.Add(imageModelKnownClearTTL)
}

func (s *imageModelCooldownLocal) forgetKnownClear(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.knownClear, key)
}

func (s *imageModelCooldownLocal) get(key string, now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if until := s.entries[key]; until.After(now) {
		return until.Sub(now)
	}
	delete(s.entries, key)
	return 0
}

func (s *imageModelCooldownLocal) observe(key string, until, now time.Time, firstOnly bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if firstOnly && s.entries[key].After(now) {
		return
	}
	if !until.After(now) {
		delete(s.entries, key)
		return
	}
	if s.entries == nil {
		s.entries = make(map[string]time.Time)
	}
	if _, exists := s.entries[key]; !exists && len(s.entries) >= imageModelCooldownLocalLimit {
		var earliestKey string
		var earliest time.Time
		for candidate, expiry := range s.entries {
			if !expiry.After(now) {
				delete(s.entries, candidate)
				continue
			}
			if earliest.IsZero() || expiry.Before(earliest) {
				earliestKey, earliest = candidate, expiry
			}
		}
		if len(s.entries) >= imageModelCooldownLocalLimit {
			delete(s.entries, earliestKey)
		}
	}
	s.entries[key] = until
}

func (s *imageModelCooldownLocal) redisAvailable(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.unavailableUntil.After(now)
}

// Redis 故障后短暂使用已知本地状态，避免挑选多个候选时逐个等待 socket 超时。
func (s *imageModelCooldownLocal) redisFailed(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unavailableUntil = now.Add(time.Second)
}

func imageModelCooldownKey(accountID int64, model, operation string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%s%s", len(model), model, operation)))
	return fmt.Sprintf("image:model_cooldown:%d:%x", accountID, digest)
}

// 已有窗口不续期，原子返回实际剩余时间，供多实例本地降级缓存对齐。
// 值保存“模型 NUL 操作”，管理端按账号前缀扫描即可还原哈希键对应的模型。
var imageModelCooldownSetScript = redis.NewScript(`
if redis.call('PTTL', KEYS[1]) <= 0 then
  redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[1])
end
return redis.call('PTTL', KEYS[1])
`)

func (c *gatewayCache) GetImageModelCooldown(ctx context.Context, accountID int64, model, operation string) (time.Duration, error) {
	model, operation = strings.TrimSpace(model), service.NormalizeImageModelOperation(operation)
	if c == nil || accountID <= 0 || model == "" || operation == "" {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	key := imageModelCooldownKey(accountID, model, operation)
	started := time.Now()
	// 调用方已取消或耗尽预算时不碰Redis，也不能把它当成共享依赖故障。
	if err := ctx.Err(); err != nil {
		return c.imageModelCooldowns.get(key, started), err
	}
	if c.rdb == nil || !c.imageModelCooldowns.redisAvailable(started) {
		return c.imageModelCooldowns.get(key, started), errors.New("image model cooldown Redis unavailable")
	}
	budget, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	ttl, err := c.smartRoutingRedisClient().PTTL(budget, key).Result()
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(ctx.Err(), context.Canceled) {
			c.imageModelCooldowns.redisFailed(time.Now())
		}
		return c.imageModelCooldowns.get(key, time.Now()), err
	}
	// 用查询起点扣除通信耗时，不能把旧 TTL 在返回时重新开启一段窗口。
	until := started.Add(max(0, ttl))
	c.imageModelCooldowns.observe(key, until, time.Now(), false)
	return max(0, time.Until(until)), nil
}

func (c *gatewayCache) CooldownImageModel(ctx context.Context, accountID int64, model, operation string, duration time.Duration) error {
	model, operation = strings.TrimSpace(model), service.NormalizeImageModelOperation(operation)
	if c == nil || accountID <= 0 || model == "" || operation == "" || duration <= 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	duration = min(duration, time.Hour)
	key := imageModelCooldownKey(accountID, model, operation)
	// 本实例刚确认的“无冷却”立即作废，下一次调度必须看到新窗口。
	c.imageModelCooldowns.forgetKnownClear(key)
	started := time.Now()
	if c.rdb == nil || !c.imageModelCooldowns.redisAvailable(started) {
		c.imageModelCooldowns.observe(key, started.Add(duration), started, true)
		return errors.New("image model cooldown Redis unavailable")
	}
	budget, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	remaining, err := imageModelCooldownSetScript.Run(budget, c.smartRoutingRedisClient(), []string{key}, max(int64(1), duration.Milliseconds()), imageModelCooldownValue(model, operation)).Int64()
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return err
		}
		c.imageModelCooldowns.redisFailed(time.Now())
		c.imageModelCooldowns.observe(key, started.Add(duration), time.Now(), true)
		return err
	}
	c.imageModelCooldowns.observe(key, started.Add(time.Duration(max(int64(0), remaining))*time.Millisecond), time.Now(), false)
	return nil
}

// ImageModelCoolingForSchedule 供调度候选过滤使用：确认无冷却的结果本地缓存一秒，
// 避免大分组在高并发下逐个账号串行访问 Redis；Redis 故障时沿用有界本地状态。
func (c *gatewayCache) ImageModelCoolingForSchedule(ctx context.Context, accountID int64, model, operation string) bool {
	model, operation = strings.TrimSpace(model), service.NormalizeImageModelOperation(operation)
	if c == nil || accountID <= 0 || model == "" || operation == "" {
		return false
	}
	key := imageModelCooldownKey(accountID, model, operation)
	if c.imageModelCooldowns.isKnownClear(key, time.Now()) {
		return false
	}
	remaining, err := c.GetImageModelCooldown(ctx, accountID, model, operation)
	if remaining > 0 {
		return true
	}
	if err == nil {
		c.imageModelCooldowns.markKnownClear(key, time.Now())
	}
	return false
}

func imageModelStrikeKey(accountID int64, model, operation string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%s%s", len(model), model, operation)))
	return fmt.Sprintf("image:model_strikes:%d:%x", accountID, digest)
}

// 模型名不含 NUL，用它分隔模型与操作，避免模型名中的冒号造成歧义。
func imageModelCooldownValue(model, operation string) string {
	return model + "\x00" + operation
}

// 连续失败计数：首次失败开启窗口，窗口内的后续失败只累加不续期，窗口结束后重新计数。
var imageModelStrikeIncrScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if redis.call('PTTL', KEYS[1]) < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return count
`)

// imageModelStrikeLocal 是 Redis 不可用时的连续失败计数降级，按条目上限淘汰最早过期者。
type imageModelStrikeLocal struct {
	mu      sync.Mutex
	entries map[string]imageModelStrikeEntry
}

type imageModelStrikeEntry struct {
	count int
	until time.Time
}

func (s *imageModelStrikeLocal) incr(key string, window time.Duration, now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]imageModelStrikeEntry)
	}
	entry := s.entries[key]
	if !entry.until.After(now) {
		entry = imageModelStrikeEntry{until: now.Add(window)}
	}
	entry.count++
	if _, exists := s.entries[key]; !exists && len(s.entries) >= imageModelCooldownLocalLimit {
		for candidate, existing := range s.entries {
			if !existing.until.After(now) {
				delete(s.entries, candidate)
			}
		}
		if len(s.entries) >= imageModelCooldownLocalLimit {
			for candidate := range s.entries {
				delete(s.entries, candidate)
				break
			}
		}
	}
	s.entries[key] = entry
	return entry.count
}

func (s *imageModelStrikeLocal) get(key string, now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.entries[key]; entry.until.After(now) {
		return entry.count
	}
	return 0
}

func (s *imageModelStrikeLocal) reset(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
}

func (s *imageModelCooldownLocal) clear(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
}

func (c *gatewayCache) IncrImageModelFailures(ctx context.Context, accountID int64, model, operation string, window time.Duration) (int, error) {
	model, operation = strings.TrimSpace(model), service.NormalizeImageModelOperation(operation)
	if c == nil || accountID <= 0 || model == "" || operation == "" {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if window <= 0 {
		window = 2 * time.Hour
	}
	key := imageModelStrikeKey(accountID, model, operation)
	now := time.Now()
	if c.rdb == nil || !c.imageModelCooldowns.redisAvailable(now) {
		return c.imageModelStrikes.incr(key, window, now), nil
	}
	budget, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	count, err := imageModelStrikeIncrScript.Run(budget, c.smartRoutingRedisClient(), []string{key}, max(int64(1), window.Milliseconds())).Int64()
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(ctx.Err(), context.Canceled) {
			c.imageModelCooldowns.redisFailed(time.Now())
		}
		return c.imageModelStrikes.incr(key, window, time.Now()), nil
	}
	return int(count), nil
}

func (c *gatewayCache) ResetImageModelFailures(ctx context.Context, accountID int64, model, operation string) error {
	model, operation = strings.TrimSpace(model), service.NormalizeImageModelOperation(operation)
	if c == nil || accountID <= 0 || model == "" || operation == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	key := imageModelStrikeKey(accountID, model, operation)
	c.imageModelStrikes.reset(key)
	if c.rdb == nil || !c.imageModelCooldowns.redisAvailable(time.Now()) {
		return nil
	}
	budget, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	return c.smartRoutingRedisClient().Del(budget, key).Err()
}

// ListImageModelCooldowns 按账号前缀扫描仍在生效的生图冷却；只服务管理端排障，不在请求路径调用。
func (c *gatewayCache) ListImageModelCooldowns(ctx context.Context, accountID int64) ([]service.ImageModelCooldownEntry, error) {
	if c == nil || accountID <= 0 {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if c.rdb == nil {
		return nil, errors.New("image model cooldown Redis unavailable")
	}
	client := c.smartRoutingRedisClient()
	pattern := fmt.Sprintf("image:model_cooldown:%d:*", accountID)
	var entries []service.ImageModelCooldownEntry
	iter := client.Scan(ctx, 0, pattern, 200).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		value, err := client.Get(ctx, key).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			return nil, err
		}
		model, operation, ok := strings.Cut(value, "\x00")
		if !ok {
			// 旧版本写入的值不含模型信息，无法展示，等待自然过期。
			continue
		}
		ttl, err := client.PTTL(ctx, key).Result()
		if err != nil {
			return nil, err
		}
		if ttl <= 0 {
			continue
		}
		failures, _ := client.Get(ctx, imageModelStrikeKey(accountID, model, operation)).Int()
		entries = append(entries, service.ImageModelCooldownEntry{
			AccountID:        accountID,
			Model:            model,
			Operation:        operation,
			RemainingSeconds: int64((ttl + time.Second - 1) / time.Second),
			Failures:         failures,
		})
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// ClearImageModelCooldown 由管理员手动解除冷却，同时清零连续失败计数。
func (c *gatewayCache) ClearImageModelCooldown(ctx context.Context, accountID int64, model, operation string) error {
	model, operation = strings.TrimSpace(model), service.NormalizeImageModelOperation(operation)
	if c == nil || accountID <= 0 || model == "" || operation == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	key := imageModelCooldownKey(accountID, model, operation)
	strikeKey := imageModelStrikeKey(accountID, model, operation)
	c.imageModelCooldowns.clear(key)
	c.imageModelStrikes.reset(strikeKey)
	if c.rdb == nil {
		return nil
	}
	return c.smartRoutingRedisClient().Del(ctx, key, strikeKey).Err()
}

var (
	_ service.ImageModelCooldownCache   = (*gatewayCache)(nil)
	_ service.ImageModelFailureTracker  = (*gatewayCache)(nil)
	_ service.ImageModelCooldownAdmin   = (*gatewayCache)(nil)
	_ service.ImageModelScheduleCooling = (*gatewayCache)(nil)
)
