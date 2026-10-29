//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// creativeFakeExecutor 是 CreativeRunExecutor 的测试替身。
type creativeFakeExecutor struct {
	result         *CreativeExecuteResult
	err            error
	execPrepareErr error
	// onExecute 可在执行期间修改仓储状态（模拟状态竞争）。
	onExecute func(runID string)
	calls     int
}

func (e *creativeFakeExecutor) Prepare(ctx context.Context, run CreativeRun) (*CreativeExecution, error) {
	if e.execPrepareErr != nil {
		return nil, e.execPrepareErr
	}
	return &CreativeExecution{
		Account:       &Account{ID: 55, Platform: PlatformGemini},
		UpstreamModel: run.Model,
		ReleaseFunc:   func() {},
	}, nil
}

func (e *creativeFakeExecutor) Execute(ctx context.Context, run CreativeRun, payload CreativeRunPayload, execution *CreativeExecution) (*CreativeExecuteResult, error) {
	e.calls++
	if e.onExecute != nil {
		e.onExecute(run.RunID)
	}
	if run.Operation != CreativeOperationTTS && e.err != nil {
		if _, ok := ImageAttemptDecisionForError(e.err); !ok {
			decision := ImageAttemptDecision{Outcome: ImageAttemptUnknown}
			var local *CreativeUpstreamError
			if errors.As(e.err, &local) && !local.Retryable {
				decision.Outcome = ImageAttemptNotSent
			}
			return e.result, WrapImageAttemptError(e.err, decision)
		}
	}
	return e.result, e.err
}

func (e *creativeFakeExecutor) IsRetryable(err error) bool { return IsRetryableCreativeError(err) }

// creativeWorkerFixture 组装 worker 测试夹具。
type creativeWorkerFixture struct {
	worker  *CreativeRunWorker
	service *CreativePublicService
	repo    *creativeFakeRunRepo
	store   *creativeFakeTransient
	billing *creativeFakeBillingRepo
	queue   *creativeFakeQueue
	exec    *creativeFakeExecutor
}

func newCreativeWorkerFixture() *creativeWorkerFixture {
	svc := newCreativeTestService()
	repo := svc.Repo.(*creativeFakeRunRepo)
	store := svc.TransientStore.(*creativeFakeTransient)
	billing := svc.BillingRepo.(*creativeFakeBillingRepo)
	queue := &creativeFakeQueue{}
	exec := &creativeFakeExecutor{}
	opts := CreativeWorkerOptions{
		ReserveBlockTimeout: time.Millisecond,
		JobLockTTL:          time.Minute,
		LockConflictDelay:   time.Millisecond,
		DefaultRequeueDelay: time.Millisecond,
		ErrorRetryDelay:     time.Millisecond,
		ErrorBackoff:        time.Millisecond,
		DelayedPollInterval: time.Millisecond,
		RecoveryInterval:    time.Millisecond,
		StaleActiveAfter:    time.Minute,
		DelayedMoveLimit:    10,
		RecoverLimit:        10,
		MaxAttempts:         3,
	}
	return &creativeWorkerFixture{
		worker:  NewCreativeRunWorker(queue, repo, store, exec, svc, opts),
		service: svc,
		repo:    repo,
		store:   store,
		billing: billing,
		queue:   queue,
		exec:    exec,
	}
}

// seedCreativeRun 种入一个 queued 任务及其暂存载荷。
func seedCreativeRun(f *creativeWorkerFixture, runID string, withPayload bool) {
	hold := 0.02
	f.repo.runs[runID] = &CreativeRun{
		RunID:                runID,
		UserID:               7,
		GroupID:              12,
		APIKeyID:             900,
		Model:                "gemini-3.1-flash-image",
		Operation:            CreativeOperationGenerate,
		RequestedOutputCount: 1,
		Status:               CreativeRunStatusQueued,
		EstimatedCost:        0.02,
		HoldAmount:           &hold,
	}
	f.repo.outputs[runID] = []*CreativeRunOutput{
		{RunID: runID, OutputIndex: 0, Status: CreativeRunOutputStatusPending},
	}
	if withPayload {
		f.store.payloads[runID] = &CreativeRunPayload{RunID: runID, Prompt: "p"}
	}
}

