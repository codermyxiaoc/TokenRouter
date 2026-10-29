//go:build unit

package service

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func (r *creativeFakeRunRepo) BeginCreativeImageAttempt(ctx context.Context, id string, accountID int64, budget time.Duration, now time.Time) (*CreativeRun, error) {
	run, err := r.GetCreativeRunByRunID(ctx, id)
	if err != nil {
		return nil, err
	}
	ids, deadline, err := CreativeImageAttemptBudget(run, accountID, budget, now)
	if err != nil {
		return nil, err
	}
	run.ImageAttemptAccountIDs = ids
	run.ImageExecutionDeadlineAt = &deadline
	run.AccountID = &accountID
	run.ProvisioningPhase = CreativeProvisioningPhaseProviderInflight
	run.Version++
	r.setAccountN++
	copy := *run
	copy.ImageAttemptAccountIDs = append([]int64(nil), run.ImageAttemptAccountIDs...)
	return &copy, nil
}
func (r *creativeFakeRunRepo) AuthorizeCreativeImageRetry(ctx context.Context, id string, accountID int64) error {
	run, err := r.GetCreativeRunByRunID(ctx, id)
	if err != nil {
		return err
	}
	if run.Status != CreativeRunStatusRunning || run.ProvisioningPhase != CreativeProvisioningPhaseProviderInflight || run.AccountID == nil || *run.AccountID != accountID || run.ProviderResultRecordedAt != nil {
		return ErrCreativeInvalidTransition
	}
	run.ProvisioningPhase = CreativeProvisioningPhaseProviderRetry
	run.Version++
	return nil
}
func (r *parallelCreativeRunRepo) BeginCreativeImageAttempt(ctx context.Context, id string, accountID int64, budget time.Duration, now time.Time) (*CreativeRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.creativeFakeRunRepo.BeginCreativeImageAttempt(ctx, id, accountID, budget, now)
}
func (r *parallelCreativeRunRepo) AuthorizeCreativeImageRetry(ctx context.Context, id string, accountID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.creativeFakeRunRepo.AuthorizeCreativeImageRetry(ctx, id, accountID)
}

type creativeRoutingTestExecutor struct {
	calls                []int64
	deadlines            []time.Time
	firstErr, errorEvery error
	onExecute            func(CreativeRun)
}

func (e *creativeRoutingTestExecutor) Prepare(ctx context.Context, run CreativeRun) (*CreativeExecution, error) {
	id := int64(55)
	if len(run.ImageAttemptAccountIDs) > 0 {
		id = 56
	}
	return &CreativeExecution{Account: &Account{ID: id, Platform: PlatformGemini}, UpstreamModel: run.Model, ReleaseFunc: func() {}}, nil
}
func (e *creativeRoutingTestExecutor) Execute(ctx context.Context, run CreativeRun, p CreativeRunPayload, x *CreativeExecution) (*CreativeExecuteResult, error) {
	e.calls = append(e.calls, x.Account.ID)
	if run.ImageExecutionDeadlineAt != nil {
		e.deadlines = append(e.deadlines, *run.ImageExecutionDeadlineAt)
	}
	if e.onExecute != nil {
		e.onExecute(run)
	}
	if e.errorEvery != nil {
		return nil, e.errorEvery
	}
	if len(e.calls) == 1 && e.firstErr != nil {
		return nil, e.firstErr
	}
	return &CreativeExecuteResult{AccountID: x.Account.ID, Outputs: []CreativeOutput{{Index: 0, Bytes: []byte("img"), Mime: "image/png"}}}, nil
}
func (e *creativeRoutingTestExecutor) IsRetryable(err error) bool {
	return IsRetryableCreativeError(err)
}

