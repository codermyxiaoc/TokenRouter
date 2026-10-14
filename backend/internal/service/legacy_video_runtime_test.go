package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 运行器测试只使用内存替身，不能连接生产或发起付费生成。
type legacyRuntimeRepoStub struct {
	claim    func(context.Context, time.Time, int, time.Duration) ([]*LegacyVideoTask, error)
	finish   func(context.Context, *LegacyVideoTask) error
	backfill func(context.Context, int) (int, error)
}

func (*legacyRuntimeRepoStub) Save(context.Context, *LegacyVideoTask) error { return nil }
func (*legacyRuntimeRepoStub) Get(context.Context, string, string, int64, int64) (*LegacyVideoTask, error) {
	return nil, ErrLegacyVideoTaskNotFound
}
func (r *legacyRuntimeRepoStub) ClaimDue(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]*LegacyVideoTask, error) {
	if r.claim != nil {
		return r.claim(ctx, now, limit, lease)
	}
	return nil, nil
}
func (*legacyRuntimeRepoStub) Checkpoint(context.Context, *LegacyVideoTask) error { return nil }
func (r *legacyRuntimeRepoStub) Finish(ctx context.Context, task *LegacyVideoTask) error {
	if r.finish != nil {
		return r.finish(ctx, task)
	}
	return nil
}
func (r *legacyRuntimeRepoStub) Backfill(ctx context.Context, limit int) (int, error) {
	if r.backfill != nil {
		return r.backfill(ctx, limit)
	}
	return 0, nil
}
func (*legacyRuntimeRepoStub) HasBilling(context.Context, string, int64) (bool, error) {
	return false, nil
}
func (*legacyRuntimeRepoStub) HasUsage(context.Context, string, int64) (bool, error) {
	return false, nil
}

type legacyRuntimeExecutorFunc func(context.Context, *LegacyVideoTask) error

func (fn legacyRuntimeExecutorFunc) RecoverLegacyVideoTask(ctx context.Context, task *LegacyVideoTask) error {
	return fn(ctx, task)
}

func TestLegacyVideoRuntimeConcurrencyAndLifecycle(t *testing.T) {
	var claimed, active, maximum, finished, backfills atomic.Int32
	gate := make(chan struct{})
	started := make(chan struct{}, 8)
	repo := &legacyRuntimeRepoStub{
		claim: func(_ context.Context, _ time.Time, limit int, lease time.Duration) ([]*LegacyVideoTask, error) {
			require.LessOrEqual(t, limit, 4)
			require.Equal(t, 120*time.Second, lease)
			var tasks []*LegacyVideoTask
			for len(tasks) < limit && claimed.Load() < 8 {
				id := claimed.Add(1)
				tasks = append(tasks, &LegacyVideoTask{ID: int64(id), Status: "processing", BillingStatus: "pending", Attempts: 1})
			}
			return tasks, nil
		},
		finish: func(ctx context.Context, task *LegacyVideoTask) error {
			require.NoError(t, ctx.Err())
			require.Equal(t, "settled", task.BillingStatus)
			finished.Add(1)
			return nil
		},
		backfill: func(_ context.Context, limit int) (int, error) {
			require.Equal(t, 100, limit)
			backfills.Add(1)
			return 0, nil
		},
	}
	executor := legacyRuntimeExecutorFunc(func(ctx context.Context, task *LegacyVideoTask) error {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), 90*time.Second)
		n := active.Add(1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		defer active.Add(-1)
		started <- struct{}{}
		<-gate
		task.BillingStatus = "settled"
		return nil
	})
	runtime := NewLegacyVideoRuntime(repo, executor)
	runtime.interval = 5 * time.Millisecond
	runtime.Start()
	runtime.Start()
	for range 4 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("首批任务未启动")
		}
	}
	require.Eventually(t, func() bool { return backfills.Load() >= 2 }, time.Second, time.Millisecond)
	require.EqualValues(t, 4, claimed.Load(), "在途任务占满时不能继续领取")
	close(gate)
	require.Eventually(t, func() bool { return finished.Load() == 8 }, time.Second, time.Millisecond)
	runtime.Stop()
	runtime.Stop()
	require.EqualValues(t, 4, maximum.Load())
	require.Zero(t, active.Load())
	before := backfills.Load()
	runtime.Start()
	require.Equal(t, before, backfills.Load(), "停止后不得重新启动恢复器")
}

