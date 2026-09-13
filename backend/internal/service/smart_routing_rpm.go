package service

import (
	"context"
	"sync"
)

type smartRoutingRPMContextKey struct{}

// smartRoutingRPMAdmission 在同一客户端请求的各次尝试之间共享，不能放进认证缓存。
type smartRoutingRPMAdmission struct {
	mu     sync.Mutex
	counts map[[2]int64]int
}

// WithSmartRoutingRPMAdmission 仅由可重放的智能请求初始化，避免跨组重复增加全局 RPM。
// @project-doc docs/domains/smart_routing_api_keys.md#group_failover
func WithSmartRoutingRPMAdmission(ctx context.Context) context.Context {
	return context.WithValue(ctx, smartRoutingRPMContextKey{}, &smartRoutingRPMAdmission{counts: make(map[[2]int64]int)})
}

// smartRoutingRPMCount 保留首次成功计数用于复核新限额；每个目标组仍有独立准入。
func smartRoutingRPMCount(ctx context.Context, userID, groupID int64, increment func() (int, error)) (int, error) {
	admission, _ := ctx.Value(smartRoutingRPMContextKey{}).(*smartRoutingRPMAdmission)
	if admission == nil {
		return increment()
	}
	admission.mu.Lock()
	defer admission.mu.Unlock()
	key := [2]int64{userID, groupID}
	if count, ok := admission.counts[key]; ok {
		return count, nil
	}
	count, err := increment()
	if err == nil {
		admission.counts[key] = count
	}
	return count, err
}
