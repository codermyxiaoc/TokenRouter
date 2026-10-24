//go:build integration

package repository

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// 真实 Redis 下两份进程内缓存共享修订号，但请求返回的可变元数据保持独立。
func TestSchedulerCacheRealRedisRevisionAndMutableSnapshotIsolation(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	writer := NewSchedulerCache(rdb)
	reader := NewSchedulerCache(rdb)
	bucket := service.SchedulerBucket{GroupID: 9821, Platform: service.PlatformGemini, Mode: service.SchedulerModeSingle}
	account := service.Account{
		ID: 98210, Platform: service.PlatformGemini, Type: service.AccountTypeServiceAccount,
		Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{bucket.GroupID},
		Credentials: map[string]any{"model_mapping": map[string]any{"alias": "first"}, "model_whitelist": []any{"first"}},
		Extra:       map[string]any{"quota_limit": 10.0, "quota_used": 1.0, "model_rate_limits": map[string]any{"other": map[string]any{"reason": "original"}}},
	}
	token, err := writer.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, writer.SetSnapshot(ctx, bucket, token, []service.Account{account}))
	read := func() *service.Account {
		snapshot, hit, err := reader.GetSnapshot(ctx, bucket)
		require.NoError(t, err)
		require.True(t, hit)
		require.Len(t, snapshot, 1)
		return snapshot[0]
	}
	require.True(t, read().IsSchedulable())
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 10; n++ {
				copy := read()
				copy.Credentials["model_mapping"].(map[string]any)["alias"] = "private"
				copy.Credentials["model_whitelist"].([]any)[0] = "private"
				copy.Extra["model_rate_limits"].(map[string]any)["other"].(map[string]any)["reason"] = "private"
				copy.GroupIDs[0] = -1
			}
		}()
	}
	wg.Wait()
	unchanged := read()
	require.Equal(t, "first", unchanged.GetMappedModel("alias"))
	require.Equal(t, []any{"first"}, unchanged.Credentials["model_whitelist"])
	require.Equal(t, "original", unchanged.Extra["model_rate_limits"].(map[string]any)["other"].(map[string]any)["reason"])
	require.Equal(t, []int64{bucket.GroupID}, unchanged.GroupIDs)

	account.Credentials["model_mapping"] = map[string]any{"alias": "second"}
	account.Extra["quota_used"] = 10.0
	require.NoError(t, writer.SetAccount(ctx, &account))
	fresh := read()
	require.Equal(t, "second", fresh.GetMappedModel("alias"))
	require.False(t, fresh.IsSchedulable())
	usedAt := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, writer.UpdateLastUsed(ctx, map[int64]time.Time{account.ID: usedAt}))
	require.Equal(t, usedAt, *read().LastUsedAt)
	require.NoError(t, writer.DeleteAccount(ctx, account.ID))
	_, hit, err := reader.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.False(t, hit)
}

// 模拟 Redis 已接收账号写入、但最后的修订号命令发送失败，保留真实 Redis 的部分写入语义。
type schedulerPartialWriteFailureHook struct{ armed atomic.Bool }

func (*schedulerPartialWriteFailureHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (*schedulerPartialWriteFailureHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return next
}
func (h *schedulerPartialWriteFailureHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, commands []redis.Cmder) error {
		if len(commands) >= 3 && h.armed.CompareAndSwap(true, false) {
			if err := next(ctx, commands[:2]); err != nil && !errors.Is(err, redis.Nil) {
				return err
			}
			err := errors.New("test: metadata revision write interrupted")
			for _, command := range commands[2:] {
				command.SetErr(err)
			}
			return err
		}
		return next(ctx, commands)
	}
}

func TestSchedulerCacheRealRedisPartialWriteInvalidatesOtherInstance(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	fault := &schedulerPartialWriteFailureHook{}
	rdb.AddHook(fault)
	writer := NewSchedulerCache(rdb).(*schedulerCache)
	reader := NewSchedulerCache(rdb)
	bucket := service.SchedulerBucket{GroupID: 9822, Platform: service.PlatformAnthropic, Mode: service.SchedulerModeSingle}
	account := service.Account{ID: 98220, Name: "before", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true}
	token, err := writer.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, writer.SetSnapshot(ctx, bucket, token, []service.Account{account}))
	snapshot, hit, err := reader.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, "before", snapshot[0].Name)
	account.Name = "after"
	account.Schedulable = false
	fault.armed.Store(true)
	require.ErrorContains(t, writer.SetAccount(ctx, &account), "metadata revision write interrupted")
	// 后台全局修订号应使另一实例立即重读已落库的部分写入，不必等待十秒兜底。
	require.Eventually(t, func() bool {
		snapshot, hit, err := reader.GetSnapshot(ctx, bucket)
		return err == nil && hit && len(snapshot) == 1 && snapshot[0].Name == "after" && !snapshot[0].Schedulable && !writer.globalRevisionWriting.Load()
	}, 3*time.Second, 10*time.Millisecond)
}
