package repository

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/redis/go-redis/v9"
)

const smartRoutingWindowRetention = 24 * time.Hour

type smartRoutingLocalAccountWindow struct {
	until, startedAt, retainUntil time.Time
	redisStartedMillis            int64
	observedRedisMillis           int64
	observedAt                    time.Time
}

// 冷却和已处理窗口分别留存；窗口到期不抹掉迟到请求所需的去重信息，容量仍有上限。
type smartRoutingLocalAccountCooldowns struct {
	mu      sync.Mutex
	entries map[string]smartRoutingLocalAccountWindow
}

func (c *smartRoutingLocalAccountCooldowns) get(key string, now time.Time) time.Duration {
	return max(0, c.deadline(key, now).Sub(now))
}

func (c *smartRoutingLocalAccountCooldowns) deadline(key string, now time.Time) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	if !entry.retainUntil.After(now) {
		delete(c.entries, key)
		return time.Time{}
	}
	return entry.until
}

func (c *smartRoutingLocalAccountCooldowns) putLocked(key string, entry smartRoutingLocalAccountWindow, now time.Time) {
	if c.entries == nil {
		c.entries = make(map[string]smartRoutingLocalAccountWindow)
	}
	if _, exists := c.entries[key]; !exists && len(c.entries) >= smartRoutingLocalCooldownLimit {
		oldest := ""
		var oldestAt time.Time
		for candidate, value := range c.entries {
			if !value.retainUntil.After(now) {
				delete(c.entries, candidate)
				continue
			}
			if oldest == "" || value.retainUntil.Before(oldestAt) {
				oldest, oldestAt = candidate, value.retainUntil
			}
		}
		if len(c.entries) >= smartRoutingLocalCooldownLimit {
			delete(c.entries, oldest)
		}
	}
	c.entries[key] = entry
}

func (c *smartRoutingLocalAccountCooldowns) observeShared(key string, ttl time.Duration, redisNow, windowStart int64, began, ended time.Time) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	previous, exists := c.entries[key]
	// 并发响应按 Redis 观测时刻排序，同毫秒时按本机发起时刻排序，旧响应不得复活窗口。
	if exists && (redisNow < previous.observedRedisMillis || redisNow == previous.observedRedisMillis && began.Before(previous.observedAt)) {
		return previous.until
	}
	if ttl <= 0 && windowStart <= 0 {
		if exists {
			previous.until = time.Time{}
			previous.observedRedisMillis, previous.observedAt = redisNow, began
			c.entries[key] = previous
		}
		return time.Time{}
	}
	// TTL 不从响应收到时重新起算；采用发送前的单调时钟，只会保守提早，不会加上网络延时。
	until := began.Add(max(0, ttl))
	startedAt := ended
	if windowStart > 0 {
		startedAt = ended.Add(-time.Duration(max(int64(0), redisNow-windowStart)) * time.Millisecond)
	}
	c.putLocked(key, smartRoutingLocalAccountWindow{until: until, startedAt: startedAt, retainUntil: startedAt.Add(smartRoutingWindowRetention), redisStartedMillis: windowStart, observedRedisMillis: redisNow, observedAt: began}, ended)
	return until
}

// 仅真实失败可建立本地窗口；重复失败和旧请求不能延长已知窗口。
func (c *smartRoutingLocalAccountCooldowns) failed(key string, duration time.Duration, now, requestStart time.Time, admitted int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !requestStart.IsZero() && now.Sub(requestStart) >= smartRoutingWindowRetention {
		return
	}
	entry := c.entries[key]
	if entry.until.After(now) {
		return
	}
	if entry.retainUntil.After(now) {
		if admitted > 0 && entry.redisStartedMillis > 0 && admitted <= entry.redisStartedMillis {
			return
		}
		if (admitted <= 0 || entry.redisStartedMillis <= 0) && !requestStart.IsZero() && !requestStart.After(entry.startedAt) {
			return
		}
	}
	c.putLocked(key, smartRoutingLocalAccountWindow{until: now.Add(duration), startedAt: now, retainUntil: now.Add(smartRoutingWindowRetention)}, now)
}

func smartRoutingAccountCooldownKey(keyID, accountID int64, model, endpoint string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%s%s", len(model), model, endpoint)))
	return fmt.Sprintf("smart_routing:account_cooldown:%d:%d:%x", keyID, accountID, digest)
}

// 读取不创建任何键。Redis 时间用于请求入场顺序，不依赖多个应用实例的墙钟一致。
var smartRoutingAccountCooldownReadScript = redis.NewScript(`
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
return {redis.call('PTTL', KEYS[1]), now, tonumber(redis.call('GET', KEYS[2])) or 0}
`)

// 真实失败原子开启首次窗口并返回现存 PTTL；旧请求不得在该窗口到期后重新开启。
// 窗口标记有界保留一天，超过同等年龄的在途请求也不能在标记过期后重新开启。
var smartRoutingAccountCooldownWriteScript = redis.NewScript(`
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
local ttl = redis.call('PTTL', KEYS[1])
local started = tonumber(redis.call('GET', KEYS[2])) or 0
local duration, retention = tonumber(ARGV[1]), tonumber(ARGV[2])
local admitted, age = tonumber(ARGV[3]), tonumber(ARGV[4])
if ttl >= 0 then
  if started == 0 then
    started = now
    redis.call('SET', KEYS[2], started, 'PX', retention)
  end
  return {ttl, now, started}
end
if age >= retention or (admitted > 0 and now - admitted >= retention) or (admitted >= 0 and started > 0 and admitted <= started) then
  return {0, now, started}
end
redis.call('SET', KEYS[1], '1', 'PX', duration)
redis.call('SET', KEYS[2], now, 'PX', retention)
return {redis.call('PTTL', KEYS[1]), now, now}
`)

