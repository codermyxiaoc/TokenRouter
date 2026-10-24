package repository

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// 假 Redis 接受 TCP 后不回应，覆盖连接正常但 socket 读取悬挂，不能只依赖立即断连。
func TestSmartRoutingRedisBudgetBoundsUnresponsiveSocket(t *testing.T) {
	operations := map[string]func(context.Context, *gatewayCache) error{
		"cooldown_read": func(ctx context.Context, c *gatewayCache) error {
			_, err := c.GetSmartRoutingAccountCooldowns(ctx, 1, []int64{2}, "model", "/v1/responses")
			return err
		},
		"cooldown_write": func(ctx context.Context, c *gatewayCache) error {
			return c.CooldownSmartRoutingAccounts(ctx, 1, []int64{2}, "model", "/v1/responses", time.Minute)
		},
		"continuation_owner": func(ctx context.Context, c *gatewayCache) error {
			_, err := c.GetSessionAccountID(ctx, 1, "response-owner")
			return err
		},
		"session_claim": func(ctx context.Context, c *gatewayCache) error {
			_, err := c.ClaimSmartRoutingSessionOwner(ctx, 1, "openai", "session", 2, "token", time.Minute)
			return err
		},
		"session_finish": func(ctx context.Context, c *gatewayCache) error {
			return c.FinishSmartRoutingSessionOwner(ctx, 1, "openai", "session", 2, "token", true)
		},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			stop, finished, accepted := make(chan struct{}), make(chan struct{}), make(chan struct{})
			go func() {
				defer close(finished)
				connection, acceptErr := listener.Accept()
				if acceptErr != nil {
					return
				}
				defer connection.Close()
				close(accepted)
				<-stop
			}()
			t.Cleanup(func() {
				close(stop)
				_ = listener.Close()
				<-finished
			})
			client := redis.NewClient(&redis.Options{Addr: listener.Addr().String(), ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second})
			t.Cleanup(func() { _ = client.Close() })
			cache := NewGatewayCache(client).(*gatewayCache)
			require.Same(t, client, cache.smartRoutingContextRedisClient(context.Background()), "普通会话保留原客户端")
			ctx, cancel := context.WithTimeout(service.WithSmartRoutingScope(context.Background()), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			require.Error(t, operation(ctx, cache))
			require.Less(t, time.Since(start), 800*time.Millisecond, "必须由请求预算终止读取，不能等待原客户端 5 秒超时")
			select {
			case <-accepted:
			default:
				t.Fatal("用例必须实际建立 TCP 连接并等待无响应上游")
			}
			require.False(t, client.Options().ContextTimeoutEnabled, "局部视图不得修改共享客户端")
			require.Equal(t, 5*time.Second, client.Options().ReadTimeout)
		})
	}
}

func TestSmartRoutingCooldownSharedTTLAndScope(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	first := NewGatewayCache(client).(*gatewayCache)
	second := NewGatewayCache(client).(*gatewayCache)
	ctx := context.Background()
	require.NoError(t, first.CooldownSmartRoutingGroup(ctx, 11, 21, time.Minute))
	remaining, err := second.GetSmartRoutingCooldown(ctx, 11, 21)
	require.NoError(t, err)
	require.Equal(t, time.Minute, remaining, "不同缓存实例共享 Redis 冷却")
	require.NoError(t, second.CooldownSmartRoutingGroup(ctx, 11, 21, 5*time.Second))
	require.Equal(t, time.Minute, server.TTL(smartRoutingCooldownKey(11, 21)))
	for _, ids := range [][2]int64{{12, 21}, {11, 22}} {
		remaining, err = second.GetSmartRoutingCooldown(ctx, ids[0], ids[1])
		require.NoError(t, err)
		require.Zero(t, remaining)
	}
	require.NoError(t, first.CooldownSmartRoutingGroup(ctx, 12, 21, 0))
	require.False(t, server.Exists(smartRoutingCooldownKey(12, 21)))
	server.FastForward(time.Minute)
	remaining, err = second.GetSmartRoutingCooldown(ctx, 11, 21)
	require.NoError(t, err)
	require.Zero(t, remaining)
}

func TestSmartRoutingCooldownBoundedLocalFallback(t *testing.T) {
	cache := &gatewayCache{}
	ctx := context.Background()
	require.Error(t, cache.CooldownSmartRoutingGroup(ctx, 1, 2, time.Minute))
	remaining, err := cache.GetSmartRoutingCooldown(ctx, 1, 2)
	require.Error(t, err)
	require.Positive(t, remaining, "Redis 失效时保留本实例已知失败")
	remaining, err = cache.GetSmartRoutingCooldown(ctx, 1, 3)
	require.Error(t, err)
	require.Zero(t, remaining, "不能将未知候选伪装成冷却")
	now := time.Now()
	for index := int64(0); index < smartRoutingLocalCooldownLimit+10; index++ {
		cache.smartRoutingCooldowns.set([2]int64{index + 10, 2}, now.Add(time.Minute), now)
	}
	require.LessOrEqual(t, len(cache.smartRoutingCooldowns.entries), smartRoutingLocalCooldownLimit)
	require.Zero(t, cache.smartRoutingCooldowns.get([2]int64{10, 2}, now.Add(2*time.Minute)))
}

