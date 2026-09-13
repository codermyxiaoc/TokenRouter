package service

import "context"

type smartRoutingScopeKey struct{}

// WithSmartRoutingScope 固定本次请求的候选选择，禁止后续传统 fallback 扩大分组范围。
func WithSmartRoutingScope(ctx context.Context) context.Context {
	return context.WithValue(ctx, smartRoutingScopeKey{}, true)
}

func isSmartRoutingScoped(ctx context.Context) bool {
	enabled, _ := ctx.Value(smartRoutingScopeKey{}).(bool)
	return enabled
}