func TestCreativeWorkerSuccessPath(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_workersuccess01"
	seedCreativeRun(f, runID, true)
	f.exec.result = &CreativeExecuteResult{
		Outputs:   []CreativeOutput{{Index: 0, Bytes: []byte("img"), Mime: "image/png"}},
		AccountID: 55,
	}

	result, err := f.worker.process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, result.Terminal)

	run := f.repo.runs[runID]
	require.Equal(t, CreativeRunStatusSucceeded, run.Status)
	require.NotNil(t, run.AccountID)
	require.Equal(t, int64(55), *run.AccountID)
	require.Equal(t, 1, f.repo.setAccountN)
	require.NotNil(t, run.ActualCost)
	// 输出行进入 succeeded，临时输出已保存。
	output := f.repo.outputs[runID][0]
	require.Equal(t, CreativeRunOutputStatusSucceeded, output.Status)
	require.NotNil(t, output.TransientExpiresAt)
	data, err := f.store.LoadOutput(context.Background(), runID, 0)
	require.NoError(t, err)
	require.Equal(t, []byte("img"), data)
	// 计费：预占发生在创建阶段（worker 测试直接种入任务，故为 0），worker 只做捕获。
	require.Equal(t, 0, f.billing.reserveN)
	require.Equal(t, 1, f.billing.captureN)
	require.Equal(t, 0, f.billing.releaseN)
}

// TestCreativeWorkerRecoversPersistedProviderOutput 验证输出元数据已落库但状态写入失败时不重复调用 provider。
func TestCreativeWorkerRecoversPersistedProviderOutput(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_workerrecover01"
	seedCreativeRun(f, runID, false)
	run := f.repo.runs[runID]
	run.Status = CreativeRunStatusRunning
	accountID := int64(55)
	run.AccountID = &accountID
	f.repo.outputs[runID][0].Status = CreativeRunOutputStatusSucceeded
	f.store.outputs[runID+":0"] = []byte("img")

	result, err := f.worker.process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, 0, f.exec.calls)
	require.Equal(t, CreativeRunStatusSucceeded, f.repo.runs[runID].Status)
	require.Equal(t, 1, f.billing.captureN)
}

// TestCreativeWorkerUserConcurrencyPending 验证用户槽位未获取时任务保持 queued 并释放 worker。
func TestCreativeWorkerUserConcurrencyPending(t *testing.T) {
	f := newCreativeWorkerFixture()
	seedCreativeRun(f, "crun_worker_user_pending", true)
	userRepo := f.service.UserRepo.(*creativeFakeUserRepo)
	userRepo.user.Concurrency = 1
	cache := &stubConcurrencyCacheForTest{acquireResult: false}
	f.worker = NewCreativeRunWorker(f.queue, f.repo, f.store, f.exec, f.service, f.worker.opts, NewConcurrencyService(cache))

	result, err := f.worker.process(context.Background(), "crun_worker_user_pending")
	require.NoError(t, err)
	require.False(t, result.Terminal)
	require.Equal(t, defaultCreativeConcurrencyRequeueDelay, result.RequeueAfter)
	require.Equal(t, CreativeRunStatusQueued, f.repo.runs["crun_worker_user_pending"].Status)
	require.Equal(t, 0, f.exec.calls)
}

// TestCreativeWorkerAccountConcurrencyPending 验证执行器报告账号槽位不足时不推进任务状态。
func TestCreativeWorkerAccountConcurrencyPending(t *testing.T) {
	f := newCreativeWorkerFixture()
	seedCreativeRun(f, "crun_worker_account_pending", true)
	f.exec.execPrepareErr = ErrCreativeExecutionPending

	result, err := f.worker.process(context.Background(), "crun_worker_account_pending")
	require.NoError(t, err)
	require.False(t, result.Terminal)
	require.Equal(t, defaultCreativeConcurrencyRequeueDelay, result.RequeueAfter)
	require.Equal(t, CreativeRunStatusQueued, f.repo.runs["crun_worker_account_pending"].Status)
}

