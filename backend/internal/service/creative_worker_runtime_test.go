//go:build unit

package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// TestCreativeWorkerRuntimeWorkerCount 验证 worker 数量默认值与正数热更新边界。
func TestCreativeWorkerRuntimeWorkerCount(t *testing.T) {
	runtime := NewCreativeWorkerRuntime(nil, &config.Config{}, nil)
	require.Equal(t, DefaultCreativeWorkerCount, runtime.WorkerCount())

	runtime.SetWorkerCount(3)
	require.Equal(t, 3, runtime.WorkerCount())
	runtime.SetWorkerCount(0)
	require.Equal(t, 3, runtime.WorkerCount())
}

// TestCreativeWorkerRuntimeStartStopAndScale 验证任务 worker 池可启动、缩容并完整停止。
func TestCreativeWorkerRuntimeStartStopAndScale(t *testing.T) {
	fixture := newCreativeWorkerFixture()
	runtime := NewCreativeWorkerRuntime(fixture.worker, &config.Config{
		Creative: config.CreativeConfig{QueueEnabled: true},
	}, nil)
	runtime.SetWorkerCount(2)
	runtime.Start()
	require.Eventually(t, runtime.Running, time.Second, 10*time.Millisecond)
	require.Equal(t, 2, runtime.WorkerCount())

	runtime.SetWorkerCount(1)
	require.Equal(t, 1, runtime.WorkerCount())
	runtime.Stop()
	require.False(t, runtime.Running())
}

// TestCreativeWorkerRuntimeStatus 验证状态快照如实反映运行状态、池规模与忙碌 worker 数量。
func TestCreativeWorkerRuntimeStatus(t *testing.T) {
	stopped := NewCreativeWorkerRuntime(nil, &config.Config{
		Creative: config.CreativeConfig{QueueEnabled: true},
	}, nil)
	require.Equal(t, CreativeWorkerStatus{}, stopped.Status())

	fixture := newCreativeWorkerFixture()
	seedCreativeRun(fixture, "crun_status_1", true)
	seedCreativeRun(fixture, "crun_status_2", true)
	fixture.service.UserRepo.(*creativeFakeUserRepo).user.Concurrency = 2

	repo := &parallelCreativeRunRepo{creativeFakeRunRepo: fixture.repo}
	transient := &parallelCreativeTransient{creativeFakeTransient: fixture.store}
	billing := &parallelCreativeBilling{creativeFakeBillingRepo: fixture.billing}
	fixture.service.Repo = repo
	fixture.service.TransientStore = transient
	fixture.service.BillingRepo = billing
	queue := &parallelCreativeQueue{ready: make(chan string, 2)}
	executor := &overlappingCreativeExecutor{
		overlapped: make(chan struct{}),
		release:    make(chan struct{}),
	}
	worker := NewCreativeRunWorker(queue, repo, transient, executor, fixture.service, fixture.worker.opts, NewConcurrencyService(&parallelCreativeUserCache{}))
	runtime := NewCreativeWorkerRuntime(worker, &config.Config{
		Creative: config.CreativeConfig{QueueEnabled: true},
	})
	runtime.SetWorkerCount(2)
	runtime.Start()
	t.Cleanup(func() {
		executor.allow()
		runtime.Stop()
	})

	queue.ready <- "crun_status_1"
	queue.ready <- "crun_status_2"
	select {
	case <-executor.overlapped:
	case <-time.After(2 * time.Second):
		t.Fatal("两个创作台任务未同时进入 provider")
	}
	require.Eventually(t, func() bool {
		status := runtime.Status()
		return status.Running && status.WorkerCount == 2 && status.BusyWorkers == 2
	}, 2*time.Second, 10*time.Millisecond)

	executor.allow()
	runtime.Stop()
	require.Equal(t, CreativeWorkerStatus{}, runtime.Status())
}

// parallelCreativeQueue 是只用于并行验收的内存队列，保留真实 worker 的 Reserve/锁/确认路径。
type parallelCreativeQueue struct {
	ready chan string
}

func (q *parallelCreativeQueue) Enqueue(_ context.Context, runID string) error {
	q.ready <- runID
	return nil
}

