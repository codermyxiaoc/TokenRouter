//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 持久 outbox 替身只确认真实完成，不能把 Redis 入队当作退款已完成。
type creativeRecoveryOutbox struct {
	CreativeRunOutboxRepository
	event              CreativeRunOutbox
	completed, retried int
}

func (o *creativeRecoveryOutbox) Claim(context.Context, string, int, time.Duration) ([]CreativeRunOutbox, error) {
	return []CreativeRunOutbox{o.event}, nil
}
func (o *creativeRecoveryOutbox) Complete(context.Context, int64, string) error {
	o.completed++
	return nil
}
func (o *creativeRecoveryOutbox) Retry(context.Context, int64, string, time.Time, string) error {
	o.retried++
	return nil
}

type creativeRecoveryLock struct {
	releaseContextErr error
	released          bool
}

func (l *creativeRecoveryLock) Release(ctx context.Context) error {
	l.released, l.releaseContextErr = true, ctx.Err()
	return l.releaseContextErr
}

type creativeRecoveryQueue struct {
	*creativeFakeQueue
	busy bool
	lock *creativeRecoveryLock
	ttl  time.Duration
}

func (q *creativeRecoveryQueue) TryAcquireJobLock(_ context.Context, _ string, ttl time.Duration) (CreativeRunJobLock, bool, error) {
	q.ttl = ttl
	if q.busy {
		return nil, false, nil
	}
	q.lock = &creativeRecoveryLock{}
	return q.lock, true, nil
}

type creativeRecoveryReleaseBilling struct {
	UsageBillingRepository
	err            error
	calls, applied int
	seen           map[string]bool
	cancel         context.CancelFunc
}

func (b *creativeRecoveryReleaseBilling) ReleaseBatchImageBalance(ctx context.Context, cmd *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error) {
	b.calls++
	if b.cancel != nil {
		b.cancel()
		return nil, ctx.Err()
	}
	if b.err != nil {
		return nil, b.err
	}
	if b.seen == nil {
		b.seen = map[string]bool{}
	}
	applied := !b.seen[cmd.RequestID]
	if applied {
		b.applied++
	}
	b.seen[cmd.RequestID] = true
	return &BatchImageBalanceHoldResult{Applied: applied}, nil
}

func creativeReleaseRecoveryFixture() (*CreativePublicService, *CreativeRun, *creativeRecoveryOutbox, *creativeRecoveryQueue, *creativeRecoveryReleaseBilling) {
	svc := newCreativeTestService()
	run := creativeUsageTestRun("crun_release_recovery")
	run.Status, run.ReleaseTargetStatus = CreativeRunStatusReleasePending, CreativeRunStatusCancelled
	run.ActualCost, run.ProviderResultRecordedAt, run.CompletedAt = nil, nil, nil
	svc.Repo.(*creativeFakeRunRepo).runs[run.RunID] = run
	outbox := &creativeRecoveryOutbox{event: CreativeRunOutbox{ID: 1, RunID: run.RunID, Operation: CreativeRunOutboxRelease, LeaseToken: "lease"}}
	queue := &creativeRecoveryQueue{creativeFakeQueue: &creativeFakeQueue{}}
	billing := &creativeRecoveryReleaseBilling{}
	svc.Outbox, svc.Queue, svc.BillingRepo = outbox, queue, billing
	return svc, run, outbox, queue, billing
}

func TestCreativeReleaseOutboxRetainsIntentUntilRefundSucceeds(t *testing.T) {
	svc, run, outbox, queue, billing := creativeReleaseRecoveryFixture()
	billing.err = errors.New("billing unavailable")
	n, err := svc.ReconcileCreativeOutboxOnce(context.Background())
	require.Error(t, err)
	require.Zero(t, n)
	require.Equal(t, CreativeRunStatusReleasePending, run.Status)
	require.Zero(t, outbox.completed)
	require.Equal(t, 1, outbox.retried)
	require.Empty(t, queue.enqueued)
	billing.err = nil
	n, err = svc.ReconcileCreativeOutboxOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, CreativeRunStatusCancelled, run.Status)
	require.Equal(t, 1, billing.applied)
	require.Equal(t, 2, billing.calls)
	require.Equal(t, 1, outbox.completed)
	// 重复事件不能重复退回预算，也不能复活生成。
	_, err = svc.ReconcileCreativeOutboxOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, billing.calls)
	require.Equal(t, 1, billing.applied)
	require.Empty(t, queue.enqueued)
}

func TestCreativeOutboxLockConflictRetainsIntentWithoutBilling(t *testing.T) {
	for _, operation := range []CreativeRunOutboxOperation{CreativeRunOutboxRelease, CreativeRunOutboxSettle} {
		t.Run(string(operation), func(t *testing.T) {
			svc, run, outbox, queue, billing := creativeReleaseRecoveryFixture()
			outbox.event.Operation, queue.busy = operation, true
			n, err := svc.ReconcileCreativeOutboxOnce(context.Background())
			require.Error(t, err)
			require.Zero(t, n)
			require.Zero(t, outbox.completed)
			require.Equal(t, 1, outbox.retried)
			require.Zero(t, billing.calls)
			require.Equal(t, CreativeRunStatusReleasePending, run.Status)
			require.Empty(t, queue.enqueued)
		})
	}
}

func TestCreativeReleaseOutboxNeverRefundsProviderSuccessOrSettledTerminal(t *testing.T) {
	for _, status := range []string{CreativeRunStatusProviderSucceeded, CreativeRunStatusSettlementPending, CreativeRunStatusSucceeded, CreativeRunStatusResultLost, CreativeRunStatusCancelled} {
		t.Run(status, func(t *testing.T) {
			svc, run, outbox, queue, billing := creativeReleaseRecoveryFixture()
			now, cost := time.Now(), 0.4
			run.Status, run.ProviderResultRecordedAt, run.ActualCost = status, &now, &cost
			_, err := svc.ReconcileCreativeOutboxOnce(context.Background())
			require.NoError(t, err)
			require.Zero(t, billing.calls)
			require.Equal(t, status, run.Status)
			require.Equal(t, 1, outbox.completed)
			require.Empty(t, queue.enqueued)
		})
	}
}

func TestCreativeOutboxCancellationStillReleasesJobLock(t *testing.T) {
	svc, run, outbox, queue, billing := creativeReleaseRecoveryFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	billing.cancel = cancel
	_, err := svc.ReconcileCreativeOutboxOnce(ctx)
	require.Error(t, err)
	require.Equal(t, CreativeRunStatusReleasePending, run.Status)
	require.Zero(t, outbox.completed)
	require.NotNil(t, queue.lock)
	require.True(t, queue.lock.released)
	require.NoError(t, queue.lock.releaseContextErr)
}
