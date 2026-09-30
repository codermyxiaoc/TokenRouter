//go:build integration

package repository

import (
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// 两个独立连接池共用真实键空间；随机实体 ID 隔离数据，不用前缀钩子改写生产 Lua/Pipeline 的键。
func newConcurrencyMultiClientRedis(t *testing.T) ([2]*redis.Client, int64) {
	t.Helper()
	randomID := uuid.New()
	id := int64(binary.BigEndian.Uint64(randomID[:8]) & uint64(1<<63-1))
	require.Positive(t, id)
	var clients [2]*redis.Client
	for i := range clients {
		opts := *integrationRedis.Options()
		clients[i] = redis.NewClient(&opts)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// 仅清理本测试的实体与索引成员，不清空共享 Redis 或其它测试的活跃索引。
		err := clients[0].Del(ctx, accountSlotKey(id), liveAccountSlotKey(id), accountWaitKey(id),
			userSlotKey(id), liveUserSlotKey(id), waitQueueKey(id),
			fmt.Sprintf("%s%d", apiKeyCreateCountKeyPrefix, id), apiKeyRateLimitKey(id)).Err()
		require.NoError(t, err)
		require.NoError(t, clients[0].ZRem(ctx, accountActiveIndexKey, id).Err())
		require.NoError(t, clients[0].ZRem(ctx, userActiveIndexKey, id).Err())
		for _, client := range clients {
			require.NoError(t, client.Close())
		}
	})
	return clients, id
}