// 输出无处保存时用户拿不到成品：结算整笔释放并以 result_lost 结束，绝不重新发起付费生成。
func TestCreativeWorkerRetriesTransientOutputFailure(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_workeroutputretry1"
	seedCreativeRun(f, runID, true)
	f.store.saveOutputErr = errors.New("redis unavailable")
	f.exec.result = &CreativeExecuteResult{
		Outputs:   []CreativeOutput{{Index: 0, Bytes: []byte("img"), Mime: "image/png"}},
		AccountID: 55,
	}

	result, err := f.worker.process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, 0, f.repo.runs[runID].AttemptCount)
	require.Equal(t, CreativeRunStatusResultLost, f.repo.runs[runID].Status)
	require.Equal(t, CreativeRunOutputStatusFailed, f.repo.outputs[runID][0].Status)

	result, err = f.worker.process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, 1, f.exec.calls)
	require.Equal(t, 0, f.billing.captureN)
	require.Equal(t, 1, f.billing.releaseN)
}

// 成功结果未写入数据库时成品已丢失：栅栏期内不重发、不提前释放，超过原执行时限后失败并释放。
func TestCreativeWorkerProviderPersistenceFailureDoesNotReplay(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_providerdbfail"
	seedCreativeRun(f, runID, true)
	f.repo.recordProviderErr = errors.New("database unavailable")
	f.exec.result = &CreativeExecuteResult{Outputs: []CreativeOutput{{Index: 0, Bytes: []byte("img"), Mime: "image/png"}}, AccountID: 55}
	first, err := f.worker.process(context.Background(), runID)
	require.NoError(t, err)
	require.False(t, first.Terminal)
	require.Equal(t, 1, f.exec.calls)
	// 模拟数据库恢复及 worker 重启；仅运行状态和执行栅栏持久存在，内存图片已丢失。
	f.repo.recordProviderErr = nil
	for range 3 {
		next, err := f.worker.process(context.Background(), runID)
		require.NoError(t, err)
		require.False(t, next.Terminal)
		require.LessOrEqual(t, next.RequeueAfter, defaultCreativeUnconfirmedRequeueDelay)
	}
	require.Equal(t, 1, f.exec.calls)
	require.Equal(t, CreativeRunStatusRunning, f.repo.runs[runID].Status)
	require.Equal(t, "PROVIDER_RESULT_UNCONFIRMED", *f.repo.runs[runID].ErrorCode)
	require.Equal(t, 0, f.billing.releaseN)
	past := time.Now().Add(-defaultCreativeUnconfirmedGrace - time.Second)
	f.repo.runs[runID].ImageExecutionDeadlineAt = &past
	last, err := f.worker.process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, last.Terminal)
	require.Equal(t, 1, f.exec.calls)
	require.Equal(t, CreativeRunStatusFailed, f.repo.runs[runID].Status)
	require.Equal(t, 0, f.billing.captureN)
	require.Equal(t, 1, f.billing.releaseN)
}

// 创建 outbox 延迟到执行期间才处理时，不能擦除栅栏或释放尚未核对的预占。
func TestCreativeProvisionRecoveryPreservesExecutionFence(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_outboxfence"
	seedCreativeRun(f, runID, true)
	run := f.repo.runs[runID]
	run.Status = CreativeRunStatusRunning
	run.ProvisioningPhase = CreativeProvisioningPhaseProviderInflight
	f.service.Queue = f.queue
	require.NoError(t, f.service.reconcileCreativeProvision(context.Background(), run))
	require.Equal(t, CreativeProvisioningPhaseProviderInflight, run.ProvisioningPhase)
	require.Equal(t, CreativeRunStatusRunning, run.Status)
	require.Equal(t, 0, f.billing.releaseN)
}

// 泛5xx无法证明未受理，不能换号重发；用户没拿到成品，一次执行后直接失败并释放。
func TestCreativeWorkerRetryableError(t *testing.T) {
	f := newCreativeWorkerFixture()
	id := "crun_workerretry0001"
	seedCreativeRun(f, id, true)
	f.exec.err = creativeHTTPStatusError(503, "upstream busy")
	for range 3 {
		result, err := f.worker.process(context.Background(), id)
		require.NoError(t, err)
		require.True(t, result.Terminal)
	}
	require.Equal(t, 1, f.exec.calls)
	require.Zero(t, f.repo.runs[id].AttemptCount)
	require.Equal(t, 1, f.billing.releaseN)
	require.Equal(t, CreativeRunStatusFailed, f.repo.runs[id].Status)
}

