package service

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

type creativeProvisionLeaseContextKey struct{}

// creativeProvisionLease 让创建、执行和补偿共享同一任务锁；租约丢失立即取消所有后续写入。
// 操作预算小于锁 TTL，并在心跳及关键阶段验证 token，防止 Redis 重启后的旧创建者继续入队。
// @project-doc docs/domains/creative_studio.md#task_lifecycle
type creativeProvisionLease struct {
	ctx       context.Context
	cancel    context.CancelFunc
	lock      CreativeRunJobLock
	refresher CreativeRunJobLockRefresher
	lost      atomic.Bool
	done      chan struct{}
}

func (s *CreativePublicService) acquireCreativeProvisionLease(ctx context.Context, runID string) (*creativeProvisionLease, error) {
	if s.Queue == nil {
		return nil, errors.New("creative queue is not configured")
	}
	lock, acquired, err := s.Queue.TryAcquireJobLock(ctx, runID, creativeOutboxLease)
	if err != nil {
		return nil, err
	}
	if !acquired || lock == nil {
		return nil, ErrCreativeLockNotAcquired
	}
	operationCtx, cancel := context.WithTimeout(ctx, creativeOutboxLease/2)
	l := &creativeProvisionLease{cancel: cancel, lock: lock, done: make(chan struct{})}
	l.ctx = context.WithValue(operationCtx, creativeProvisionLeaseContextKey{}, l)
	l.refresher, _ = lock.(CreativeRunJobLockRefresher)
	if l.refresher == nil {
		close(l.done)
		return l, nil
	}
	go func() {
		defer close(l.done)
		ticker := time.NewTicker(creativeOutboxLease / 4)
		defer ticker.Stop()
		for {
			select {
			case <-l.ctx.Done():
				return
			case <-ticker.C:
				if l.check() != nil {
					return
				}
			}
		}
	}()
	return l, nil
}

func (l *creativeProvisionLease) check() error {
	if l.lost.Load() {
		return ErrCreativeLeaseLost
	}
	if err := l.ctx.Err(); err != nil {
		return err
	}
	if l.refresher == nil {
		return nil
	}
	checkCtx, cancel := context.WithTimeout(l.ctx, 5*time.Second)
	defer cancel()
	owned, err := l.refresher.Refresh(checkCtx, creativeOutboxLease)
	if err != nil || !owned {
		l.lost.Store(true)
		l.cancel()
		return ErrCreativeLeaseLost
	}
	if l.lost.Load() {
		return ErrCreativeLeaseLost
	}
	return l.ctx.Err()
}

func (l *creativeProvisionLease) close() {
	l.cancel()
	<-l.done
	// 解锁只使用本次 token，且不继承已经取消的创建/恢复上下文。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = l.lock.Release(ctx)
}

func checkCreativeProvisionLease(ctx context.Context) error {
	if lease, ok := ctx.Value(creativeProvisionLeaseContextKey{}).(*creativeProvisionLease); ok {
		return lease.check()
	}
	return ctx.Err()
}