func (q *parallelCreativeQueue) Reserve(ctx context.Context, blockTimeout time.Duration) (ReservedCreativeRun, error) {
	timer := time.NewTimer(blockTimeout)
	defer timer.Stop()
	select {
	case runID := <-q.ready:
		return ReservedCreativeRun{RunID: runID, LeaseToken: "test-lease"}, nil
	case <-ctx.Done():
		return ReservedCreativeRun{}, ctx.Err()
	case <-timer.C:
		return ReservedCreativeRun{}, ErrCreativeQueueEmpty
	}
}

func (q *parallelCreativeQueue) RequeueAfter(context.Context, string, string, time.Duration) error {
	return nil
}
func (q *parallelCreativeQueue) Ack(context.Context, string, string) error { return nil }
func (q *parallelCreativeQueue) Heartbeat(context.Context, string, string) (bool, error) {
	return true, nil
}
func (q *parallelCreativeQueue) MoveDueDelayedToReady(context.Context, int) (int, error) {
	return 0, nil
}
func (q *parallelCreativeQueue) RecoverStaleActive(context.Context, time.Duration, int) (int, error) {
	return 0, nil
}
func (q *parallelCreativeQueue) TryAcquireJobLock(context.Context, string, time.Duration) (CreativeRunJobLock, bool, error) {
	return &creativeFakeJobLock{}, true, nil
}

// parallelCreativeRunRepo 为并行测试保护 fake 仓储的 map 访问。
type parallelCreativeRunRepo struct {
	*creativeFakeRunRepo
	mu sync.Mutex
}

func (r *parallelCreativeRunRepo) GetCreativeRunByRunID(ctx context.Context, runID string) (*CreativeRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.creativeFakeRunRepo.GetCreativeRunByRunID(ctx, runID)
}

func (r *parallelCreativeRunRepo) MarkCreativeRunRunning(ctx context.Context, runID string, accountID int64, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.creativeFakeRunRepo.MarkCreativeRunRunning(ctx, runID, accountID, now)
}

func (r *parallelCreativeRunRepo) SetCreativeRunAccountID(ctx context.Context, runID string, accountID int64, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.creativeFakeRunRepo.SetCreativeRunAccountID(ctx, runID, accountID, now)
}

func (r *parallelCreativeRunRepo) MarkCreativeRunSucceeded(ctx context.Context, runID string, actualCost float64, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.creativeFakeRunRepo.MarkCreativeRunSucceeded(ctx, runID, actualCost, now)
}

func (r *parallelCreativeRunRepo) UpdateCreativeRunOutput(ctx context.Context, runID string, outputIndex int, status, mimeType string, byteSize int64, transientExpiresAt *time.Time, errorCode, errorMessage string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.creativeFakeRunRepo.UpdateCreativeRunOutput(ctx, runID, outputIndex, status, mimeType, byteSize, transientExpiresAt, errorCode, errorMessage)
}

func (r *parallelCreativeRunRepo) ListCreativeRunOutputs(ctx context.Context, runID string) ([]*CreativeRunOutput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.creativeFakeRunRepo.ListCreativeRunOutputs(ctx, runID)
}

// parallelCreativeTransient 保护并行结算写入的输出 map。
type parallelCreativeTransient struct {
	*creativeFakeTransient
	mu sync.Mutex
}

func (s *parallelCreativeTransient) SaveOutput(ctx context.Context, runID string, index int, data []byte, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creativeFakeTransient.SaveOutput(ctx, runID, index, data, ttl)
}

// parallelCreativeBilling 保护并行结算更新的 fake 计数器。
type parallelCreativeBilling struct {
	*creativeFakeBillingRepo
	mu sync.Mutex
}

func (r *parallelCreativeBilling) CaptureBatchImageBalance(ctx context.Context, cmd *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.creativeFakeBillingRepo.CaptureBatchImageBalance(ctx, cmd)
}

// parallelCreativeUserCache 以原子计数模拟用户并发槽位。
type parallelCreativeUserCache struct {
	stubConcurrencyCacheForTest
	active atomic.Int64
}

