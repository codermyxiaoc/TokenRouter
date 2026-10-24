package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type imageCooldownServiceStub struct {
	reads, writes    int
	account          int64
	model, operation string
	duration, budget time.Duration
	cancelled        bool
}

func (s *imageCooldownServiceStub) GetImageModelCooldown(ctx context.Context, account int64, model, operation string) (time.Duration, error) {
	s.reads++
	s.account, s.model, s.operation = account, model, operation
	deadline, _ := ctx.Deadline()
	s.budget = time.Until(deadline)
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	return s.duration, errors.New("local fallback")
}
func (s *imageCooldownServiceStub) CooldownImageModel(ctx context.Context, account int64, model, operation string, duration time.Duration) error {
	s.writes++
	s.account, s.model, s.operation, s.duration = account, model, operation, duration
	s.cancelled = ctx.Err() != nil
	deadline, _ := ctx.Deadline()
	s.budget = time.Until(deadline)
	return nil
}

// 对外包装只接受图片作用域，并原样使用最终映射模型，不在健康缓存层再映射。
func TestImageModelCooldownServiceScopeAndBudgets(t *testing.T) {
	store := &imageCooldownServiceStub{duration: 3 * time.Second}
	require.Equal(t, 3*time.Second, ImageModelCooldownRemaining(context.Background(), store, 7, " mapped-model ", " Edit "))
	require.Equal(t, int64(7), store.account)
	require.Equal(t, "mapped-model", store.model)
	require.Equal(t, "edit", store.operation)
	require.Positive(t, store.budget)
	require.LessOrEqual(t, store.budget, 250*time.Millisecond)
	for _, operation := range []string{"tts", "text", "chat", "video", ""} {
		require.Zero(t, ImageModelCooldownRemaining(context.Background(), store, 7, "model", operation))
		require.NoError(t, RecordImageModelCooldown(context.Background(), store, 7, "model", operation, time.Second))
	}
	require.Equal(t, 1, store.reads)
	require.Zero(t, store.writes)
	require.False(t, ImageAttemptHasPolicy(context.Background()), "普通文本上下文不会隐式开启图片发送预算")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Zero(t, ImageModelCooldownRemaining(ctx, store, 7, "model", "generate"))
	// 真正账号故障的短写入独立于断开的客户端；调用方仍须排除纯取消。
	require.NoError(t, RecordImageModelCooldown(ctx, store, 7, "model", "generate", 2*time.Hour))
	require.False(t, store.cancelled)
	require.Equal(t, time.Hour, store.duration)
	require.Positive(t, store.budget)
	require.LessOrEqual(t, store.budget, 250*time.Millisecond)
	require.NoError(t, RecordImageModelCooldown(nil, store, 7, "model", "generate", 0))
	require.Equal(t, DefaultImageModelCooldown, store.duration)
}

func TestImageModelCooldownServiceInvalidScopeDoesNotConsultCache(t *testing.T) {
	store := &imageCooldownServiceStub{}
	for _, account := range []int64{0, -1} {
		require.Zero(t, ImageModelCooldownRemaining(nil, store, account, "model", "generate"))
		require.NoError(t, RecordImageModelCooldown(nil, store, account, "model", "generate", time.Second))
	}
	require.Zero(t, ImageModelCooldownRemaining(nil, store, 1, " ", "generate"))
	require.Zero(t, ImageModelCooldownRemaining(nil, nil, 1, "model", "generate"))
	require.Zero(t, store.reads)
	require.Zero(t, store.writes)
}
