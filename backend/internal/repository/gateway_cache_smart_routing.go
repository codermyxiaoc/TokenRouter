package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/redis/go-redis/v9"
)

const smartRoutingLocalCooldownLimit = 2048

// 本地降级只保留本实例已观测的真实失败；容量固定，过期清理无需后台 goroutine。
type smartRoutingLocalCooldowns struct {
	mu      sync.Mutex
	entries map[[2]int64]time.Time
}

func (c *smartRoutingLocalCooldowns) get(key [2]int64, now time.Time) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	until := c.entries[key]
	if !until.After(now) {
		delete(c.entries, key)
		return 0
	}
	return until.Sub(now)
}

func (c *smartRoutingLocalCooldowns) set(key [2]int64, until, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !until.After(now) || !until.After(c.entries[key]) {
		return
	}
	if c.entries == nil {
		c.entries = make(map[[2]int64]time.Time)
	}
	if _, exists := c.entries[key]; !exists && len(c.entries) >= smartRoutingLocalCooldownLimit {
		var earliestKey [2]int64
		var earliest time.Time
		for candidate, expiry := range c.entries {
			if !expiry.After(now) {
				delete(c.entries, candidate)
				continue
			}
			if earliest.IsZero() || expiry.Before(earliest) {
				earliestKey, earliest = candidate, expiry
			}
		}
		if len(c.entries) >= smartRoutingLocalCooldownLimit {
			delete(c.entries, earliestKey)
		}
	}
	c.entries[key] = until
}

func smartRoutingCooldownKey(keyID, groupID int64) string {
	return fmt.Sprintf("smart_routing:cooldown:%d:%d", keyID, groupID)
}

// Lua 比较 Redis 自身剩余 TTL，避免并发的短冷却覆盖长冷却及多实例时钟差。
var smartRoutingCooldownSetScript = redis.NewScript(`
local remaining = redis.call('PTTL', KEYS[1])
local duration = tonumber(ARGV[1])
if remaining < duration then
  redis.call('SET', KEYS[1], '1', 'PX', duration)
  return duration
end
return remaining
`)

func (c *gatewayCache) GetSmartRoutingCooldown(ctx context.Context, keyID, groupID int64) (time.Duration, error) {
	if keyID <= 0 || groupID <= 0 {
		return 0, nil
	}
	if c == nil {
		return 0, errors.New("smart routing cooldown cache unavailable")
	}
	key := [2]int64{keyID, groupID}
	if c.rdb == nil {
		return c.smartRoutingCooldowns.get(key, time.Now()), errors.New("smart routing Redis unavailable")
	}
	remaining, err := c.rdb.PTTL(ctx, smartRoutingCooldownKey(keyID, groupID)).Result()
	if err != nil {
		return c.smartRoutingCooldowns.get(key, time.Now()), err
	}
	// 正常读取以共享 Redis 为准；正 TTL 同时保存为有界故障降级快照。
	if remaining > 0 {
		now := time.Now()
		c.smartRoutingCooldowns.set(key, now.Add(remaining), now)
	}
	return max(0, remaining), nil
}

func (c *gatewayCache) CooldownSmartRoutingGroup(ctx context.Context, keyID, groupID int64, duration time.Duration) error {
	if duration <= 0 || keyID <= 0 || groupID <= 0 {
		return nil
	}
	if c == nil {
		return errors.New("smart routing cooldown cache unavailable")
	}
	duration = min(duration, time.Hour)
	now := time.Now()
	key := [2]int64{keyID, groupID}
	c.smartRoutingCooldowns.set(key, now.Add(duration), now)
	if c.rdb == nil {
		return errors.New("smart routing Redis unavailable")
	}
	remaining, err := smartRoutingCooldownSetScript.Run(ctx, c.rdb, []string{smartRoutingCooldownKey(keyID, groupID)}, max(int64(1), duration.Milliseconds())).Int64()
	if err == nil && remaining > 0 {
		now = time.Now()
		c.smartRoutingCooldowns.set(key, now.Add(time.Duration(remaining)*time.Millisecond), now)
	}
	return err
}

func smartRoutingSessionClaimKey(ownerKey string) string { return ownerKey + ":smart_claim" }

// 主 owner 保留既有数字格式，随机 token 放入同生命周期的附属键。
var claimSmartRoutingSessionOwnerScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return 0 end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[3])
redis.call('SET', KEYS[2], ARGV[2], 'PX', ARGV[3])
return 1
`)

var finishSmartRoutingSessionOwnerScript = redis.NewScript(`
if redis.call('GET', KEYS[2]) ~= ARGV[2] then return 0 end
if ARGV[3] == '1' and redis.call('GET', KEYS[1]) == ARGV[1] then
  redis.call('DEL', KEYS[1])
end
redis.call('DEL', KEYS[2])
return 1
`)

var setSessionOwnerWithSmartClaimScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return 0 end
if tonumber(ARGV[2]) > 0 then
  redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
else
  redis.call('SET', KEYS[1], ARGV[1])
end
redis.call('DEL', KEYS[2])
return 1
`)

var getSessionOwnerAndCommitSmartClaimScript = redis.NewScript(`
local owner = redis.call('GET', KEYS[1])
if owner then redis.call('DEL', KEYS[2]) end
return owner
`)

var refreshSessionOwnerAndCommitSmartClaimScript = redis.NewScript(`
redis.call('DEL', KEYS[2])
return redis.call('PEXPIRE', KEYS[1], ARGV[1])
`)

func (c *gatewayCache) ClaimSmartRoutingSessionOwner(ctx context.Context, userID int64, source, sessionHash string, groupID int64, token string, ttl time.Duration) (bool, error) {
	if c == nil || c.rdb == nil {
		return false, errors.New("smart routing session cache unavailable")
	}
	if userID <= 0 || groupID <= 0 || source == "" || sessionHash == "" || token == "" || ttl <= 0 {
		return false, errors.New("invalid smart routing session claim")
	}
	key := buildSessionOwnerKey(userID, source, sessionHash)
	written, err := claimSmartRoutingSessionOwnerScript.Run(ctx, c.rdb, []string{key, smartRoutingSessionClaimKey(key)}, groupID, token, max(int64(1), ttl.Milliseconds())).Int()
	return written == 1, err
}

func (c *gatewayCache) FinishSmartRoutingSessionOwner(ctx context.Context, userID int64, source, sessionHash string, groupID int64, token string, release bool) error {
	if c == nil || c.rdb == nil {
		return errors.New("smart routing session cache unavailable")
	}
	if token == "" {
		return errors.New("invalid smart routing session claim token")
	}
	key := buildSessionOwnerKey(userID, source, sessionHash)
	shouldRelease := 0
	if release {
		shouldRelease = 1
	}
	return finishSmartRoutingSessionOwnerScript.Run(ctx, c.rdb, []string{key, smartRoutingSessionClaimKey(key)}, groupID, token, shouldRelease).Err()
}

var _ service.SmartRoutingCooldownCache = (*gatewayCache)(nil)
var _ service.SmartRoutingSessionClaimCache = (*gatewayCache)(nil)