func TestCreativeWorkerNonRetryableError(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_workernonretry01"
	seedCreativeRun(f, runID, true)
	f.exec.err = creativeNonRetryableError("grok platform does not support creative operation edit")

	result, err := f.worker.process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, 1, f.exec.calls)
	// 不可重试错误不消耗 attempt，直接失败并释放。
	run := f.repo.runs[runID]
	require.Equal(t, CreativeRunStatusFailed, run.Status)
	require.Equal(t, 0, run.AttemptCount)
	require.Equal(t, 1, f.billing.releaseN)
}

// TestCreativeWorkerCancelRace 校验执行期间进入 cancelled：界面显示失败、用户拿不到成品，因此只释放不扣费。
func TestCreativeWorkerCancelRace(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_workercancel001"
	seedCreativeRun(f, runID, true)
	f.exec.result = &CreativeExecuteResult{
		Outputs:   []CreativeOutput{{Index: 0, Bytes: []byte("img"), Mime: "image/png"}},
		AccountID: 55,
	}
	// 模拟执行期间进入 cancelled：execute 返回前把任务置为 cancelled。
	f.exec.onExecute = func(runID string) {
		run := f.repo.runs[runID]
		run.Status = CreativeRunStatusCancelled
	}

	result, err := f.worker.process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, result.Terminal)

	run := f.repo.runs[runID]
	require.Equal(t, CreativeRunStatusCancelled, run.Status)
	require.Equal(t, 0, f.billing.captureN)
	// 同步结算与 worker 结算都可能触发释放，但只使用同一幂等请求号，账本只生效一次。
	require.NotEmpty(t, f.billing.releaseIDs)
	for _, id := range f.billing.releaseIDs {
		require.Equal(t, CreativeReleaseRequestID(runID), id)
	}
	require.Nil(t, run.ActualCost)
}

// TestCreativeWorkerPayloadLost 校验载荷过期：result_lost 且未执行的预占被释放。
func TestCreativeWorkerPayloadLost(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_workerlost00001"
	seedCreativeRun(f, runID, false)

	result, err := f.worker.process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, 0, f.exec.calls)

	run := f.repo.runs[runID]
	require.Equal(t, CreativeRunStatusResultLost, run.Status)
	// provider 未执行：释放预占。
	require.Equal(t, 1, f.billing.releaseN)
	require.Equal(t, 0, f.billing.captureN)
}

// TestCreativeWorkerCancelBeforeExecute 校验执行前发现已 cancelled：不调用上游，直接清理出队。
func TestCreativeWorkerCancelBeforeExecute(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_workerprecancel1"
	seedCreativeRun(f, runID, true)
	f.repo.runs[runID].Status = CreativeRunStatusCancelled

	result, err := f.worker.process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, 0, f.exec.calls)
	// 任务在入队前已释放预占：worker 只幂等清理，不重复释放。
	require.Equal(t, 0, f.billing.releaseN)
}

// TestCreativeWorkerRunOnceAck 校验完整 RunOnce 链路：Reserve → 锁 → 处理 → Ack。
func TestCreativeWorkerRunOnceAck(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_workerrunonce001"
	seedCreativeRun(f, runID, true)
	f.queue.reserveBatch = []string{runID}
	f.exec.result = &CreativeExecuteResult{
		Outputs:   []CreativeOutput{{Index: 0, Bytes: []byte("img"), Mime: "image/png"}},
		AccountID: 55,
	}

	require.NoError(t, f.worker.RunOnce(context.Background()))
	require.Equal(t, []string{runID}, f.queue.acked)
	require.Empty(t, f.queue.requeued)
	require.NotNil(t, f.queue.lastLock)
	require.True(t, f.queue.lastLock.released)
	require.Equal(t, CreativeRunStatusSucceeded, f.repo.runs[runID].Status)
}
