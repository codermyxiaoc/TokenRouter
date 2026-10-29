//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// creativeCanvasDeliverySink 模拟画布归档：可配置归档错误，并按已归档集合回答交付状态。
type creativeCanvasDeliverySink struct {
	mu         sync.Mutex
	persistErr error
	archived   map[int]bool
	persists   int
}

func (s *creativeCanvasDeliverySink) PersistCreativeOutput(_ context.Context, _ string, index int, _ []byte, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persists++
	if s.persistErr != nil {
		return s.persistErr
	}
	if s.archived == nil {
		s.archived = make(map[int]bool)
	}
	s.archived[index] = true
	return nil
}

func (s *creativeCanvasDeliverySink) CreativeOutputDelivered(_ context.Context, _ string, index int) (bool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return true, s.archived[index], nil
}

func creativeDeliveryResult() *CreativeExecuteResult {
	return &CreativeExecuteResult{AccountID: 55, Outputs: []CreativeOutput{{Index: 0, Bytes: []byte("img"), Mime: "image/png"}}}
}

// 画布归档永久失败（如配额已满）时用户拿不到成品：即使 Redis 有副本也不扣费，整笔释放。
func TestCreativeCanvasArchiveRejectedReleasesWithoutCharge(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_canvas_quota"
	seedCreativeRun(f, runID, true)
	sink := &creativeCanvasDeliverySink{persistErr: ErrCanvasQuota}
	f.service.CloudOutputSink = sink
	f.exec.result = creativeDeliveryResult()
	result, err := f.worker.process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, CreativeRunStatusResultLost, f.repo.runs[runID].Status)
	require.Equal(t, creativeOutputUndeliveredCode, *f.repo.outputs[runID][0].ErrorCode)
	require.Zero(t, f.billing.captureN)
	require.Equal(t, []string{CreativeReleaseRequestID(runID)}, uniqueCreativeIDs(f.billing.releaseIDs))
	require.Equal(t, 1, f.exec.calls)
}

// 画布归档暂时失败时保留结算重试，不扣费也不释放；存储恢复后从 Redis 副本补归档并按交付扣费。
func TestCreativeCanvasArchiveTransientFailureRetriesThenCharges(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_canvas_transient"
	seedCreativeRun(f, runID, true)
	sink := &creativeCanvasDeliverySink{persistErr: errors.New("object storage unavailable")}
	f.service.CloudOutputSink = sink
	f.exec.result = creativeDeliveryResult()
	pending, err := f.worker.process(context.Background(), runID)
	require.NoError(t, err)
	require.False(t, pending.Terminal)
	require.Equal(t, CreativeRunStatusSettlementPending, f.repo.runs[runID].Status)
	require.Zero(t, f.billing.captureN)
	require.Zero(t, f.billing.releaseN)

	sink.mu.Lock()
	sink.persistErr = nil
	sink.mu.Unlock()
	result, err := f.worker.process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, CreativeRunStatusSucceeded, f.repo.runs[runID].Status)
	require.Equal(t, []string{CreativeCaptureRequestID(runID)}, f.billing.captureIDs)
	require.Zero(t, f.billing.releaseN)
	require.Equal(t, 1, f.exec.calls)
}

// 多张输出只为已交付的张数扣费，未交付的输出标记为失败且不计费。
func TestCreativeSettlementChargesOnlyDeliveredOutputs(t *testing.T) {
	svc := newCreativeTestService()
	repo := svc.Repo.(*creativeFakeRunRepo)
	run := creativeUsageTestRun("crun_partial_delivery")
	run.ActualCost, run.CompletedAt = nil, nil
	run.RequestedOutputCount = 4
	run.BaseUnitPrice = 0.1
	repo.runs[run.RunID] = run
	outputs := make([]*CreativeRunOutput, 0, 4)
	for i := range 4 {
		outputs = append(outputs, &CreativeRunOutput{RunID: run.RunID, OutputIndex: i, Status: CreativeRunOutputStatusSucceeded})
	}
	repo.outputs[run.RunID] = outputs
	store := svc.TransientStore.(*creativeFakeTransient)
	store.outputs[fmtInputKey(run.RunID, 0)] = []byte("a")
	store.outputs[fmtInputKey(run.RunID, 2)] = []byte("c")
	billing := svc.BillingRepo.(*creativeFakeBillingRepo)
	svc.UsageLogRepo = &creativeUsageLogRepo{}
	require.NoError(t, svc.SettleRun(context.Background(), run.RunID))
	require.Equal(t, CreativeRunStatusSucceeded, run.Status)
	require.Equal(t, 1, billing.captureN)
	require.InDelta(t, 0.2, *run.ActualCost, 1e-10)
	require.Equal(t, CreativeRunOutputStatusSucceeded, repo.outputs[run.RunID][0].Status)
	require.Equal(t, CreativeRunOutputStatusFailed, repo.outputs[run.RunID][1].Status)
	require.Equal(t, CreativeRunOutputStatusSucceeded, repo.outputs[run.RunID][2].Status)
	require.Equal(t, CreativeRunOutputStatusFailed, repo.outputs[run.RunID][3].Status)
}

