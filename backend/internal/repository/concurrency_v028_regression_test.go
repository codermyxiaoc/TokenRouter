//go:build integration

package repository

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// 使用隔离容器的专用逻辑库和唯一账号，不加测试命名空间钩子，保留生产的 Lua 与 Pipeline 键行为。
func v028RawConcurrencyRedis(t *testing.T, db int, accountIDs ...int64) *redis.Client {
	t.Helper()
	opts := *integrationRedis.Options()
	opts.DB = db
	rdb := redis.NewClient(&opts)
	require.NoError(t, rdb.Ping(context.Background()).Err())
	t.Cleanup(func() {
		ctx := context.Background()
		for _, id := range accountIDs {
			require.NoError(t, rdb.Del(ctx, accountSlotKey(id), liveAccountSlotKey(id), accountWaitKey(id)).Err())
			require.NoError(t, rdb.ZRem(ctx, accountActiveIndexKey, strconv.FormatInt(id, 10)).Err())
		}
		require.NoError(t, rdb.Close())
	})
	return rdb
}

// 起跑屏障确保指定数量的操作同时到达 Redis；工作协程只写各自的结果，主协程统一断言。
func v028ConcurrencyBurst(t *testing.T, count int, action func(int) (bool, error)) []bool {
	t.Helper()
	start := make(chan struct{})
	accepted, errs := make([]bool, count), make([]error, count)
	var ready, done sync.WaitGroup
	ready.Add(count)
	done.Add(count)
	for i := range count {
		go func(i int) {
			defer done.Done()
			ready.Done()
			<-start
			accepted[i], errs[i] = action(i)
		}(i)
	}
	ready.Wait()
	close(start)
	done.Wait()
	for i, err := range errs {
		require.NoError(t, err, "第%d个并发操作", i)
	}
	return accepted
}