func TestSmartRoutingAccountCooldownScopeAndBoundedRecovery(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	first := NewGatewayCache(client).(*gatewayCache)
	second := NewGatewayCache(client).(*gatewayCache)
	ctx := context.Background()
	require.NoError(t, first.CooldownSmartRoutingAccounts(ctx, 11, []int64{21}, "model-a", "/v1/responses", time.Minute))
	remaining, err := second.GetSmartRoutingAccountCooldowns(ctx, 11, []int64{21, 22}, "model-a", "/v1/responses")
	require.NoError(t, err)
	require.LessOrEqual(t, remaining[21], time.Minute)
	require.Greater(t, remaining[21], 59*time.Second)
	require.Zero(t, remaining[22])
	for _, test := range []struct {
		key             int64
		model, endpoint string
	}{{12, "model-a", "/v1/responses"}, {11, "model-b", "/v1/responses"}, {11, "model-a", "/v1/chat/completions"}} {
		remaining, err = second.GetSmartRoutingAccountCooldowns(ctx, test.key, []int64{21}, test.model, test.endpoint)
		require.NoError(t, err)
		require.Zero(t, remaining[21])
	}
	server.FastForward(20 * time.Second)
	require.NoError(t, second.CooldownSmartRoutingAccounts(ctx, 11, []int64{21}, "model-a", "/v1/responses", time.Minute))
	remaining, err = first.GetSmartRoutingAccountCooldowns(ctx, 11, []int64{21}, "model-a", "/v1/responses")
	require.NoError(t, err)
	require.LessOrEqual(t, remaining[21], 40*time.Second, "不能把读取耗时加入剩余时间")
	require.Greater(t, remaining[21], 39*time.Second)
	server.FastForward(40 * time.Second)
	remaining, err = second.GetSmartRoutingAccountCooldowns(ctx, 11, []int64{21}, "model-a", "/v1/responses")
	require.NoError(t, err)
	require.Zero(t, remaining[21])
	require.Zero(t, second.smartRoutingAccountCooldowns.get(smartRoutingAccountCooldownKey(11, 21, "model-a", "/v1/responses"), time.Now()), "共享状态到期同步清理本地降级数据")
}

func TestSmartRoutingAccountCooldownLocalFallbackBoundedAndUnknownOpen(t *testing.T) {
	cache := &gatewayCache{}
	ctx := context.Background()
	require.Error(t, cache.CooldownSmartRoutingAccounts(ctx, 1, []int64{2}, "model", "/v1/responses", time.Minute))
	remaining, err := cache.GetSmartRoutingAccountCooldowns(ctx, 1, []int64{2, 3}, "model", "/v1/responses")
	require.Error(t, err)
	require.Positive(t, remaining[2])
	require.Zero(t, remaining[3])
	now := time.Now()
	for id := int64(10); id < smartRoutingLocalCooldownLimit+30; id++ {
		cache.smartRoutingAccountCooldowns.failed(smartRoutingAccountCooldownKey(1, id, "model", "endpoint"), time.Minute, now, time.Time{}, 0)
	}
	require.LessOrEqual(t, len(cache.smartRoutingAccountCooldowns.entries), smartRoutingLocalCooldownLimit)
	require.NotEqual(t, smartRoutingAccountCooldownKey(1, 2, "ab", "c"), smartRoutingAccountCooldownKey(1, 2, "a", "bc"))
}

func TestSmartRoutingSessionClaimReleaseCommitAndOtherObserver(t *testing.T) {
	for _, action := range []string{"release", "commit", "observed", "refreshed", "wrong-token"} {
		t.Run(action, func(t *testing.T) {
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			cache := NewGatewayCache(client).(*gatewayCache)
			ctx := context.Background()
			written, err := cache.ClaimSmartRoutingSessionOwner(ctx, 1, "openai", "session", 10, "attempt-token", time.Hour)
			require.NoError(t, err)
			require.True(t, written)
			key := buildSessionOwnerKey(1, "openai", "session")
			value, err := server.Get(key)
			require.NoError(t, err)
			require.Equal(t, "10", value, "普通会话读取仍使用数字 owner")
			release, token := true, "attempt-token"
			switch action {
			case "commit":
				release = false
			case "observed":
				owner, err := cache.GetSessionOwnerGroupID(ctx, 1, "openai", "session")
				require.NoError(t, err)
				require.Equal(t, int64(10), owner)
			case "refreshed":
				require.NoError(t, cache.RefreshSessionOwnerTTL(ctx, 1, "openai", "session", time.Hour))
			case "wrong-token":
				token = "another-attempt"
			}
			require.NoError(t, cache.FinishSmartRoutingSessionOwner(ctx, 1, "openai", "session", 10, token, release))
			require.Equal(t, action != "release", server.Exists(key))
			if action == "release" {
				written, err = cache.ClaimSmartRoutingSessionOwner(ctx, 1, "openai", "session", 20, "next-attempt", time.Hour)
				require.NoError(t, err)
				require.True(t, written, "安全失败释放后下一候选可建立新的隔离归属")
			}
		})
	}
}

func TestSmartRoutingSessionCannotReleaseExistingOwner(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewGatewayCache(client).(*gatewayCache)
	ctx := context.Background()
	written, err := cache.SetSessionOwnerGroupID(ctx, 1, "openai", "existing", 10, time.Hour)
	require.NoError(t, err)
	require.True(t, written)
	written, err = cache.ClaimSmartRoutingSessionOwner(ctx, 1, "openai", "existing", 20, "token", time.Hour)
	require.NoError(t, err)
	require.False(t, written)
	require.NoError(t, cache.FinishSmartRoutingSessionOwner(ctx, 1, "openai", "existing", 10, "token", true))
	owner, err := cache.GetSessionOwnerGroupID(ctx, 1, "openai", "existing")
	require.NoError(t, err)
	require.Equal(t, int64(10), owner)
}
