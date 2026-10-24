package repository

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// 同时启动多个执行者，错误回传主协程，避免工作协程提前退出造成假通过。
func runCacheFailoverStress(t *testing.T, workers int, action func(int) error) {
	t.Helper()
	start := make(chan struct{})
	errors := make(chan error, workers)
	var pending sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		pending.Add(1)
		go func(id int) {
			defer pending.Done()
			<-start
			if err := action(id); err != nil {
				errors <- err
			}
		}(worker)
	}
	close(start)
	pending.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func TestCacheFailoverStressSharedCooldownIsolation(t *testing.T) {
	server := miniredis.RunT(t)
	caches := make([]*gatewayCache, 4)
	for index := range caches {
		client := redis.NewClient(&redis.Options{Addr: server.Addr(), PoolSize: 128})
		t.Cleanup(func() { _ = client.Close() })
		caches[index] = NewGatewayCache(client).(*gatewayCache)
	}
	ctx := context.Background()
	require.NoError(t, caches[0].CooldownSmartRoutingAccounts(ctx, 11, []int64{21}, "model", "/v1/responses", time.Minute))
	server.FastForward(20 * time.Second)
	runCacheFailoverStress(t, 128, func(id int) error {
		cache := caches[id%len(caches)]
		for iteration := 0; iteration < 8; iteration++ {
			if err := cache.CooldownSmartRoutingAccounts(ctx, 11, []int64{21}, "model", "/v1/responses", time.Minute); err != nil {
				return err
			}
			values, err := cache.GetSmartRoutingAccountCooldowns(ctx, 11, []int64{21, 22}, "model", "/v1/responses")
			if err != nil || values[21] <= 0 || values[21] > 40*time.Second || values[22] != 0 {
				return fmt.Errorf("worker=%d cooldown=%v err=%v", id, values, err)
			}
			for _, scope := range []struct {
				key             int64
				model, endpoint string
			}{{12, "model", "/v1/responses"}, {11, "other", "/v1/responses"}, {11, "model", "/v1/chat/completions"}} {
				values, err = cache.GetSmartRoutingAccountCooldowns(ctx, scope.key, []int64{21}, scope.model, scope.endpoint)
				if err != nil || values[21] != 0 {
					return fmt.Errorf("worker=%d scope leaked: %v err=%v", id, values, err)
				}
			}
		}
		return nil
	})
	require.Equal(t, 40*time.Second, server.TTL(smartRoutingAccountCooldownKey(11, 21, "model", "/v1/responses")))
	server.FastForward(40 * time.Second)
	for _, cache := range caches {
		values, err := cache.GetSmartRoutingAccountCooldowns(ctx, 11, []int64{21}, "model", "/v1/responses")
		require.NoError(t, err)
		require.Zero(t, values[21])
	}
	t.Log("CACHE_STRESS shared_cooldown concurrency=128 batches=1024 redis_operations=5120 ttl_extension=0 scope_leak=0")
}

