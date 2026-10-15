package service

import "context"

type smartRoutingScopeKey struct{}

// WithSmartRoutingScope 固定本次请求的候选选择，禁止后续传统 fallback 扩大分组范围。
func WithSmartRoutingScope(ctx context.Context) context.Context {
	return context.WithValue(ctx, smartRoutingScopeKey{}, true)
}

func isSmartRoutingScoped(ctx context.Context) bool {
	return IsSmartRoutingScope(ctx)
}

// IsSmartRoutingScope 供缓存适配器限制智能路由的局部等待，不改变普通会话的超时语义。
func IsSmartRoutingScope(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(smartRoutingScopeKey{}).(bool)
	return enabled
}
