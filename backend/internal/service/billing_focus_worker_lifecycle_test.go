package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 默认策略下即使两个槽位都被占据，120 个溢出任务也必须逐个同步执行。
func TestBillingFocusWorkerDefaultOverflowExecutesAllTasks(t *testing.T) {
	pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{
		WorkerCount: 1, QueueSize: 1, TaskTimeout: 5 * time.Second,
	})
	release, started := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); pool.Stop() })
	var completed atomic.Int64
	require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(context.Context) {
		close(started)
		<-release
		completed.Add(1)
	}))
	<-started
	require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(context.Context) { completed.Add(1) }))
	const concurrency = 120
	start := make(chan struct{})
	modes := make([]UsageRecordSubmitMode, concurrency)
	var workers sync.WaitGroup
	workers.Add(concurrency)
	for i := range modes {
		go func(i int) {
			defer workers.Done()
			<-start
			modes[i] = pool.Submit(func(context.Context) { completed.Add(1) })
		}(i)
	}
	close(start)
	workers.Wait()
	for _, mode := range modes {
		require.Equal(t, UsageRecordSubmitModeSync, mode)
	}
	require.Equal(t, int64(concurrency), completed.Load())
	require.Equal(t, uint64(concurrency), pool.Stats().SyncFallbackTasks)
	require.Zero(t, pool.Stats().DroppedQueueFull)
	unblock()
	pool.Stop()
	require.Equal(t, int64(concurrency+2), completed.Load())
}

// 正常停止必须等待已入队任务完成，不能只停止接收新任务就返回。
func TestBillingFocusWorkerStopDrainsQueuedTasks(t *testing.T) {
	pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{
		WorkerCount: 1, QueueSize: 4, TaskTimeout: 5 * time.Second,
	})
	release, started, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); pool.Stop() })
	var completed atomic.Int64
	require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(context.Context) {
		close(started)
		<-release
		completed.Add(1)
	}))
	<-started
	for i := 0; i < 3; i++ {
		require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(context.Context) { completed.Add(1) }))
	}
	go func() { pool.Stop(); close(stopped) }()
	require.Eventually(t, pool.pool.Stopped, time.Second, time.Millisecond)
	select {
	case <-stopped:
		t.Fatal("队列尚未执行完时 Stop 提前返回")
	default:
	}
	unblock()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("正常停止未完成队列排空")
	}
	require.Equal(t, int64(4), completed.Load())
}

// 明确现有故障边界：panic 不击穿工作池，但该任务不会自动重试。
func TestBillingFocusWorkerPanicDoesNotRetryButAllowsNextTask(t *testing.T) {
	pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{
		WorkerCount: 1, QueueSize: 4, TaskTimeout: time.Second,
	})
	t.Cleanup(pool.Stop)
	var attempts, completed atomic.Int64
	require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(context.Context) {
		attempts.Add(1)
		panic("billing-focus-synthetic-panic-before-commit")
	}))
	require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(context.Context) { completed.Add(1) }))
	pool.Stop()
	require.Equal(t, int64(1), attempts.Load(), "当前内存任务没有自动补偿重试机制")
	require.Equal(t, int64(1), completed.Load(), "单个任务 panic 不得阻断之后的任务")
}
