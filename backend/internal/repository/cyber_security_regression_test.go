//go:build unit

package repository

import (
	"context"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

// 改短配置 TTL 后的新命中不能提前删除其他来源仍未过期的历史匹配索引。
func TestRegressionCyberShorterTTLDoesNotRemoveLongerActiveBlockIndex(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewGatewayCache(client).(service.CyberSessionBlockStore)
	ctx := context.Background()
	require.NoError(t, store.SetCyberSessionBlocked(ctx, service.CyberSessionBlock{APIKeyID: 71, ScopeKey: "long-scope", Keys: []string{"long-block"}, TranscriptLengths: []int{3}}, time.Hour))
	require.NoError(t, store.SetCyberSessionBlocked(ctx, service.CyberSessionBlock{APIKeyID: 71, ScopeKey: "short-scope", Keys: []string{"short-block"}, TranscriptLengths: []int{8}}, time.Minute))
	server.FastForward(2 * time.Minute)
	require.True(t, server.Exists(cyberSessionScopePrefix+"long-scope"))
	exact, err := store.FindCyberSessionBlocked(ctx, []string{"long-block"})
	require.NoError(t, err)
	require.Equal(t, "long-block", exact)
	lengths, err := store.CyberSessionTranscriptLengths(ctx, 71, "long-scope")
	require.NoError(t, err)
	t.Logf("old_scope_ttl=%s old_exact_ttl=%s index_exists=%v lengths=%v", server.TTL(cyberSessionScopePrefix+"long-scope"), server.TTL(cyberSessionBlockPrefix+"long-block"), server.Exists(cyberSessionTranscriptLengthsKey(71)), lengths)
	require.Contains(t, lengths, 3, "旧 scope 和精确键还有58分钟有效期，但索引已提前过期")
}

// 在真实 Redis 命令层模拟升级前只有 scope 和精确键的数据，确认新代码是否能检索。
func TestRegressionCyberLegacyRedisDataHasRetrievablePrefixes(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewGatewayCache(client).(service.CyberSessionBlockStore)
	ctx := context.Background()
	body := []byte(`{"input":[{"role":"user","content":"synthetic-history"}]}`)
	block := service.CyberSessionTranscriptBlocks(71, body)[0]
	scope := service.CyberSessionScopeKey(71, "203.0.113.7", "client/1.0")
	require.NoError(t, client.Set(ctx, cyberSessionBlockPrefix+block.Key, "1", time.Hour).Err())
	require.NoError(t, client.Set(ctx, cyberSessionScopePrefix+scope, "1", time.Hour).Err())
	// 升级前只有 scope 与精确键：存储报告旧数据，由服务层回退到有界前缀查询。
	_, err := store.CyberSessionTranscriptLengths(ctx, 71, scope)
	require.ErrorIs(t, err, service.ErrCyberSessionLegacyScope)
	// 旧精确键的哈希算法未变：服务层回退计算出的前缀 key 能在真实 Redis 中命中。
	matched, err := store.FindCyberSessionBlocked(ctx, []string{block.Key})
	require.NoError(t, err)
	require.Equal(t, block.Key, matched, "升级前保留的阻断键无法再进入新的历史前缀查询")
}
