//go:build unit

package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 租约替身可在准备步骤中主动失效，验证旧创建者不能继续写入或补偿。
type creativeProvisionTestLock struct {
	owned      atomic.Bool
	refreshErr error
	checks     atomic.Int32
	released   bool
	releaseErr error
	deadline   time.Duration
}

func (l *creativeProvisionTestLock) Refresh(ctx context.Context, ttl time.Duration) (bool, error) {
	l.checks.Add(1)
	if deadline, ok := ctx.Deadline(); ok {
		l.deadline = time.Until(deadline)
	}
	return l.owned.Load(), l.refreshErr
}
func (l *creativeProvisionTestLock) Release(ctx context.Context) error {
	l.released = true
	l.releaseErr = ctx.Err()
	return ctx.Err()
}

type creativeProvisionTestQueue struct {
	*creativeFakeQueue
	lock         *creativeProvisionTestLock
	busy         bool
	afterAcquire func()
	afterEnqueue func()
}

func (q *creativeProvisionTestQueue) TryAcquireJobLock(context.Context, string, time.Duration) (CreativeRunJobLock, bool, error) {
	if q.busy {
		return nil, false, nil
	}
	q.lock.owned.Store(true)
	if q.afterAcquire != nil {
		q.afterAcquire()
	}
	return q.lock, true, nil
}
func (q *creativeProvisionTestQueue) Enqueue(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := q.creativeFakeQueue.Enqueue(ctx, id); err != nil {
		return err
	}
	if q.afterEnqueue != nil {
		q.afterEnqueue()
	}
	return nil
}

type creativeProvisionTestRepo struct {
	*creativeFakeRunRepo
	lock             *creativeProvisionTestLock
	createdUnderLock bool
	reads            int
	readRequiresLock bool
}

func (r *creativeProvisionTestRepo) CreateCreativeRun(ctx context.Context, p CreateCreativeRunParams) (*CreativeRun, error) {
	r.createdUnderLock = r.lock.owned.Load()
	return r.creativeFakeRunRepo.CreateCreativeRun(ctx, p)
}
func (r *creativeProvisionTestRepo) GetCreativeRunByRunID(ctx context.Context, id string) (*CreativeRun, error) {
	r.reads++
	if r.readRequiresLock && !r.lock.owned.Load() {
		return nil, errors.New("读取发生在取得锁之前")
	}
	run, err := r.creativeFakeRunRepo.GetCreativeRunByRunID(ctx, id)
	if err != nil {
		return nil, err
	}
	copy := *run
	return &copy, nil
}

func creativeProvisionLeaseFixture() (*CreativePublicService, *creativeProvisionTestQueue, *creativeProvisionTestRepo) {
	s := newCreativeTestService()
	lock := &creativeProvisionTestLock{}
	queue := &creativeProvisionTestQueue{creativeFakeQueue: &creativeFakeQueue{}, lock: lock}
	repo := &creativeProvisionTestRepo{creativeFakeRunRepo: s.Repo.(*creativeFakeRunRepo), lock: lock}
	s.Queue, s.Repo = queue, repo
	return s, queue, repo
}

func TestCreativeProvisionLeaseAcquiredBeforeCreatingVisibleRun(t *testing.T) {
	s, queue, repo := creativeProvisionLeaseFixture()
	run, err := s.CreateRun(context.Background(), testCreativeScope(7), validCreateParams(), "lease-order")
	require.NoError(t, err)
	require.True(t, repo.createdUnderLock)
	require.True(t, queue.lock.released)
	require.NoError(t, queue.lock.releaseErr)
	require.GreaterOrEqual(t, queue.lock.checks.Load(), int32(7))
	require.LessOrEqual(t, queue.lock.deadline, 5*time.Second)
	require.Equal(t, CreativeProvisioningPhaseEnqueued, repo.runs[run.ID].ProvisioningPhase)
	require.Equal(t, []string{run.ID}, queue.enqueued)
}

func TestCreativeProvisionLeaseBusyDoesNotCreateOrReserve(t *testing.T) {
	s, queue, repo := creativeProvisionLeaseFixture()
	queue.busy = true
	_, err := s.CreateRun(context.Background(), testCreativeScope(7), validCreateParams(), "lease-busy")
	require.ErrorIs(t, err, ErrCreativeLockNotAcquired)
	require.Empty(t, repo.runs)
	require.Zero(t, s.BillingRepo.(*creativeFakeBillingRepo).reserveN)
	require.Empty(t, queue.enqueued)
}

type creativeProvisionLoseBilling struct {
	*creativeFakeBillingRepo
	lock         *creativeProvisionTestLock
	transportErr bool
}

func (b *creativeProvisionLoseBilling) ReserveBatchImageBalance(ctx context.Context, c *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error) {
	result, err := b.creativeFakeBillingRepo.ReserveBatchImageBalance(ctx, c)
	if b.transportErr {
		b.lock.refreshErr = errors.New("Redis连接已失效")
	} else {
		b.lock.owned.Store(false)
	}
	return result, err
}
func TestCreativeProvisionLeaseLossAfterReserveDoesNotWriteOrRefund(t *testing.T) {
	for _, transportErr := range []bool{false, true} {
		s, queue, repo := creativeProvisionLeaseFixture()
		billing := &creativeProvisionLoseBilling{creativeFakeBillingRepo: s.BillingRepo.(*creativeFakeBillingRepo), lock: queue.lock, transportErr: transportErr}
		s.BillingRepo = billing
		_, err := s.CreateRun(context.Background(), testCreativeScope(7), validCreateParams(), "lease-lost")
		require.Error(t, err)
		require.Len(t, repo.runs, 1)
		require.Equal(t, 1, billing.reserveN)
		require.Zero(t, billing.releaseN)
		require.Empty(t, repo.transition)
		require.Empty(t, s.TransientStore.(*creativeFakeTransient).payloads)
		require.Empty(t, queue.enqueued)
		require.True(t, queue.lock.released)
		require.NoError(t, queue.lock.releaseErr)
	}
}