// 屏障统一放行工作协程，各协程只写独立结果位置，断言在全部操作完成后执行。
func concurrencyMultiClientBurst(t *testing.T, count int, action func(int) (int64, error)) []int64 {
	t.Helper()
	values, errs := make([]int64, count), make([]error, count)
	start := make(chan struct{})
	var ready, done sync.WaitGroup
	ready.Add(count)
	done.Add(count)
	for i := range count {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			values[i], errs[i] = action(i)
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	for i, err := range errs {
		require.NoError(t, err, "第 %d 个并发 Redis 操作", i)
	}
	return values
}

func TestConcurrencyMultiClientIntegration(t *testing.T) {
	for _, entity := range []string{"account", "user"} {
		for _, concurrent := range []int{60, 120} {
			t.Run(fmt.Sprintf("%s/%d", entity, concurrent), func(t *testing.T) {
				clients, id := newConcurrencyMultiClientRedis(t)
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				caches := [2]service.ConcurrencyCache{
					NewConcurrencyCache(clients[0], 15, 900),
					NewConcurrencyCache(clients[1], 15, 900),
				}
				const limit, waitLimit = 8, 3
				acquire := func(client int, requestID string) (bool, error) {
					if entity == "account" {
						return caches[client].AcquireAccountSlot(ctx, id, limit, requestID)
					}
					return caches[client].AcquireUserSlot(ctx, id, limit, requestID)
				}
				release := func(client int, requestID string) error {
					if entity == "account" {
						return caches[client].ReleaseAccountSlot(ctx, id, requestID)
					}
					return caches[client].ReleaseUserSlot(ctx, id, requestID)
				}
				assertLoad := func(current, waiting int) {
					for _, cache := range caches {
						if entity == "account" {
							actual, err := cache.GetAccountConcurrency(ctx, id)
							require.NoError(t, err)
							require.Equal(t, current, actual)
							loads, err := cache.GetAccountsLoadBatch(ctx, []service.AccountWithConcurrency{{ID: id, MaxConcurrency: limit}})
							require.NoError(t, err)
							require.Equal(t, &service.AccountLoadInfo{AccountID: id, CurrentConcurrency: current, WaitingCount: waiting, LoadRate: (current + waiting) * 100 / limit}, loads[id])
						} else {
							actual, err := cache.GetUserConcurrency(ctx, id)
							require.NoError(t, err)
							require.Equal(t, current, actual)
							loads, err := cache.GetUsersLoadBatch(ctx, []service.UserWithConcurrency{{ID: id, MaxConcurrency: limit}})
							require.NoError(t, err)
							require.Equal(t, &service.UserLoadInfo{UserID: id, CurrentConcurrency: current, WaitingCount: waiting, LoadRate: (current + waiting) * 100 / limit}, loads[id])
						}
					}
				}
				assertLoad(0, 0)
				for round := range 2 {
					// 全部争抢结束前不释放槽，成功数就等于本轮峰值，不能靠快速释放掩盖超卖。
					values := concurrencyMultiClientBurst(t, concurrent, func(i int) (int64, error) {
						ok, err := acquire(i%2, fmt.Sprintf("round-%d-request-%d", round, i))
						if ok {
							return 1, err
						}
						return 0, err
					})
					var winners []string
					for i, accepted := range values {
						if accepted == 1 {
							winners = append(winners, fmt.Sprintf("round-%d-request-%d", round, i))
						}
					}
					require.Len(t, winners, limit, "两个客户端合计只能占用 8 个槽")
					assertLoad(limit, 0)
					waits := concurrencyMultiClientBurst(t, concurrent, func(i int) (int64, error) {
						var ok bool
						var err error
						if entity == "account" {
							ok, err = caches[i%2].IncrementAccountWaitCount(ctx, id, waitLimit)
						} else {
							ok, err = caches[i%2].IncrementWaitCount(ctx, id, waitLimit)
						}
						if ok {
							return 1, err
						}
						return 0, err
					})
					var acceptedWaits int64
					for _, value := range waits {
						acceptedWaits += value
					}
					require.EqualValues(t, waitLimit, acceptedWaits)
					assertLoad(limit, waitLimit)
					// 同一 requestID 跨客户端重入仍占一个槽；重复释放也不能扣减别人的槽。
					concurrencyMultiClientBurst(t, concurrent, func(i int) (int64, error) {
						ok, err := acquire(i%2, winners[i%len(winners)])
						if !ok && err == nil {
							err = fmt.Errorf("重入已有槽失败")
						}
						return 0, err
					})
					assertLoad(limit, waitLimit)
					concurrencyMultiClientBurst(t, concurrent, func(i int) (int64, error) {
						return 0, release(i%2, winners[i%len(winners)])
					})
					assertLoad(0, waitLimit)
					concurrencyMultiClientBurst(t, concurrent, func(i int) (int64, error) {
						if entity == "account" {
							return 0, caches[i%2].DecrementAccountWaitCount(ctx, id)
						}
						return 0, caches[i%2].DecrementWaitCount(ctx, id)
					})
					require.NoError(t, release(0, "不存在的请求"))
					assertLoad(0, 0)
				}
			})
		}
	}
}

func TestAPIKeyCreateCountMultiClientIntegration(t *testing.T) {
	clients, userID := newConcurrencyMultiClientRedis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	caches := [2]service.APIKeyCache{NewAPIKeyCache(clients[0]), NewAPIKeyCache(clients[1])}
	const attempts, limit = 120, 60
	counts := concurrencyMultiClientBurst(t, attempts, func(i int) (int64, error) {
		return caches[i%2].IncrementCreateCount(ctx, userID, time.Hour)
	})
	sort.Slice(counts, func(i, j int) bool { return counts[i] < counts[j] })
	allowed := 0
	for i, count := range counts {
		require.EqualValues(t, i+1, count, "跨客户端计数必须恰好覆盖 1 到 120，不能重复或丢失")
		if count <= limit {
			allowed++
		}
	}
	require.Equal(t, limit, allowed, "创建策略只允许窗口计数不超过 60 的尝试")
	key := fmt.Sprintf("%s%d", apiKeyCreateCountKeyPrefix, userID)
	for _, client := range clients {
		count, err := client.Get(ctx, key).Int()
		require.NoError(t, err)
		require.Equal(t, attempts, count)
	}
	// 主动缩短现有窗口后再次递增，精确比较绝对过期时刻，避免靠 sleep 的时序断言。
	require.NoError(t, clients[0].PExpire(ctx, key, time.Minute).Err())
	expiresBefore, err := clients[0].Do(ctx, "PEXPIRETIME", key).Int64()
	require.NoError(t, err)
	require.Positive(t, expiresBefore)
	concurrencyMultiClientBurst(t, attempts, func(i int) (int64, error) {
		return caches[i%2].IncrementCreateCount(ctx, userID, time.Hour)
	})
	for i, client := range clients {
		// 历史的创建失败次数清理与新创建窗口隔离，不能借删除旧计数重置新限额。
		require.NoError(t, caches[i].DeleteCreateAttemptCount(ctx, userID))
		expiresAfter, err := client.Do(ctx, "PEXPIRETIME", key).Int64()
		require.NoError(t, err)
		require.Equal(t, expiresBefore, expiresAfter, "后续尝试不得延长原创建窗口")
		count, err := client.Get(ctx, key).Int()
		require.NoError(t, err)
		require.Equal(t, attempts*2, count)
	}
}