func TestLegacyVideoRuntimeStopCancelsAndPersists(t *testing.T) {
	started := make(chan struct{})
	var claimed atomic.Bool
	var finished atomic.Bool
	repo := &legacyRuntimeRepoStub{
		claim: func(context.Context, time.Time, int, time.Duration) ([]*LegacyVideoTask, error) {
			if claimed.Swap(true) {
				return nil, nil
			}
			return []*LegacyVideoTask{{ID: 1, Status: "processing", BillingStatus: "pending", Attempts: 1}}, nil
		},
		finish: func(ctx context.Context, task *LegacyVideoTask) error {
			require.NoError(t, ctx.Err(), "停机收尾使用独立有界上下文")
			require.Contains(t, task.LastError, "自动重试")
			finished.Store(true)
			return nil
		},
	}
	runtime := NewLegacyVideoRuntime(repo, legacyRuntimeExecutorFunc(func(ctx context.Context, _ *LegacyVideoTask) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}))
	runtime.Start()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("任务未启动")
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() { defer wg.Done(); runtime.Stop() }()
	}
	wg.Wait()
	require.True(t, finished.Load())
	// 先关闭再启动也不能提交新的数据库领取。
	stopped := NewLegacyVideoRuntime(repo, legacyRuntimeExecutorFunc(func(context.Context, *LegacyVideoTask) error { t.Fatal("停止后执行"); return nil }))
	stopped.Stop()
	stopped.Start()
}

func TestLegacyVideoRuntimeFailureSchedulingAndCheckpointSafety(t *testing.T) {
	for _, tc := range []struct {
		name      string
		attempts  int
		reconcile bool
		explicit  bool
		panics    bool
		delay     time.Duration
	}{
		{"首次失败", 1, false, false, false, 5 * time.Second},
		{"退避封顶", 30, false, false, false, 15 * time.Minute},
		{"待核对", 1, true, false, false, time.Hour},
		{"保留执行器时间", 1, false, true, false, 2 * time.Hour},
		{"异常隔离", 1, false, false, true, 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stored *LegacyVideoTask
			repo := &legacyRuntimeRepoStub{finish: func(_ context.Context, task *LegacyVideoTask) error { stored = task; return nil }}
			start := time.Now()
			task := &LegacyVideoTask{ID: 1, Status: "completed", BillingStatus: "pending", NextPollAt: start.Add(-time.Minute), Attempts: tc.attempts}
			runtime := NewLegacyVideoRuntime(repo, legacyRuntimeExecutorFunc(func(_ context.Context, task *LegacyVideoTask) error {
				if tc.reconcile {
					task.BillingStatus = "reconciliation"
				}
				if tc.explicit {
					task.NextPollAt = start.Add(2 * time.Hour)
				}
				if tc.panics {
					panic("secret-bearing-upstream-url")
				}
				return errors.New("secret-bearing-upstream-url")
			}))
			runtime.runTask(task)
			require.NotNil(t, stored)
			require.WithinDuration(t, start.Add(tc.delay), stored.NextPollAt, time.Second)
			require.NotContains(t, stored.LastError, "secret")
			require.Equal(t, "completed", stored.Status, "恢复失败不能抹掉已观测的完成状态")
			runtime.Stop()
		})
	}
}

func TestLegacyVideoRuntimeTaskDeadline(t *testing.T) {
	var saved bool
	repo := &legacyRuntimeRepoStub{finish: func(_ context.Context, task *LegacyVideoTask) error { saved = true; return nil }}
	runtime := NewLegacyVideoRuntime(repo, legacyRuntimeExecutorFunc(func(ctx context.Context, _ *LegacyVideoTask) error {
		<-ctx.Done()
		return ctx.Err()
	}))
	runtime.jobLimit = 10 * time.Millisecond
	runtime.runTask(&LegacyVideoTask{ID: 1, Status: "processing", BillingStatus: "pending", Attempts: 1})
	require.True(t, saved)
	runtime.Stop()
}