// 安全拒绝当次就选择同组另一个账号，重复领取不能刷新deadline或再次生成。
func TestCreativeImageSafeSwitchPersistsBudgetAndSettlesOnce(t *testing.T) {
	f := newCreativeWorkerFixture()
	id := "crun_image_safe_switch"
	seedCreativeRun(f, id, true)
	e := &creativeRoutingTestExecutor{firstErr: WrapImageAttemptError(creativeHTTPStatusError(429, "rate limited"), ImageAttemptDecision{Outcome: ImageAttemptRejected, RetryDifferentAccount: true, AttemptFault: true})}
	f.worker.executor = e
	result, err := f.worker.process(context.Background(), id)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, []int64{55, 56}, e.calls)
	require.Equal(t, e.deadlines[0], e.deadlines[1])
	require.Equal(t, []int64{55, 56}, f.repo.runs[id].ImageAttemptAccountIDs)
	require.Equal(t, 1, f.billing.captureN)
	require.Zero(t, f.billing.releaseN)
	_, err = f.worker.process(context.Background(), id)
	require.NoError(t, err)
	require.Len(t, e.calls, 2)
	require.Equal(t, 1, f.billing.captureN)
}

// 未知、已受理但无图或产物未确认都不换号重发；用户没拿到成品，一次执行后直接失败并释放。
func TestCreativeImageUnknownNeverSwitchesOrReleases(t *testing.T) {
	for _, outcome := range []ImageAttemptOutcome{ImageAttemptUnknown, ImageAttemptAccepted, ImageAttemptResultReady} {
		t.Run(string(outcome), func(t *testing.T) {
			f := newCreativeWorkerFixture()
			id := "crun_image_unknown"
			seedCreativeRun(f, id, true)
			e := &creativeRoutingTestExecutor{errorEvery: WrapImageAttemptError(errors.New("unknown image result"), ImageAttemptDecision{Outcome: outcome})}
			f.worker.executor = e
			for range 3 {
				result, err := f.worker.process(context.Background(), id)
				require.NoError(t, err)
				require.True(t, result.Terminal)
			}
			require.Equal(t, []int64{55}, e.calls)
			require.Equal(t, 1, f.billing.releaseN)
			require.Zero(t, f.billing.captureN)
			require.Equal(t, CreativeRunStatusFailed, f.repo.runs[id].Status)
			require.Equal(t, CreativeResultUnavailableCode, *f.repo.runs[id].ErrorCode)
		})
	}
}
func TestCreativeImageTwoRejectionsStopWithoutThirdSend(t *testing.T) {
	f := newCreativeWorkerFixture()
	id := "crun_image_two_rejects"
	seedCreativeRun(f, id, true)
	e := &creativeRoutingTestExecutor{errorEvery: WrapImageAttemptError(creativeHTTPStatusError(401, "auth"), ImageAttemptDecision{Outcome: ImageAttemptRejected, RetryDifferentAccount: true, AttemptFault: true})}
	f.worker.executor = e
	result, err := f.worker.process(context.Background(), id)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, []int64{55, 56}, e.calls)
	require.Equal(t, 1, f.billing.releaseN)
	require.Zero(t, f.billing.captureN)
}

// 参数和审核拒绝不尝试备用账号；失败释放同样幂等。
func TestCreativeImageRequestRejectionReleasesOnceWithoutSwitch(t *testing.T) {
	f := newCreativeWorkerFixture()
	id := "crun_image_request_rejected"
	seedCreativeRun(f, id, true)
	e := &creativeRoutingTestExecutor{errorEvery: WrapImageAttemptError(creativeHTTPStatusError(400, "invalid image"), ImageAttemptDecision{Outcome: ImageAttemptRejected})}
	f.worker.executor = e
	for range 2 {
		result, err := f.worker.process(context.Background(), id)
		require.NoError(t, err)
		require.True(t, result.Terminal)
	}
	require.Equal(t, []int64{55}, e.calls)
	require.Equal(t, 1, f.billing.releaseN)
	require.Zero(t, f.billing.captureN)
}
func TestCreativeImageDeadlineAndLocalQueueDoNotResetBudget(t *testing.T) {
	f := newCreativeWorkerFixture()
	id := "crun_image_expired"
	seedCreativeRun(f, id, true)
	expired := time.Now().Add(-time.Second)
	f.repo.runs[id].ImageExecutionDeadlineAt = &expired
	result, err := f.worker.process(context.Background(), id)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Zero(t, f.exec.calls)
	f = newCreativeWorkerFixture()
	id = "crun_image_local_queue"
	seedCreativeRun(f, id, true)
	f.exec.execPrepareErr = ErrCreativeExecutionPending
	_, err = f.worker.process(context.Background(), id)
	require.NoError(t, err)
	require.Empty(t, f.repo.runs[id].ImageAttemptAccountIDs)
	require.Nil(t, f.repo.runs[id].ImageExecutionDeadlineAt)
	require.Zero(t, f.repo.runs[id].AttemptCount)
}

