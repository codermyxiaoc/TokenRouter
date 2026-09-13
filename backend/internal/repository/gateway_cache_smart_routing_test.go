package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

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
