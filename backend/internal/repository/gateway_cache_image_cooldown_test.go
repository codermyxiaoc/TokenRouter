package repository

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func imageCooldownTestCache(t *testing.T, address string) *gatewayCache {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: address, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second})
	t.Cleanup(func() { _ = client.Close() })
	return NewGatewayCache(client).(*gatewayCache)
}

// 多实例共享同一真实窗口；晚写只能保留剩余时间，不能重新从十秒开始。
func TestImageModelCooldownSharedScopeAndNoExtension(t *testing.T) {
	server := miniredis.RunT(t)
	first, second := imageCooldownTestCache(t, server.Addr()), imageCooldownTestCache(t, server.Addr())
	ctx := context.Background()
	require.NoError(t, first.CooldownImageModel(ctx, 12, "mapped", "generate", 10*time.Second))
	key := imageModelCooldownKey(12, "mapped", "generate")
	server.FastForward(7 * time.Second)
	var wg sync.WaitGroup
	errors := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errors <- second.CooldownImageModel(ctx, 12, "mapped", "generate", time.Minute)
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.Equal(t, 3*time.Second, server.TTL(key))
	for _, scope := range []struct {
		account          int64
		model, operation string
	}{{13, "mapped", "generate"}, {12, "alias", "generate"}, {12, "mapped", "edit"}, {12, "mapped", "inpaint"}} {
		remaining, err := second.GetImageModelCooldown(ctx, scope.account, scope.model, scope.operation)
		require.NoError(t, err)
		require.Zero(t, remaining)
	}
	second.rdb = nil
	remaining, err := second.GetImageModelCooldown(ctx, 12, "mapped", "generate")
	require.Error(t, err)
	require.Positive(t, remaining)
	require.LessOrEqual(t, remaining, 3*time.Second, "本地降级跟随Redis剩余TTL")
	server.FastForward(3 * time.Second)
	remaining, err = first.GetImageModelCooldown(ctx, 12, "mapped", "generate")
	require.NoError(t, err)
	require.Zero(t, remaining)
	require.NoError(t, first.CooldownImageModel(ctx, 12, "mapped", "generate", time.Second))
	require.Equal(t, time.Second, server.TTL(key), "新失败允许在过期后创建新窗口")
}

// 本地降级只排除已知故障账号，容量与时间都有限，不会把全部候选当作冷却。
func TestImageModelCooldownFallbackCapacityAndExpiry(t *testing.T) {
	cache := &gatewayCache{}
	ctx := context.Background()
	require.Error(t, cache.CooldownImageModel(ctx, 1, "model", "generate", time.Minute))
	before := cache.imageModelCooldowns.get(imageModelCooldownKey(1, "model", "generate"), time.Now())
	require.Error(t, cache.CooldownImageModel(ctx, 1, "model", "generate", time.Hour))
	after := cache.imageModelCooldowns.get(imageModelCooldownKey(1, "model", "generate"), time.Now())
	require.LessOrEqual(t, after, before)
	remaining, err := cache.GetImageModelCooldown(ctx, 2, "model", "generate")
	require.Error(t, err)
	require.Zero(t, remaining)
	now := time.Now()
	for i := 0; i < imageModelCooldownLocalLimit+16; i++ {
		cache.imageModelCooldowns.observe(fmt.Sprint(i), now.Add(time.Minute), now, true)
	}
	require.LessOrEqual(t, len(cache.imageModelCooldowns.entries), imageModelCooldownLocalLimit)
	require.Zero(t, cache.imageModelCooldowns.get("100", now.Add(2*time.Minute)))
}

