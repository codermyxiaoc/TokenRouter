package repository

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// 并发请求必须取得唯一递增序号，60 次阈值只能放行 120 次竞争中的前 60 次。
func TestAPIKeyCreateCounterConcurrentWindow(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &apiKeyCache{rdb: client}
	counts := make([]int64, 120)
	errors := make([]error, len(counts))
	var workers sync.WaitGroup
	for i := range counts {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			counts[i], errors[i] = cache.IncrementCreateCount(context.Background(), 7, time.Hour)
		}(i)
	}
	workers.Wait()
	for _, err := range errors {
		require.NoError(t, err)
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i] < counts[j] })
	allowed := 0
	for i, count := range counts {
		require.Equal(t, int64(i+1), count)
		if count <= 60 {
			allowed++
		}
	}
	require.Equal(t, 60, allowed)
	require.Equal(t, time.Hour, server.TTL("apikey:create_count:7"))
	server.FastForward(time.Minute)
	_, err := cache.IncrementCreateCount(context.Background(), 7, time.Hour)
	require.NoError(t, err)
	require.Equal(t, 59*time.Minute, server.TTL("apikey:create_count:7"))
}

// 固定窗口不因后续创建而续期，自定义密钥失败计数的清理也不能返还创建额度。
func TestAPIKeyCreateCounterFixedWindowAndIsolation(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &apiKeyCache{rdb: client}
	ctx := context.Background()
	count, err := cache.IncrementCreateCount(ctx, 7, time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	server.FastForward(20 * time.Minute)
	count, err = cache.IncrementCreateCount(ctx, 7, time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(2), count)
	require.Equal(t, 40*time.Minute, server.TTL("apikey:create_count:7"))
	require.NoError(t, cache.IncrementCreateAttemptCount(ctx, 7))
	require.NoError(t, cache.DeleteCreateAttemptCount(ctx, 7))
	count, err = cache.IncrementCreateCount(ctx, 7, time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(3), count)
	count, err = cache.IncrementCreateCount(ctx, 8, time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	server.FastForward(40 * time.Minute)
	count, err = cache.IncrementCreateCount(ctx, 7, time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
}
