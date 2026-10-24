package service

import (
	"context"
	"sync"
	"time"
)

type smartRoutingAccountFailuresKey struct{}
type smartRoutingAccountCooldownContextKey struct{}

// 同一客户端请求共享失败集合，换分组时不得把已失败账号重新投入调度。
type smartRoutingAccountFailures struct {
	mu                sync.RWMutex
	failed            map[int64]struct{}
	cooling           map[int64]time.Time
	requiredAccountID int64
	startedAt         time.Time
	admissions        map[smartRoutingCooldownAdmissionKey]int64
}

type smartRoutingCooldownAdmissionKey struct {
	keyID, accountID int64
	model, endpoint  string
}

type smartRoutingAccountCooldownContext struct {
	keyID    int64
	duration time.Duration
	model    string
	endpoint string
}

// WithSmartRoutingAccountFailures 只为智能路由创建请求私有状态；复用上下文时保留同一集合。
func WithSmartRoutingAccountFailures(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(smartRoutingAccountFailuresKey{}).(*smartRoutingAccountFailures); ok {
		return ctx
	}
	return context.WithValue(ctx, smartRoutingAccountFailuresKey{}, &smartRoutingAccountFailures{
		failed: make(map[int64]struct{}), cooling: make(map[int64]time.Time),
		startedAt: time.Now(), admissions: make(map[smartRoutingCooldownAdmissionKey]int64),
	})
}

// RememberSmartRoutingCooldownAdmission 保存首次候选读取时的 Redis 时钟，不使用实例墙钟比先后。
// 按 Key/账号/模型/入口隔离，换组仍属于同一请求，不刷新入场时间以重开已处理窗口。
func RememberSmartRoutingCooldownAdmission(ctx context.Context, accountID, redisMillis int64) {
	if ctx == nil || redisMillis <= 0 {
		return
	}
	state, _ := ctx.Value(smartRoutingAccountFailuresKey{}).(*smartRoutingAccountFailures)
	config, _ := ctx.Value(smartRoutingAccountCooldownContextKey{}).(smartRoutingAccountCooldownContext)
	if state == nil || config.keyID <= 0 {
		return
	}
	key := smartRoutingCooldownAdmissionKey{config.keyID, accountID, config.model, config.endpoint}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.admissions == nil {
		state.admissions = make(map[smartRoutingCooldownAdmissionKey]int64)
	}
	if previous, exists := state.admissions[key]; !exists || redisMillis < previous {
		state.admissions[key] = redisMillis
	}
}

// SmartRoutingCooldownRequestInfo 区分请求入场时间未知与非请求调用；未知不能借失败写重开旧窗口。
func SmartRoutingCooldownRequestInfo(ctx context.Context, accountID int64) (time.Time, int64, bool) {
	if ctx == nil {
		return time.Time{}, 0, false
	}
	state, _ := ctx.Value(smartRoutingAccountFailuresKey{}).(*smartRoutingAccountFailures)
	config, _ := ctx.Value(smartRoutingAccountCooldownContextKey{}).(smartRoutingAccountCooldownContext)
	if state == nil || config.keyID <= 0 {
		return time.Time{}, 0, false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.startedAt, state.admissions[smartRoutingCooldownAdmissionKey{config.keyID, accountID, config.model, config.endpoint}], true
}

// WithSmartRoutingAccountCooldown 传递本次已鉴权的冷却维度，不写入共享 Key 快照。
func WithSmartRoutingAccountCooldown(ctx context.Context, keyID int64, duration time.Duration, model, endpoint string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if duration <= 0 {
		if state, _ := ctx.Value(smartRoutingAccountFailuresKey{}).(*smartRoutingAccountFailures); state != nil {
			state.mu.Lock()
			clear(state.cooling)
			state.mu.Unlock()
		}
	}
	return context.WithValue(ctx, smartRoutingAccountCooldownContextKey{}, smartRoutingAccountCooldownContext{
		keyID: keyID, duration: duration, model: model, endpoint: endpoint,
	})
}

// MarkSmartRoutingAccountFailed 记录真实尝试失败，普通请求不创建额外状态。
func MarkSmartRoutingAccountFailed(ctx context.Context, accountID int64) {
	if ctx == nil {
		return
	}
	state, _ := ctx.Value(smartRoutingAccountFailuresKey{}).(*smartRoutingAccountFailures)
	if state == nil || accountID <= 0 {
		return
	}
	state.mu.Lock()
	state.failed[accountID] = struct{}{}
	state.mu.Unlock()
}

// SetSmartRoutingContinuationAccount 仅由已验证响应归属的智能选组器设置硬绑定。
func SetSmartRoutingContinuationAccount(ctx context.Context, accountID int64) {
	if ctx == nil {
		return
	}
	state, _ := ctx.Value(smartRoutingAccountFailuresKey{}).(*smartRoutingAccountFailures)
	if state == nil || accountID <= 0 {
		return
	}
	state.mu.Lock()
	state.requiredAccountID = accountID
	state.mu.Unlock()
}

// SmartRoutingRequiredAccountID 返回经过租户归属校验的续接账号，零表示没有可证明的绑定。
func SmartRoutingRequiredAccountID(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	state, _ := ctx.Value(smartRoutingAccountFailuresKey{}).(*smartRoutingAccountFailures)
	if state == nil {
		return 0
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.requiredAccountID
}

// IsSmartRoutingAccountExcluded 由全部调度路径复核，防止换组、粘性或旧快照重新选中失败账号。
func IsSmartRoutingAccountExcluded(ctx context.Context, accountID int64) bool {
	if ctx == nil {
		return false
	}
	state, _ := ctx.Value(smartRoutingAccountFailuresKey{}).(*smartRoutingAccountFailures)
	if state == nil {
		return false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	_, failed := state.failed[accountID]
	return failed || state.cooling[accountID].After(time.Now()) ||
		state.requiredAccountID > 0 && state.requiredAccountID != accountID
}

func setSmartRoutingAccountCooldowns(ctx context.Context, accountIDs []int64, cooling map[int64]time.Time) {
	state, _ := ctx.Value(smartRoutingAccountFailuresKey{}).(*smartRoutingAccountFailures)
	if state == nil {
		return
	}
	now := time.Now()
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, id := range accountIDs {
		if until := cooling[id]; until.After(now) {
			state.cooling[id] = until
		} else {
			delete(state.cooling, id)
		}
	}
}