func (c *parallelCreativeUserCache) AcquireUserSlot(_ context.Context, _ int64, maxConcurrency int, _ string) (bool, error) {
	for {
		current := c.active.Load()
		if maxConcurrency > 0 && current >= int64(maxConcurrency) {
			return false, nil
		}
		if c.active.CompareAndSwap(current, current+1) {
			return true, nil
		}
	}
}

func (c *parallelCreativeUserCache) ReleaseUserSlot(context.Context, int64, string) error {
	c.active.Add(-1)
	return nil
}

// overlappingCreativeExecutor 在两个 provider 调用同时进入时通知测试，然后等待统一放行。
type overlappingCreativeExecutor struct {
	active      atomic.Int64
	overlapped  chan struct{}
	overlapOnce sync.Once
	release     chan struct{}
	releaseOnce sync.Once
}

func (e *overlappingCreativeExecutor) Prepare(_ context.Context, run CreativeRun) (*CreativeExecution, error) {
	return &CreativeExecution{
		Account:       &Account{ID: 55, Platform: PlatformGemini},
		UpstreamModel: run.Model,
		ReleaseFunc:   func() {},
	}, nil
}

func (e *overlappingCreativeExecutor) Execute(ctx context.Context, _ CreativeRun, _ CreativeRunPayload, _ *CreativeExecution) (*CreativeExecuteResult, error) {
	if e.active.Add(1) >= 2 {
		e.overlapOnce.Do(func() { close(e.overlapped) })
	}
	select {
	case <-e.release:
	case <-ctx.Done():
		e.active.Add(-1)
		return nil, ctx.Err()
	}
	e.active.Add(-1)
	return &CreativeExecuteResult{
		Outputs:   []CreativeOutput{{Index: 0, Bytes: []byte("img"), Mime: "image/png"}},
		AccountID: 55,
	}, nil
}

func (e *overlappingCreativeExecutor) IsRetryable(err error) bool {
	return IsRetryableCreativeError(err)
}

func (e *overlappingCreativeExecutor) allow() {
	e.releaseOnce.Do(func() { close(e.release) })
}

// TestCreativeWorkerRuntimeParallelProviderExecution 验证两个 worker 可让同一用户的两个任务并行进入 provider。
func TestCreativeWorkerRuntimeParallelProviderExecution(t *testing.T) {
	fixture := newCreativeWorkerFixture()
	seedCreativeRun(fixture, "crun_parallel_1", true)
	seedCreativeRun(fixture, "crun_parallel_2", true)
	fixture.service.UserRepo.(*creativeFakeUserRepo).user.Concurrency = 2

	repo := &parallelCreativeRunRepo{creativeFakeRunRepo: fixture.repo}
	transient := &parallelCreativeTransient{creativeFakeTransient: fixture.store}
	billing := &parallelCreativeBilling{creativeFakeBillingRepo: fixture.billing}
	fixture.service.Repo = repo
	fixture.service.TransientStore = transient
	fixture.service.BillingRepo = billing
	queue := &parallelCreativeQueue{ready: make(chan string, 2)}
	executor := &overlappingCreativeExecutor{
		overlapped: make(chan struct{}),
		release:    make(chan struct{}),
	}
	concurrency := NewConcurrencyService(&parallelCreativeUserCache{})
	worker := NewCreativeRunWorker(queue, repo, transient, executor, fixture.service, fixture.worker.opts, concurrency)
	runtime := NewCreativeWorkerRuntime(worker, &config.Config{Creative: config.CreativeConfig{QueueEnabled: true}})
	runtime.SetWorkerCount(2)
	runtime.Start()
	t.Cleanup(func() {
		executor.allow()
		runtime.Stop()
	})

	queue.ready <- "crun_parallel_1"
	queue.ready <- "crun_parallel_2"
	select {
	case <-executor.overlapped:
	case <-time.After(2 * time.Second):
		t.Fatal("两个创作台任务未同时进入 provider")
	}
	executor.allow()
	runtime.Stop()

	first, err := repo.GetCreativeRunByRunID(context.Background(), "crun_parallel_1")
	require.NoError(t, err)
	second, err := repo.GetCreativeRunByRunID(context.Background(), "crun_parallel_2")
	require.NoError(t, err)
	require.Equal(t, CreativeRunStatusSucceeded, first.Status)
	require.Equal(t, CreativeRunStatusSucceeded, second.Status)
}