func smartRoutingCooldownResult(command *redis.Cmd) (time.Duration, int64, int64, error) {
	values, err := command.Slice()
	if err != nil {
		return 0, 0, 0, err
	}
	if len(values) != 3 {
		return 0, 0, 0, errors.New("invalid smart routing cooldown response")
	}
	ttl, a := values[0].(int64)
	now, b := values[1].(int64)
	started, d := values[2].(int64)
	if !a || !b || !d {
		return 0, 0, 0, errors.New("invalid smart routing cooldown timestamps")
	}
	return time.Duration(max(int64(0), ttl)) * time.Millisecond, now, started, nil
}

func (c *gatewayCache) GetSmartRoutingAccountCooldownDeadlines(ctx context.Context, keyID int64, accountIDs []int64, model, endpoint string) (map[int64]time.Time, error) {
	remaining := make(map[int64]time.Time)
	if c == nil || keyID <= 0 {
		return remaining, nil
	}
	if c.rdb == nil {
		for _, id := range accountIDs {
			remaining[id] = c.smartRoutingAccountCooldowns.deadline(smartRoutingAccountCooldownKey(keyID, id, model, endpoint), time.Now())
		}
		return remaining, errors.New("smart routing Redis unavailable")
	}
	began := time.Now()
	pipe := c.smartRoutingRedisClient().Pipeline()
	commands := make(map[int64]*redis.Cmd, len(accountIDs))
	for _, id := range accountIDs {
		if id > 0 {
			key := smartRoutingAccountCooldownKey(keyID, id, model, endpoint)
			commands[id] = smartRoutingAccountCooldownReadScript.Eval(ctx, pipe, []string{key, key + ":window"})
		}
	}
	_, err := pipe.Exec(ctx)
	for id, command := range commands {
		key := smartRoutingAccountCooldownKey(keyID, id, model, endpoint)
		ttl, serverNow, started, commandErr := smartRoutingCooldownResult(command)
		if commandErr != nil {
			remaining[id] = c.smartRoutingAccountCooldowns.deadline(key, time.Now())
			if err == nil {
				err = commandErr
			}
			continue
		}
		service.RememberSmartRoutingCooldownAdmission(ctx, id, serverNow)
		remaining[id] = c.smartRoutingAccountCooldowns.observeShared(key, ttl, serverNow, started, began, time.Now())
	}
	return remaining, err
}

// 时长接口只在边界转换，生产选组使用上面的绝对截止时间接口。
func (c *gatewayCache) GetSmartRoutingAccountCooldowns(ctx context.Context, keyID int64, accountIDs []int64, model, endpoint string) (map[int64]time.Duration, error) {
	deadlines, err := c.GetSmartRoutingAccountCooldownDeadlines(ctx, keyID, accountIDs, model, endpoint)
	remaining := make(map[int64]time.Duration, len(deadlines))
	now := time.Now()
	for id, until := range deadlines {
		remaining[id] = max(0, until.Sub(now))
	}
	return remaining, err
}

func (c *gatewayCache) CooldownSmartRoutingAccounts(ctx context.Context, keyID int64, accountIDs []int64, model, endpoint string, duration time.Duration) error {
	if c == nil || keyID <= 0 || duration <= 0 {
		return nil
	}
	duration = min(duration, time.Hour)
	fallback := func(id int64) {
		started, admitted, _ := service.SmartRoutingCooldownRequestInfo(ctx, id)
		c.smartRoutingAccountCooldowns.failed(smartRoutingAccountCooldownKey(keyID, id, model, endpoint), duration, time.Now(), started, admitted)
	}
	if c.rdb == nil {
		for _, id := range accountIDs {
			if id > 0 {
				fallback(id)
			}
		}
		return errors.New("smart routing Redis unavailable")
	}
	began := time.Now()
	pipe := c.smartRoutingRedisClient().Pipeline()
	commands := make(map[int64]*redis.Cmd, len(accountIDs))
	for _, id := range accountIDs {
		if id <= 0 {
			continue
		}
		key := smartRoutingAccountCooldownKey(keyID, id, model, endpoint)
		requestStart, admitted, tracked := service.SmartRoutingCooldownRequestInfo(ctx, id)
		age := int64(0)
		if !tracked {
			admitted = -1
		} else if !requestStart.IsZero() {
			age = max(int64(0), time.Since(requestStart).Milliseconds())
		}
		commands[id] = smartRoutingAccountCooldownWriteScript.Eval(ctx, pipe, []string{key, key + ":window"}, max(int64(1), duration.Milliseconds()), smartRoutingWindowRetention.Milliseconds(), admitted, age)
	}
	_, err := pipe.Exec(ctx)
	for id, command := range commands {
		ttl, serverNow, started, commandErr := smartRoutingCooldownResult(command)
		if commandErr != nil {
			fallback(id)
			if err == nil {
				err = commandErr
			}
			continue
		}
		c.smartRoutingAccountCooldowns.observeShared(smartRoutingAccountCooldownKey(keyID, id, model, endpoint), ttl, serverNow, started, began, time.Now())
	}
	return err
}

var _ service.SmartRoutingAccountCooldownDeadlineCache = (*gatewayCache)(nil)