// 旧无账号执行记录不能因新增预算字段为空再次生成；其原执行时限早已过去，直接失败并释放。
func TestCreativeImageLegacyUnknownIsNotGrantedFreshAttempts(t *testing.T) {
	f := newCreativeWorkerFixture()
	id := "crun_image_legacy_unknown"
	seedCreativeRun(f, id, true)
	run := f.repo.runs[id]
	run.Status = CreativeRunStatusRunning
	run.AttemptCount = 1
	run.ProvisioningPhase = CreativeProvisioningPhaseProviderRetry
	result, err := f.worker.process(context.Background(), id)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Zero(t, f.exec.calls)
	require.Equal(t, 1, f.billing.releaseN)
	require.Zero(t, f.billing.captureN)
	require.Equal(t, CreativeRunStatusFailed, run.Status)
}

// 原执行者可能仍在运行时只等待其结果落库，不重发也不提前释放；超过原执行时限加宽限期后才释放。
func TestCreativeImageFenceWaitsUntilDeadlineThenReleases(t *testing.T) {
	f := newCreativeWorkerFixture()
	id := "crun_image_fence_wait"
	seedCreativeRun(f, id, true)
	run := f.repo.runs[id]
	run.Status = CreativeRunStatusRunning
	run.ProvisioningPhase = CreativeProvisioningPhaseProviderInflight
	run.ImageAttemptAccountIDs = []int64{55}
	future := time.Now().Add(time.Minute)
	run.ImageExecutionDeadlineAt = &future
	result, err := f.worker.process(context.Background(), id)
	require.NoError(t, err)
	require.False(t, result.Terminal)
	require.Positive(t, result.RequeueAfter)
	require.LessOrEqual(t, result.RequeueAfter, defaultCreativeUnconfirmedRequeueDelay)
	require.Zero(t, f.exec.calls)
	require.Zero(t, f.billing.releaseN)

	past := time.Now().Add(-defaultCreativeUnconfirmedGrace - time.Second)
	run.ImageExecutionDeadlineAt = &past
	result, err = f.worker.process(context.Background(), id)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Zero(t, f.exec.calls)
	require.Equal(t, 1, f.billing.releaseN)
	require.Zero(t, f.billing.captureN)
	require.Equal(t, CreativeRunStatusFailed, run.Status)
}

// 音频保留既有有限重试，不写图片账号预算或更改其资金处理。
func TestCreativeImageRoutingDoesNotChangeTTSRetries(t *testing.T) {
	f := newCreativeWorkerFixture()
	id := "crun_tts_unchanged"
	seedCreativeRun(f, id, true)
	f.repo.runs[id].Operation = CreativeOperationTTS
	f.exec.err = creativeHTTPStatusError(503, "busy")
	for i := 0; i < 3; i++ {
		_, err := f.worker.process(context.Background(), id)
		require.NoError(t, err)
	}
	require.Equal(t, 3, f.exec.calls)
	require.Empty(t, f.repo.runs[id].ImageAttemptAccountIDs)
	require.Nil(t, f.repo.runs[id].ImageExecutionDeadlineAt)
	require.Equal(t, 1, f.billing.releaseN)
}
