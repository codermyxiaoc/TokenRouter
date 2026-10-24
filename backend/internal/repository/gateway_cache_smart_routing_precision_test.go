package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func precisionSmartContext() context.Context {
	return service.WithSmartRoutingAccountCooldown(service.WithSmartRoutingAccountFailures(context.Background()), 11, 10*time.Second, "model", "/v1/responses")
}

func TestSmartRoutingPrecisionLateRequestsCannotReopenExpiredWindow(t *testing.T) {
	server := miniredis.RunT(t)
	base := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	server.SetTime(base)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	first, second := NewGatewayCache(client).(*gatewayCache), NewGatewayCache(client).(*gatewayCache)
	oldRequest := precisionSmartContext()
	_, err := second.GetSmartRoutingAccountCooldowns(oldRequest, 11, []int64{21}, "model", "/v1/responses")
	require.NoError(t, err)
	require.Empty(t, server.Keys(), "健康读取不得创建窗口或冷却键")
	server.SetTime(base.Add(time.Millisecond))
	require.NoError(t, first.CooldownSmartRoutingAccounts(context.Background(), 11, []int64{21}, "model", "/v1/responses", 10*time.Second))
	key := smartRoutingAccountCooldownKey(11, 21, "model", "/v1/responses")
	server.FastForward(10 * time.Second)
	server.SetTime(base.Add(11 * time.Second))
	var workers sync.WaitGroup
	errors := make(chan error, 32)
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			errors <- second.CooldownSmartRoutingAccounts(oldRequest, 11, []int64{21}, "model", "/v1/responses", 10*time.Second)
		}()
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.False(t, server.Exists(key), "同一旧批次在途失败不能在到期后重新冷却")
	require.True(t, server.Exists(key+":window"))
	fresh := precisionSmartContext()
	_, err = first.GetSmartRoutingAccountCooldowns(fresh, 11, []int64{21}, "model", "/v1/responses")
	require.NoError(t, err)
	require.NoError(t, first.CooldownSmartRoutingAccounts(fresh, 11, []int64{21}, "model", "/v1/responses", 10*time.Second))
	require.Equal(t, 10*time.Second, server.TTL(key), "新请求的真实失败仍可开启新窗口")
	// marker 过期也不能让超过保留期的旧入场票据重新生效。
	server.FastForward(smartRoutingWindowRetention + time.Second)
	server.SetTime(base.Add(smartRoutingWindowRetention + 30*time.Second))
	require.NoError(t, second.CooldownSmartRoutingAccounts(oldRequest, 11, []int64{21}, "model", "/v1/responses", 10*time.Second))
	require.False(t, server.Exists(key))
}

func TestSmartRoutingPrecisionExistingSharedTTLControlsLocalFallback(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	first, second := NewGatewayCache(client).(*gatewayCache), NewGatewayCache(client).(*gatewayCache)
	ctx := context.Background()
	require.NoError(t, first.CooldownSmartRoutingAccounts(ctx, 11, []int64{21}, "model", "/v1/responses", time.Minute))
	server.FastForward(50 * time.Second)
	// 第二个实例先前没有本地记录，晚到失败不能用完整60秒覆盖剩余10秒的降级期限。
	require.NoError(t, second.CooldownSmartRoutingAccounts(ctx, 11, []int64{21}, "model", "/v1/responses", time.Minute))
	second.rdb = nil
	remaining, err := second.GetSmartRoutingAccountCooldowns(ctx, 11, []int64{21, 22}, "model", "/v1/responses")
	require.Error(t, err)
	require.Positive(t, remaining[21])
	require.LessOrEqual(t, remaining[21], 10*time.Second)
	require.Zero(t, remaining[22], "未知账号不得伪装成全部冷却")
}

func TestSmartRoutingPrecisionDoesNotAddReadLatencyToDeadline(t *testing.T) {
	var local smartRoutingLocalAccountCooldowns
	began := time.Now()
	ended := began.Add(400 * time.Millisecond)
	until := local.observeShared("scope", 10*time.Second, 100000, 99000, began, ended)
	require.Equal(t, began.Add(10*time.Second), until)
	require.Equal(t, 9600*time.Millisecond, local.get("scope", ended))
	local.failed("scope", 10*time.Second, began.Add(11*time.Second), began.Add(-time.Second), 98000)
	require.Zero(t, local.get("scope", began.Add(11*time.Second)), "已知旧请求不能重开本地窗口")
	local.failed("scope", 10*time.Second, began.Add(11*time.Second), began.Add(11*time.Second), 101000)
	require.Equal(t, 10*time.Second, local.get("scope", began.Add(11*time.Second)))
	local.failed("scope", 10*time.Second, began.Add(22*time.Second), began.Add(-time.Second), 98000)
	require.Zero(t, local.get("scope", began.Add(22*time.Second)), "Redis 故障期间创建的本地窗口也拒绝旧请求")
}

func TestSmartRoutingPrecisionOlderReadCannotResurrectClearedWindow(t *testing.T) {
	var local smartRoutingLocalAccountCooldowns
	base := time.Now()
	local.observeShared("scope", time.Minute, 1000, 1000, base, base)
	local.observeShared("scope", 0, 1001, 0, base.Add(time.Millisecond), base.Add(2*time.Millisecond))
	late := local.observeShared("scope", time.Minute, 1000, 1000, base, base.Add(500*time.Millisecond))
	require.True(t, late.IsZero())
	require.Zero(t, local.get("scope", base.Add(time.Second)))
}
