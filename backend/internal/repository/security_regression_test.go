//go:build unit

package repository

import (
	"context"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

// 用隔离 Redis 模拟并发请求乱序抵达，撤销时间点不应倒退。
func TestRegressionRevocationCutoffMustBeMonotonic(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &refreshTokenCache{rdb: client}
	ctx := context.Background()
	older := time.Now().Add(-2 * time.Second)
	newer := older.Add(time.Second)
	require.NoError(t, cache.RevokeUserAccessTokensBefore(ctx, 719, newer, time.Hour))
	// 较早开始、较晚写入的撤销覆盖新时间点，是分布式并发的一种合法执行顺序。
	require.NoError(t, cache.RevokeUserAccessTokensBefore(ctx, 719, older, time.Hour))
	actual, err := cache.UserAccessTokensRevokedBefore(ctx, 719)
	require.NoError(t, err)
	t.Logf("newer=%s delayed_older=%s actual=%s", newer.Format(time.RFC3339Nano), older.Format(time.RFC3339Nano), actual.Format(time.RFC3339Nano))
	require.True(t, !actual.Before(newer), "迟到的撤销写入重新放行本已撤销的令牌")
}

// 并发记录独立会话后检查所有记录以及过期回收，不访问共享 Redis。
func TestRegressionConcurrentSessionRevocation(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &refreshTokenCache{rdb: client}
	var wg sync.WaitGroup
	for i := 0; i < 128; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			require.NoError(t, cache.RevokeAccessSession(context.Background(), string(rune(1000+i)), time.Minute))
		}(i)
	}
	wg.Wait()
	for i := 0; i < 128; i++ {
		ok, err := cache.IsAccessSessionRevoked(context.Background(), string(rune(1000+i)))
		require.NoError(t, err)
		require.True(t, ok)
	}
	mini.FastForward(61 * time.Second)
	ok, err := cache.IsAccessSessionRevoked(context.Background(), string(rune(1000)))
	require.NoError(t, err)
	require.False(t, ok)
}