// Redis 暂时不可用不能被当作成品丢失：保持结算等待，不扣费也不释放。
func TestCreativeSettlementTransientStoreOutageKeepsPending(t *testing.T) {
	svc := newCreativeTestService()
	repo := svc.Repo.(*creativeFakeRunRepo)
	run := creativeUsageTestRun("crun_redis_outage")
	run.ActualCost, run.CompletedAt = nil, nil
	repo.runs[run.RunID] = run
	repo.outputs[run.RunID] = []*CreativeRunOutput{{RunID: run.RunID, Status: CreativeRunOutputStatusSucceeded}}
	svc.TransientStore = &creativeOutageTransient{creativeFakeTransient: svc.TransientStore.(*creativeFakeTransient)}
	billing := svc.BillingRepo.(*creativeFakeBillingRepo)
	require.ErrorIs(t, svc.SettleRun(context.Background(), run.RunID), ErrCreativeTransientUnavailable)
	require.Equal(t, CreativeRunStatusSettlementPending, run.Status)
	require.Zero(t, billing.captureN)
	require.Zero(t, billing.releaseN)
}

// 迟到的失败路径不能整笔释放已记录上游成功结果的任务，必须交给结算按交付张数处理。
func TestCreativeFailRunAfterProviderResultDefersToSettlement(t *testing.T) {
	svc := newCreativeTestService()
	repo := svc.Repo.(*creativeFakeRunRepo)
	run := creativeUsageTestRun("crun_fail_after_result")
	run.ActualCost, run.CompletedAt = nil, nil
	run.Status = CreativeRunStatusProviderSucceeded
	repo.runs[run.RunID] = run
	repo.outputs[run.RunID] = []*CreativeRunOutput{{RunID: run.RunID, Status: CreativeRunOutputStatusSucceeded}}
	svc.TransientStore.(*creativeFakeTransient).outputs[fmtInputKey(run.RunID, 0)] = []byte("image")
	svc.UsageLogRepo = &creativeUsageLogRepo{}
	billing := svc.BillingRepo.(*creativeFakeBillingRepo)
	require.NoError(t, svc.FailRun(context.Background(), run.RunID, "PROVIDER_FAILED", "late failure"))
	require.Equal(t, CreativeRunStatusSucceeded, run.Status)
	require.Equal(t, 1, billing.captureN)
	require.Zero(t, billing.releaseN)
}

// 租约在成品到手后丢失：先把结果落库结算，不能丢弃用户的成品，也不 ACK 或重排。
func TestCreativeLeaseLostAfterSuccessPersistsResult(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_lease_lost_success"
	seedCreativeRun(f, runID, true)
	state := &creativeLeaseState{}
	f.exec.result = creativeDeliveryResult()
	f.exec.onExecute = func(string) { state.lost.Store(true) }
	ctx := context.WithValue(context.Background(), creativeLeaseStateKey{}, state)
	_, err := f.worker.process(ctx, runID)
	require.ErrorIs(t, err, ErrCreativeLeaseLost)
	require.Equal(t, CreativeRunStatusSucceeded, f.repo.runs[runID].Status)
	require.Equal(t, 1, f.billing.captureN)
	require.Zero(t, f.billing.releaseN)
	require.Equal(t, 1, f.exec.calls)
}

// creativeFlakyHeartbeatQueue 让前若干次心跳失败，用于验证短暂抖动不会判定失去租约。
type creativeFlakyHeartbeatQueue struct {
	creativeFakeQueue
	mu       sync.Mutex
	failures int
}

func (q *creativeFlakyHeartbeatQueue) Heartbeat(context.Context, string, string) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.failures != 0 {
		if q.failures > 0 {
			q.failures--
		}
		return false, errors.New("redis timeout")
	}
	return true, nil
}

