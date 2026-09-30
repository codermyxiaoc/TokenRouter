//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// 混跑旧程序时，旧来源的固定缓存键不能覆盖本项目的发布结果，也不能被新程序读入。
func TestUpdateCacheIsolatesLegacySource(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	cache := NewUpdateCache(client)
	require.NoError(t, client.Set(ctx, "update:latest", "legacy release", time.Hour).Err())
	_, err := cache.GetUpdateInfo(ctx)
	require.ErrorIs(t, err, redis.Nil)
	require.NoError(t, cache.SetUpdateInfo(ctx, "TokenRouter release", 20*time.Minute))
	require.NoError(t, client.Set(ctx, "update:latest", "upstream overwrite", time.Hour).Err())
	got, err := cache.GetUpdateInfo(ctx)
	require.NoError(t, err)
	require.Equal(t, "TokenRouter release", got)
	require.Equal(t, 20*time.Minute, server.TTL("update:latest:codermyxiaoc/TokenRouter:v2"))
	server.FastForward(20 * time.Minute)
	_, err = cache.GetUpdateInfo(ctx)
	require.ErrorIs(t, err, redis.Nil, "新来源过期后也不能读取仍存活的旧来源结果")
}