// 批量负载必须反映真实账号槽、等待队列与释放结果，不能把已占用账号误判为空闲。
func TestV028ConcurrencyGetAccountsLoadBatchRealRedis(t *testing.T) {
	for _, concurrency := range []int{60, 120} {
		t.Run(fmt.Sprintf("%d", concurrency), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			baseID := time.Now().UnixNano()
			primary, secondary, empty := baseID, baseID+1, baseID+2
			rdb := v028RawConcurrencyRedis(t, 13, primary, secondary, empty)
			otherDB := v028RawConcurrencyRedis(t, 12, primary)
			cache := NewConcurrencyCache(rdb, 15, 900)
			otherCache := NewConcurrencyCache(otherDB, 15, 900)
			limit := concurrency / 2
			accounts := []service.AccountWithConcurrency{{ID: primary, MaxConcurrency: limit}, {ID: secondary, MaxConcurrency: 4}, {ID: empty, MaxConcurrency: 1}}
			assertLoad := func(id int64, current, waiting, rate int) {
				loads, err := cache.GetAccountsLoadBatch(ctx, accounts)
				require.NoError(t, err)
				require.Len(t, loads, 3)
				require.Equal(t, &service.AccountLoadInfo{AccountID: id, CurrentConcurrency: current, WaitingCount: waiting, LoadRate: rate}, loads[id])
				single, err := cache.GetAccountConcurrency(ctx, id)
				require.NoError(t, err)
				require.Equal(t, current, single)
				waitCount, err := cache.GetAccountWaitingCount(ctx, id)
				require.NoError(t, err)
				require.Equal(t, waiting, waitCount)
			}
			// 相同请求ID在另一个账号或Redis库中独立计数。
			for _, requestID := range []string{"request-0", "request-1"} {
				ok, err := cache.AcquireAccountSlot(ctx, secondary, 4, requestID)
				require.NoError(t, err)
				require.True(t, ok)
			}
			ok, err := cache.IncrementAccountWaitCount(ctx, secondary, 4)
			require.NoError(t, err)
			require.True(t, ok)
			ok, err = otherCache.AcquireAccountSlot(ctx, primary, 5, "request-0")
			require.NoError(t, err)
			require.True(t, ok)
			assertLoad(primary, 0, 0, 0)
			assertLoad(secondary, 2, 1, 75)
			assertLoad(empty, 0, 0, 0)

			started := time.Now()
			accepted := v028ConcurrencyBurst(t, concurrency, func(i int) (bool, error) {
				return cache.AcquireAccountSlot(ctx, primary, limit, fmt.Sprintf("request-%d", i))
			})
			var winners []string
			for i, acquired := range accepted {
				if acquired {
					winners = append(winners, fmt.Sprintf("request-%d", i))
				}
			}
			require.Len(t, winners, limit, "并发准入不能超过账号配置上限")
			assertLoad(primary, limit, 0, 100)
			waitAccepted := v028ConcurrencyBurst(t, concurrency, func(int) (bool, error) {
				return cache.IncrementAccountWaitCount(ctx, primary, limit)
			})
			waiting := 0
			for _, acquired := range waitAccepted {
				if acquired {
					waiting++
				}
			}
			require.Equal(t, limit, waiting, "等待队列也必须按账号上限原子拒绝超额请求")
			assertLoad(primary, limit, limit, 200)
			// 已占用请求重试只刷新租约，不额外消耗容量；满载不能接纳新请求。
			for _, acquired := range v028ConcurrencyBurst(t, concurrency, func(i int) (bool, error) {
				return cache.AcquireAccountSlot(ctx, primary, limit, winners[i%len(winners)])
			}) {
				require.True(t, acquired)
			}
			ok, err = cache.AcquireAccountSlot(ctx, primary, limit, "must-reject-full")
			require.NoError(t, err)
			require.False(t, ok)
			assertLoad(primary, limit, limit, 200)
			assertLoad(secondary, 2, 1, 75)
			assertLoad(empty, 0, 0, 0)

			v028ConcurrencyBurst(t, limit/2, func(i int) (bool, error) {
				return true, cache.ReleaseAccountSlot(ctx, primary, winners[i])
			})
			assertLoad(primary, limit/2, limit, 150)
			v028ConcurrencyBurst(t, concurrency, func(i int) (bool, error) {
				// 重复释放与超次数退出队列均不得得到负数或影响其他账号。
				if err := cache.ReleaseAccountSlot(ctx, primary, winners[i%len(winners)]); err != nil {
					return false, err
				}
				return true, cache.DecrementAccountWaitCount(ctx, primary)
			})
			assertLoad(primary, 0, 0, 0)
			assertLoad(secondary, 2, 1, 75)
			assertLoad(empty, 0, 0, 0)
			otherLoads, err := otherCache.GetAccountsLoadBatch(ctx, []service.AccountWithConcurrency{{ID: primary, MaxConcurrency: 5}})
			require.NoError(t, err)
			require.Equal(t, &service.AccountLoadInfo{AccountID: primary, CurrentConcurrency: 1, WaitingCount: 0, LoadRate: 20}, otherLoads[primary])
			for _, emptyInput := range [][]service.AccountWithConcurrency{nil, {}} {
				loads, err := cache.GetAccountsLoadBatch(ctx, emptyInput)
				require.NoError(t, err)
				require.Empty(t, loads)
			}
			zeroLimit, err := cache.GetAccountsLoadBatch(ctx, []service.AccountWithConcurrency{{ID: empty, MaxConcurrency: 0}})
			require.NoError(t, err)
			require.Zero(t, zeroLimit[empty].LoadRate)
			t.Logf("Redis直连并发=%d acquired=%d rejected=%d wait_accepted=%d wait_rejected=%d 负载率100->200->150->0；重复取得/释放及账号/数据库隔离通过；elapsed=%s", concurrency, limit, concurrency-limit, waiting, concurrency-waiting, time.Since(started))
		})
	}
}