func TestImageModelCooldownNormalizesScopeAndIgnoresCallerCancellation(t *testing.T) {
	server := miniredis.RunT(t)
	cache := imageCooldownTestCache(t, server.Addr())
	ctx := context.Background()
	require.NoError(t, cache.CooldownImageModel(ctx, 1, " final-model ", " Generate ", time.Second))
	remaining, err := cache.GetImageModelCooldown(ctx, 1, "final-model", "generate")
	require.NoError(t, err)
	require.Positive(t, remaining)
	require.Len(t, server.Keys(), 1)
	for _, operation := range []string{"tts", "text", "chat"} {
		require.NoError(t, cache.CooldownImageModel(ctx, 1, "model", operation, time.Second))
		remaining, err = cache.GetImageModelCooldown(ctx, 1, "model", operation)
		require.NoError(t, err)
		require.Zero(t, remaining)
	}
	for _, alreadyExpired := range []bool{false, true} {
		var stopped context.Context
		if alreadyExpired {
			var cancel context.CancelFunc
			stopped, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
			defer cancel()
		} else {
			var cancel context.CancelFunc
			stopped, cancel = context.WithCancel(ctx)
			cancel()
		}
		_, err = cache.GetImageModelCooldown(stopped, 1, "final-model", "generate")
		require.Error(t, err)
		require.Error(t, cache.CooldownImageModel(stopped, 2, "other", "generate", time.Second))
		require.True(t, cache.imageModelCooldowns.redisAvailable(time.Now()))
		require.Zero(t, cache.imageModelCooldowns.get(imageModelCooldownKey(2, "other", "generate"), time.Now()))
	}
	require.Len(t, server.Keys(), 1)
	remaining, err = cache.GetImageModelCooldown(ctx, 1, "final-model", "generate")
	require.NoError(t, err)
	require.Positive(t, remaining)
	require.NoError(t, cache.CooldownImageModel(ctx, 2, "model", "generate", 0))
	require.Len(t, server.Keys(), 1, "仓储零时长不得创建冷却窗口")
}

type imageCooldownReadDelayHook struct{ delay time.Duration }

func (h imageCooldownReadDelayHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h imageCooldownReadDelayHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h imageCooldownReadDelayHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if cmd.Name() == "pttl" {
			timer := time.NewTimer(h.delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
			}
		}
		return err
	}
}

// 返回旧TTL后人为延迟，剩余时间必须扣除通信耗时，不能在函数返回时重新延长。
func TestImageModelCooldownReadSubtractsLatency(t *testing.T) {
	server := miniredis.RunT(t)
	cache := imageCooldownTestCache(t, server.Addr())
	ctx := context.Background()
	require.NoError(t, cache.CooldownImageModel(ctx, 1, "model", "generate", time.Second))
	cache.smartRoutingRedisClient().AddHook(imageCooldownReadDelayHook{delay: 100 * time.Millisecond})
	remaining, err := cache.GetImageModelCooldown(ctx, 1, "model", "generate")
	require.NoError(t, err)
	require.Positive(t, remaining)
	require.LessOrEqual(t, remaining, 910*time.Millisecond)
}

// 本地TCP接受连接后不回应，验证socket预算和故障熔断，不依赖真实Redis服务。
func TestImageModelCooldownSocketBudgetAndCircuit(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(fmt.Sprint(write), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			stop, finished, accepted := make(chan struct{}), make(chan struct{}), make(chan struct{})
			go func() {
				defer close(finished)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				close(accepted)
				<-stop
			}()
			t.Cleanup(func() { close(stop); _ = listener.Close(); <-finished })
			cache := imageCooldownTestCache(t, listener.Addr().String())
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			if write {
				err = cache.CooldownImageModel(ctx, 1, "model", "generate", time.Second)
			} else {
				_, err = cache.GetImageModelCooldown(ctx, 1, "model", "generate")
			}
			require.Error(t, err)
			require.Less(t, time.Since(start), 800*time.Millisecond)
			select {
			case <-accepted:
			default:
				t.Fatal("必须实际建立TCP连接")
			}
			start = time.Now()
			for i := 0; i < 20; i++ {
				remaining, err := cache.GetImageModelCooldown(context.Background(), int64(i+100), "model", "generate")
				require.Error(t, err)
				require.Zero(t, remaining)
			}
			require.Less(t, time.Since(start), 200*time.Millisecond, "故障期间候选查询不能逐一等待Redis")
			require.False(t, cache.rdb.Options().ContextTimeoutEnabled)
			require.Equal(t, 5*time.Second, cache.rdb.Options().ReadTimeout)
		})
	}
}
