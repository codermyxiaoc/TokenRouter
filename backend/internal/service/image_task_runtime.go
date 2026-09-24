package service

import (
	"context"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	imageTaskLeaseDuration     = 90 * time.Second
	imageTaskHeartbeatInterval = 15 * time.Second
)

// imageTaskRuntime 只维护执行租约与结果补偿，不保存请求正文，也不重新发送生图请求。
type imageTaskRuntime struct {
	executionID string
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	started     bool
	stopped     bool
	wg          sync.WaitGroup
}

func newImageTaskRuntime() *imageTaskRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	return &imageTaskRuntime{executionID: uuid.NewString(), ctx: ctx, cancel: cancel}
}

// Start 在应用装配后启动补偿，数据库关闭前必须调用 Stop。
func (s *ImageTaskService) Start() {
	store, ok := s.store.(ImageTaskDurableStore)
	if !ok || s.runtime == nil {
		return
	}
	r := s.runtime
	r.mu.Lock()
	if r.started || r.stopped {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(imageTaskHeartbeatInterval)
		defer ticker.Stop()
		for {
			ctx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
			if err := store.Maintain(ctx, time.Now().UTC()); err != nil && r.ctx.Err() == nil {
				logger.L().Warn("image_task.reconcile_failed", zap.Error(err))
			}
			cancel()
			select {
			case <-r.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// KeepAlive 为已受理任务续租，返回的停止函数应在上传和终态保存结束后调用。
func (s *ImageTaskService) KeepAlive(id string) func() {
	store, ok := s.store.(ImageTaskDurableStore)
	if !ok || s.runtime == nil {
		return func() {}
	}
	r := s.runtime
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return func() {}
	}
	ctx, cancel := context.WithCancel(r.ctx)
	r.wg.Add(1)
	r.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer r.wg.Done()
		defer close(done)
		ticker := time.NewTicker(imageTaskHeartbeatInterval)
		defer ticker.Stop()
		for {
			beatCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			err := store.Heartbeat(beatCtx, id, r.executionID, time.Now().Add(imageTaskLeaseDuration).Unix())
			stop()
			if err != nil && ctx.Err() == nil {
				logger.L().Warn("image_task.heartbeat_failed", zap.String("task_id", id), zap.Error(err))
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

// Stop 可重复调用，并在底层存储关闭前等待续租和补偿循环退出。
func (s *ImageTaskService) Stop() {
	if s == nil || s.runtime == nil {
		return
	}
	r := s.runtime
	r.mu.Lock()
	r.stopped = true
	r.cancel()
	r.mu.Unlock()
	r.wg.Wait()
}
