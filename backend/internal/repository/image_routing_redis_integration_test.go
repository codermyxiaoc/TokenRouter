//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 真实 Redis 上验证两个实例共享绝对冷却窗口，迟到的并发失败不能复活已到期窗口。
func TestSmartRoutingRealRedisConcurrentExpiryAndImageScope(t *testing.T) {
	// 缓存的 WithTimeout 客户端视图不会继承测试 prefixHook；用唯一账号/Key 和显式清理隔离。
	rdb := integrationRedis
	keyID, accountID := time.Now().UnixNano(), time.Now().UnixNano()+100
	key := smartRoutingAccountCooldownKey(keyID, accountID, "mapped", "/v1/images/generations")
	t.Cleanup(func() {
		require.NoError(t, rdb.Unlink(context.Background(), key, key+":window", imageModelCooldownKey(accountID, "mapped", "edit")).Err())
	})
	first, second := NewGatewayCache(rdb).(*gatewayCache), NewGatewayCache(rdb).(*gatewayCache)
	ctx := context.Background()
	old := service.WithSmartRoutingAccountCooldown(service.WithSmartRoutingAccountFailures(ctx), keyID, time.Second, "mapped", "/v1/images/generations")
	_, err := second.GetSmartRoutingAccountCooldowns(old, keyID, []int64{accountID}, "mapped", "/v1/images/generations")
	require.NoError(t, err)
	require.NoError(t, first.CooldownSmartRoutingAccounts(ctx, keyID, []int64{accountID}, "mapped", "/v1/images/generations", 250*time.Millisecond))
	require.Eventually(t, func() bool { return rdb.Exists(ctx, key).Val() == 0 }, 3*time.Second, 20*time.Millisecond)
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for range 64 {
		wg.Go(func() {
			errs <- second.CooldownSmartRoutingAccounts(old, keyID, []int64{accountID}, "mapped", "/v1/images/generations", time.Second)
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Zero(t, rdb.Exists(ctx, key).Val())
	remaining, err := second.GetSmartRoutingAccountCooldowns(ctx, keyID, []int64{accountID, accountID + 1}, "mapped", "/v1/images/generations")
	require.NoError(t, err)
	require.Zero(t, remaining[accountID])
	require.Zero(t, remaining[accountID+1])
	// 图片健康范围不含用户 Key，但必须区分账号、最终模型以及生成/编辑操作。
	require.NoError(t, first.CooldownImageModel(ctx, accountID, "mapped", "edit", time.Second))
	before, err := second.GetImageModelCooldown(ctx, accountID, "mapped", "edit")
	require.NoError(t, err)
	require.Positive(t, before)
	sharedBefore, err := rdb.PTTL(ctx, imageModelCooldownKey(accountID, "mapped", "edit")).Result()
	require.NoError(t, err)
	require.NoError(t, second.CooldownImageModel(ctx, accountID, "mapped", "edit", time.Hour))
	after, err := first.GetImageModelCooldown(ctx, accountID, "mapped", "edit")
	require.NoError(t, err)
	require.LessOrEqual(t, after, time.Second)
	// 不同比例的网络耗时会改变本地保守估算；原子不续期以 Redis 的实际 PTTL 为准。
	sharedAfter, err := rdb.PTTL(ctx, imageModelCooldownKey(accountID, "mapped", "edit")).Result()
	require.NoError(t, err)
	require.LessOrEqual(t, sharedAfter, sharedBefore)
	for _, scope := range []struct {
		id               int64
		model, operation string
	}{{accountID + 1, "mapped", "edit"}, {accountID, "other", "edit"}, {accountID, "mapped", "generate"}} {
		duration, err := second.GetImageModelCooldown(ctx, scope.id, scope.model, scope.operation)
		require.NoError(t, err)
		require.Zero(t, duration)
	}
	require.Eventually(t, func() bool {
		duration, err := first.GetImageModelCooldown(ctx, accountID, "mapped", "edit")
		return err == nil && duration == 0
	}, 3*time.Second, 20*time.Millisecond)
}