type creativeProvisionCancelStore struct {
	*creativeFakeTransient
	cancel context.CancelFunc
}

func (s *creativeProvisionCancelStore) SavePayload(ctx context.Context, id string, p *CreativeRunPayload) error {
	s.cancel()
	return ctx.Err()
}
func TestCreativeProvisionCancelledCreatorLeavesRecoveryIntentAndUnlocks(t *testing.T) {
	s, queue, repo := creativeProvisionLeaseFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.TransientStore = &creativeProvisionCancelStore{creativeFakeTransient: s.TransientStore.(*creativeFakeTransient), cancel: cancel}
	_, err := s.CreateRun(ctx, testCreativeScope(7), validCreateParams(), "cancel-create")
	require.Error(t, err)
	require.Empty(t, repo.transition)
	require.Empty(t, queue.enqueued)
	require.True(t, queue.lock.released)
	require.NoError(t, queue.lock.releaseErr)
	for _, run := range repo.runs {
		require.Equal(t, CreativeRunStatusQueued, run.Status)
		require.Equal(t, CreativeProvisioningPhaseHoldReserved, run.ProvisioningPhase)
	}
}

func TestCreativeProvisionLossAfterEnqueueDoesNotFailRunnableTask(t *testing.T) {
	s, queue, repo := creativeProvisionLeaseFixture()
	queue.afterEnqueue = func() { queue.lock.owned.Store(false) }
	_, err := s.CreateRun(context.Background(), testCreativeScope(7), validCreateParams(), "lost-after-enqueue")
	require.Error(t, err)
	require.Len(t, queue.enqueued, 1)
	require.Empty(t, repo.transition)
	require.Zero(t, s.BillingRepo.(*creativeFakeBillingRepo).releaseN)
	for _, run := range repo.runs {
		require.Equal(t, CreativeRunStatusQueued, run.Status)
		require.Equal(t, CreativeProvisioningPhaseTransientSaved, run.ProvisioningPhase)
	}
}

func TestCreativeProvisionRecoveryReadsCurrentRunOnlyAfterLock(t *testing.T) {
	s, queue, repo := creativeProvisionLeaseFixture()
	run := creativeUsageTestRun("run-stale-provision")
	run.Status = CreativeRunStatusQueued
	run.ProvisioningPhase = CreativeProvisioningPhaseCreated
	repo.runs[run.RunID] = run
	repo.readRequiresLock = true
	queue.afterAcquire = func() { run.ProvisioningPhase = CreativeProvisioningPhaseEnqueued }
	err := s.reconcileCreativeOutboxEvent(context.Background(), CreativeRunOutbox{RunID: run.RunID, Operation: CreativeRunOutboxProvision})
	require.NoError(t, err)
	require.Equal(t, CreativeRunStatusQueued, run.Status)
	require.Equal(t, CreativeProvisioningPhaseComplete, run.ProvisioningPhase)
	require.Equal(t, []string{run.RunID}, queue.enqueued)
	require.Empty(t, repo.transition)
	require.Zero(t, s.BillingRepo.(*creativeFakeBillingRepo).releaseN)
}

type creativeProvisionLoadFailure struct {
	*creativeFakeTransient
	err error
}

func (s *creativeProvisionLoadFailure) LoadPayload(context.Context, string) (*CreativeRunPayload, error) {
	return nil, s.err
}
func TestCreativeProvisionTransientFailureOnlyReleasesConfirmedLoss(t *testing.T) {
	for _, testErr := range []error{ErrCreativeTransientUnavailable, context.DeadlineExceeded, ErrCreativeTransientNotFound, ErrCreativeTransientCorrupt} {
		s, queue, repo := creativeProvisionLeaseFixture()
		run := creativeUsageTestRun("run-store-error")
		run.Status = CreativeRunStatusQueued
		run.ProvisioningPhase = CreativeProvisioningPhaseTransientSaved
		run.ActualCost = nil
		run.ProviderResultRecordedAt = nil
		run.CompletedAt = nil
		repo.runs[run.RunID] = run
		s.TransientStore = &creativeProvisionLoadFailure{creativeFakeTransient: s.TransientStore.(*creativeFakeTransient), err: testErr}
		err := s.reconcileCreativeOutboxEvent(context.Background(), CreativeRunOutbox{RunID: run.RunID, Operation: CreativeRunOutboxProvision})
		if errors.Is(testErr, ErrCreativeTransientNotFound) || errors.Is(testErr, ErrCreativeTransientCorrupt) {
			require.NoError(t, err)
			require.Equal(t, CreativeRunStatusResultLost, run.Status)
			require.Equal(t, 1, s.BillingRepo.(*creativeFakeBillingRepo).releaseN)
		} else {
			require.Error(t, err)
			require.Equal(t, CreativeRunStatusQueued, run.Status)
			require.Zero(t, s.BillingRepo.(*creativeFakeBillingRepo).releaseN)
		}
		require.Empty(t, queue.enqueued)
	}
}

func TestCreativeProvisionFailureMessageIsSafeForHistoricalExecutions(t *testing.T) {
	require.Equal(t, "生成任务准备失败，请重新生成", creativeCanvasFailureMessage("PROVISIONING_INCOMPLETE"))
	require.Equal(t, "生成任务准备失败，请重新生成", creativeCanvasExecutionFailureMessage("audio", "PROVISIONING_INCOMPLETE"))
}
