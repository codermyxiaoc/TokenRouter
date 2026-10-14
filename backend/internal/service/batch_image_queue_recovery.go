package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// BatchImageQueueRecoveryRepository 只返回已取得上游任务 ID 的持久任务，恢复不得重新提交生成。
type BatchImageQueueRecoveryRepository interface {
	ListBatchImageJobsForQueueRecovery(context.Context, int64, int) ([]*BatchImageJob, error)
}

// BatchImageQueueRecoveryService 以数据库补齐队列，不改变任务、价卡或资金状态。
// @project-doc docs/domains/batch_image_jobs.md#batch_queue_recovery
type BatchImageQueueRecoveryService struct {
	Repo  BatchImageQueueRecoveryRepository
	Queue BatchImageQueue
	Limit int
	mu    sync.Mutex
	after int64
}

func (s *BatchImageQueueRecoveryService) RecoverOnce(ctx context.Context) (int, error) {
	if s == nil || s.Repo == nil || s.Queue == nil {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := s.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	jobs, err := s.Repo.ListBatchImageJobsForQueueRecovery(ctx, s.after, limit)
	if err != nil {
		return 0, err
	}
	recovered := 0
	var lastErr error
	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return recovered, err
		}
		if job == nil {
			continue
		}
		if recovery, ok := s.Queue.(BatchImageQueueRecovery); ok {
			err = recovery.EnsureQueued(ctx, job.BatchID)
		} else {
			err = s.Queue.Enqueue(ctx, job.BatchID)
		}
		if err == nil {
			recovered++
		} else if !errors.Is(err, ErrBatchImageAlreadyQueued) {
			lastErr = fmt.Errorf("batch image %s queue recovery failed: %w", job.BatchID, err)
		}
		// 个别任务入队失败不阻塞后续任务；完整巡回后会再次尝试该任务。
		s.after = job.ID
	}
	if len(jobs) < limit {
		s.after = 0
	}
	return recovered, lastErr
}
