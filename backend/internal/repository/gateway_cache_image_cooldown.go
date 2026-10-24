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
var imageModelCooldownSetScript = redis.NewScript(`
if redis.call('PTTL', KEYS[1]) <= 0 then
  redis.call('SET', KEYS[1], '1', 'PX', ARGV[1])
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
	started := time.Now()
	if c.rdb == nil || !c.imageModelCooldowns.redisAvailable(started) {
		c.imageModelCooldowns.observe(key, started.Add(duration), started, true)
		return errors.New("image model cooldown Redis unavailable")
	}
	budget, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	remaining, err := imageModelCooldownSetScript.Run(budget, c.smartRoutingRedisClient(), []string{key}, max(int64(1), duration.Milliseconds())).Int64()
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

var _ service.ImageModelCooldownCache = (*gatewayCache)(nil)
