//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// 稳定分页测试替身不提供生成方法，恢复只能重新排入已受理任务。
type batchQueueRecoveryRepo struct {
	jobs  []*BatchImageJob
	after []int64
}

func (r *batchQueueRecoveryRepo) ListBatchImageJobsForQueueRecovery(_ context.Context, after int64, limit int) ([]*BatchImageJob, error) {
	r.after = append(r.after, after)
	var result []*BatchImageJob
	for _, job := range r.jobs {
		if job.ID > after {
			result = append(result, job)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

type batchRecoverableQueue struct {
	BatchImageQueue
	calls []string
	errs  map[string]error
}

func (q *batchRecoverableQueue) EnsureQueued(_ context.Context, id string) error {
	q.calls = append(q.calls, id)
	return q.errs[id]
}

func TestBatchImageQueueRecoveryPagesPastAlreadyQueuedAndFailures(t *testing.T) {
	repo := &batchQueueRecoveryRepo{jobs: []*BatchImageJob{
		{ID: 1, BatchID: "imgbatch_one"}, {ID: 2, BatchID: "imgbatch_two"}, {ID: 3, BatchID: "imgbatch_three"},
	}}
	queue := &batchRecoverableQueue{errs: map[string]error{
		"imgbatch_one": ErrBatchImageAlreadyQueued, "imgbatch_two": errors.New("redis unavailable"),
	}}
	svc := &BatchImageQueueRecoveryService{Repo: repo, Queue: queue, Limit: 2}
	n, err := svc.RecoverOnce(context.Background())
	require.Error(t, err)
	require.Zero(t, n)
	n, err = svc.RecoverOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	delete(queue.errs, "imgbatch_two")
	n, err = svc.RecoverOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, []int64{0, 2, 0}, repo.after)
	require.Equal(t, []string{"imgbatch_one", "imgbatch_two", "imgbatch_three", "imgbatch_one", "imgbatch_two"}, queue.calls)
}

func TestBatchImageQueueRecoveryCancellationDoesNotSkipUnprocessedPage(t *testing.T) {
	repo := &batchQueueRecoveryRepo{jobs: []*BatchImageJob{{ID: 1, BatchID: "imgbatch_one"}}}
	queue := &batchRecoverableQueue{}
	svc := &BatchImageQueueRecoveryService{Repo: repo, Queue: queue, Limit: 1}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.RecoverOnce(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, queue.calls)
	_, err = svc.RecoverOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int64{0, 0}, repo.after)
}
