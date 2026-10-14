package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"go.uber.org/zap"
)

// LegacyVideoTaskExecutor 只允许查询和恢复已受理任务，禁止从恢复器再次提交生成。
type LegacyVideoTaskExecutor interface {
	RecoverLegacyVideoTask(context.Context, *LegacyVideoTask) error
}

// LegacyVideoRuntime 的本机并发与数据库租约共同限制后台工作；停机时保留可恢复的持久记录。
type LegacyVideoRuntime struct {
	repo     LegacyVideoTaskRepository
	executor LegacyVideoTaskExecutor
	ctx      context.Context
	cancel   context.CancelFunc
	start    sync.Once
	wg       sync.WaitGroup
	slots    chan struct{}
	interval time.Duration
	jobLimit time.Duration
	lease    time.Duration
}

func NewLegacyVideoRuntime(repo LegacyVideoTaskRepository, executor LegacyVideoTaskExecutor) *LegacyVideoRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	return &LegacyVideoRuntime{repo: repo, executor: executor, ctx: ctx, cancel: cancel,
		slots: make(chan struct{}, 4), interval: 5 * time.Second, jobLimit: 90 * time.Second, lease: 120 * time.Second}
}

// Start 只能启动一次；先 Stop 再 Start 不得复活已关闭应用的恢复器。
func (r *LegacyVideoRuntime) Start() {
	if r == nil || r.repo == nil || r.executor == nil {
		return
	}
	r.start.Do(func() {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			ticker := time.NewTicker(r.interval)
			defer ticker.Stop()
			for r.ctx.Err() == nil {
				r.cycle()
				select {
				case <-r.ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	})
}

// Stop 在关闭数据库前取消所有上游查询，等待有界收尾完成，重复调用仍等待相同工作结束。
func (r *LegacyVideoRuntime) Stop() {
	if r == nil {
		return
	}
	r.cancel()
	r.start.Do(func() {})
	r.wg.Wait()
}

func (r *LegacyVideoRuntime) cycle() {
	// 优先领取已有到期任务，历史回填每轮有上限，不得挤占正在恢复的新任务。
	if available := cap(r.slots) - len(r.slots); available > 0 {
		ctx, cancel := context.WithTimeout(r.ctx, 5*time.Second)
		tasks, err := r.repo.ClaimDue(ctx, time.Now().UTC(), available, r.lease)
		cancel()
		if err != nil && r.ctx.Err() == nil {
			logger.L().Warn("legacy_video.claim_failed")
		}
		for _, task := range tasks {
			if task == nil {
				continue
			}
			r.slots <- struct{}{}
			r.wg.Add(1)
			go func(task *LegacyVideoTask) {
				defer r.wg.Done()
				defer func() { <-r.slots }()
				r.runTask(task)
			}(task)
		}
	}
	if r.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.ctx, 5*time.Second)
	defer cancel()
	if _, err := r.repo.Backfill(ctx, 100); err != nil && r.ctx.Err() == nil {
		logger.L().Warn("legacy_video.backfill_failed")
	}
}

func (r *LegacyVideoRuntime) runTask(task *LegacyVideoTask) {
	originalNext := task.NextPollAt
	ctx, cancel := context.WithTimeout(r.ctx, r.jobLimit)
	err := r.execute(ctx, task)
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	cancel()
	now := time.Now().UTC()
	if err != nil {
		// 错误正文可能含带签名地址或凭据，只保存固定类别，不复制上游或数据库原始错误。
		task.LastError = "视频任务恢复暂时失败，将自动重试"
		if task.BillingStatus == "reconciliation" {
			task.LastError = "视频任务结算依据不足，需核对原任务和账务"
		}
	} else {
		task.LastError = ""
	}
	if task.NextPollAt.Equal(originalNext) || !task.NextPollAt.After(now) {
		delay := 10 * time.Second
		if err != nil {
			delay = legacyVideoRetryDelay(task.Attempts)
		}
		if task.BillingStatus == "reconciliation" {
			delay = time.Hour
		}
		task.NextPollAt = now.Add(delay)
	}
	// 应用关闭或查询超时后仍留短暂独立预算保存状态；旧租约失效则交由新实例恢复。
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	if finishErr := r.repo.Finish(finishCtx, task); finishErr != nil {
		logger.L().Warn("legacy_video.finish_failed", zap.Int64("task_id", task.ID), zap.Bool("lease_lost", errors.Is(finishErr, ErrLegacyVideoTaskLeaseLost)))
	}
}

func (r *LegacyVideoRuntime) execute(ctx context.Context, task *LegacyVideoTask) (err error) {
	// 单个任务的异常不能终止整个恢复器；租约和持久账本负责下一次安全恢复。
	defer func() {
		if recover() != nil {
			err = errors.New("视频任务恢复执行异常")
		}
	}()
	return r.executor.RecoverLegacyVideoTask(ctx, task)
}

func legacyVideoRetryDelay(attempts int) time.Duration {
	if attempts <= 1 {
		return 5 * time.Second
	}
	if attempts >= 9 {
		return 15 * time.Minute
	}
	return (5 * time.Second) << (attempts - 1)
}