// 单次心跳失败时锁仍在有效期内，继续执行；持续失败超过锁有效期的七成才判定失去租约。
func TestCreativeHeartbeatToleratesTransientFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failures int
		lost     bool
	}{{"transient", 1, false}, {"persistent", -1, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCreativeWorkerFixture()
			queue := &creativeFlakyHeartbeatQueue{failures: tc.failures}
			opts := f.worker.opts
			opts.JobLockTTL, opts.StaleActiveAfter = 3*time.Second, 3*time.Second
			worker := NewCreativeRunWorker(queue, f.repo, f.store, f.exec, f.service, opts)
			stop, done, lost := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go worker.runJobHeartbeat(ctx, "crun_heartbeat", "lease", &creativeFakeJobLock{}, stop, done, lost, cancel, &creativeLeaseState{})
			select {
			case <-lost:
				require.True(t, tc.lost, "短暂心跳失败不应判定失去租约")
			case <-time.After(3500 * time.Millisecond):
				require.False(t, tc.lost, "持续心跳失败应判定失去租约")
			}
			close(stop)
			<-done
		})
	}
}

// creativeOrphanRunRepo 为兜底扫描提供固定的待恢复任务列表。
type creativeOrphanRunRepo struct {
	*creativeFakeRunRepo
	ids    []string
	before time.Time
}

func (r *creativeOrphanRunRepo) ListCreativeRunIDsForQueueRecovery(_ context.Context, staleBefore time.Time, _ int) ([]string, error) {
	r.before = staleBefore
	return r.ids, nil
}

// creativeDedupQueue 模拟 Redis 去重键：已在队列中的任务返回 ErrCreativeAlreadyQueued。
type creativeDedupQueue struct {
	creativeFakeQueue
	queued map[string]bool
}

func (q *creativeDedupQueue) Enqueue(ctx context.Context, runID string) error {
	if q.queued[runID] {
		return ErrCreativeAlreadyQueued
	}
	q.queued[runID] = true
	return q.creativeFakeQueue.Enqueue(ctx, runID)
}

// Redis 队列丢失的非终态任务由数据库兜底重新入队；已在队列中的任务跳过。
func TestCreativeOrphanRecoveryRequeuesOnlyMissingRuns(t *testing.T) {
	f := newCreativeWorkerFixture()
	repo := &creativeOrphanRunRepo{creativeFakeRunRepo: f.repo, ids: []string{"crun_orphan_a", "crun_orphan_b"}}
	queue := &creativeDedupQueue{queued: map[string]bool{"crun_orphan_b": true}}
	opts := f.worker.opts
	opts.OrphanRecoveryAfter = 20 * time.Minute
	worker := NewCreativeRunWorker(queue, repo, f.store, f.exec, f.service, opts)
	recovered, err := worker.RecoverOrphanedRunsOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, recovered)
	require.Equal(t, []string{"crun_orphan_a"}, queue.enqueued)
	require.WithinDuration(t, time.Now().Add(-20*time.Minute), repo.before, 5*time.Second)
}

// 停机时等待在途任务完成，而不是取消上游请求把任务打成“结果未知”。
func TestCreativeWorkerRuntimeStopDrainsInFlightRun(t *testing.T) {
	fixture := newCreativeWorkerFixture()
	seedCreativeRun(fixture, "crun_drain_1", true)
	repo := &parallelCreativeRunRepo{creativeFakeRunRepo: fixture.repo}
	transient := &parallelCreativeTransient{creativeFakeTransient: fixture.store}
	billing := &parallelCreativeBilling{creativeFakeBillingRepo: fixture.billing}
	fixture.service.Repo = repo
	fixture.service.TransientStore = transient
	fixture.service.BillingRepo = billing
	queue := &parallelCreativeQueue{ready: make(chan string, 1)}
	executor := &overlappingCreativeExecutor{overlapped: make(chan struct{}), release: make(chan struct{})}
	worker := NewCreativeRunWorker(queue, repo, transient, executor, fixture.service, fixture.worker.opts)
	runtime := NewCreativeWorkerRuntime(worker, &config.Config{Creative: config.CreativeConfig{QueueEnabled: true, ShutdownDrainSeconds: 5}})
	runtime.SetWorkerCount(1)
	runtime.Start()
	t.Cleanup(executor.allow)

	queue.ready <- "crun_drain_1"
	require.Eventually(t, func() bool { return executor.active.Load() == 1 }, 2*time.Second, 10*time.Millisecond)
	stopped := make(chan struct{})
	go func() {
		runtime.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("停机未等待在途任务完成")
	case <-time.After(200 * time.Millisecond):
	}
	executor.allow()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("在途任务完成后停机未结束")
	}
	run, err := repo.GetCreativeRunByRunID(context.Background(), "crun_drain_1")
	require.NoError(t, err)
	require.Equal(t, CreativeRunStatusSucceeded, run.Status)
}

func uniqueCreativeIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// creativeOutageTransient 模拟 Redis 整体不可用，读取输出返回基础设施错误。
type creativeOutageTransient struct {
	*creativeFakeTransient
}

func (s *creativeOutageTransient) LoadOutput(context.Context, string, int) ([]byte, error) {
	return nil, ErrCreativeTransientUnavailable
}
