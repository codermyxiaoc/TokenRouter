package service

import "context"

type chanshuiManualAccountStateKey struct{}

// WithChanshuiManualAccountState 仅由管理员显式状态/调度操作使用，后台维护不自动携带。
func WithChanshuiManualAccountState(ctx context.Context) context.Context {
	return context.WithValue(ctx, chanshuiManualAccountStateKey{}, true)
}

// HasChanshuiManualAccountState 让仓储在原账号事务内使旧自动恢复资格失效。
func HasChanshuiManualAccountState(ctx context.Context) bool {
	manual, _ := ctx.Value(chanshuiManualAccountStateKey{}).(bool)
	return manual
}