func TestCacheFailoverStressSessionOwnership(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), PoolSize: 128})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewGatewayCache(client).(*gatewayCache)
	ctx := context.Background()
	for round := 0; round < 16; round++ {
		session := fmt.Sprintf("stress-session-%d", round)
		var winners atomic.Int64
		var winner atomic.Int64
		runCacheFailoverStress(t, 64, func(id int) error {
			claimed, err := cache.ClaimSmartRoutingSessionOwner(ctx, 11, "openai", session, int64(id+1), fmt.Sprintf("attempt-%d", id+1), time.Minute)
			if claimed {
				winners.Add(1)
				winner.Store(int64(id + 1))
			}
			return err
		})
		require.Equal(t, int64(1), winners.Load())
		owner := winner.Load()
		// 未持有尝试令牌的并发失败请求不能删除另一个请求的会话归属。
		runCacheFailoverStress(t, 64, func(id int) error {
			return cache.FinishSmartRoutingSessionOwner(ctx, 11, "openai", session, owner, fmt.Sprintf("wrong-%d", id), true)
		})
		require.True(t, server.Exists(buildSessionOwnerKey(11, "openai", session)))
		if round%2 == 0 {
			require.NoError(t, cache.FinishSmartRoutingSessionOwner(ctx, 11, "openai", session, owner, fmt.Sprintf("attempt-%d", owner), true))
			require.False(t, server.Exists(buildSessionOwnerKey(11, "openai", session)))
		} else {
			// 已被其他请求读取的归属视为已提交，原执行者随后失败不能撤回。
			observed, err := cache.GetSessionOwnerGroupID(ctx, 11, "openai", session)
			require.NoError(t, err)
			require.Equal(t, owner, observed)
			require.NoError(t, cache.FinishSmartRoutingSessionOwner(ctx, 11, "openai", session, owner, fmt.Sprintf("attempt-%d", owner), true))
			require.True(t, server.Exists(buildSessionOwnerKey(11, "openai", session)))
		}
	}
	t.Log("CACHE_STRESS session concurrency=64 rounds=16 claims=1024 winners=16 unauthorized_release=0")
}

func TestCacheFailoverStressLocalBound(t *testing.T) {
	cache := &gatewayCache{}
	ctx := context.Background()
	runCacheFailoverStress(t, 128, func(id int) error {
		for iteration := 0; iteration < 32; iteration++ {
			account := int64(id*32 + iteration + 1)
			if err := cache.CooldownSmartRoutingAccounts(ctx, 11, []int64{account}, "model", "/v1/responses", time.Minute); err == nil {
				return fmt.Errorf("expected Redis unavailable")
			}
			values, err := cache.GetSmartRoutingAccountCooldowns(ctx, 11, []int64{999999}, "model", "/v1/responses")
			if err == nil || values[999999] != 0 {
				return fmt.Errorf("unknown account was incorrectly excluded: %v, %v", values, err)
			}
		}
		return nil
	})
	cache.smartRoutingAccountCooldowns.mu.Lock()
	count := len(cache.smartRoutingAccountCooldowns.entries)
	cache.smartRoutingAccountCooldowns.mu.Unlock()
	require.LessOrEqual(t, count, smartRoutingLocalCooldownLimit)
	t.Logf("CACHE_STRESS local_fallback concurrency=128 writes=4096 retained=%d unknown_exclusions=0", count)
}

func TestCacheFailoverStressUnresponsiveRedis(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	stop := make(chan struct{})
	var accepted atomic.Int64
	var connections sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			connections.Add(1)
			go func() {
				defer connections.Done()
				defer connection.Close()
				<-stop
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-acceptDone
		close(stop)
		connections.Wait()
	})
	client := redis.NewClient(&redis.Options{Addr: listener.Addr().String(), PoolSize: 128, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewGatewayCache(client).(*gatewayCache)
	started := time.Now()
	runCacheFailoverStress(t, 64, func(id int) error {
		ctx, cancel := context.WithTimeout(service.WithSmartRoutingScope(context.Background()), 150*time.Millisecond)
		defer cancel()
		_, err := cache.GetSmartRoutingAccountCooldowns(ctx, 11, []int64{int64(id + 1)}, "model", "/v1/responses")
		if err == nil {
			return fmt.Errorf("unresponsive Redis unexpectedly succeeded")
		}
		return nil
	})
	elapsed := time.Since(started)
	require.Positive(t, accepted.Load())
	require.Less(t, elapsed, 2*time.Second, "并发时也应受短预算约束，不能等待原客户端 5 秒 socket 超时")
	require.Equal(t, 5*time.Second, client.Options().ReadTimeout)
	require.False(t, client.Options().ContextTimeoutEnabled)
	t.Logf("CACHE_STRESS blackhole concurrency=64 elapsed_ms=%d accepted=%d", elapsed.Milliseconds(), accepted.Load())
}
