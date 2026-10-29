package service

import (
	"context"
	"errors"
	"sync"
)

type asyncImageDeferredCaptureKey struct{}

// AsyncImageDeferredCapture 让异步生图“先交付、后扣费”：生成成功时只登记捕获动作，
// 等图片真正转存后、任务标记完成之前执行捕获；无法交付或无法扣费时执行释放，用户不为未交付的图片付费。
// 捕获与释放各自只在成功后才算完成，失败可以重试，不会因一次数据库故障永久丢失结算机会。
// @project-doc docs/domains/media_tasks.md#async_image_billing
type AsyncImageDeferredCapture struct {
	mu       sync.Mutex
	capture  func(context.Context) error
	release  func(context.Context) error
	captured bool
	released bool
}

// ErrAsyncImageAlreadyCaptured 表示已经扣费，不能再释放。
var ErrAsyncImageAlreadyCaptured = errors.New("async image billing already captured")

// WithAsyncImageDeferredCapture 为一次异步生图执行安装延迟结算容器。
func WithAsyncImageDeferredCapture(ctx context.Context) (context.Context, *AsyncImageDeferredCapture) {
	if ctx == nil {
		ctx = context.Background()
	}
	holder := &AsyncImageDeferredCapture{}
	return context.WithValue(ctx, asyncImageDeferredCaptureKey{}, holder), holder
}

// AsyncImageDeferredCaptureFromContext 读取延迟结算容器；同步请求返回 nil，保持原有即时捕获。
func AsyncImageDeferredCaptureFromContext(ctx context.Context) *AsyncImageDeferredCapture {
	if ctx == nil {
		return nil
	}
	holder, _ := ctx.Value(asyncImageDeferredCaptureKey{}).(*AsyncImageDeferredCapture)
	return holder
}

// Defer 登记交付成功后的捕获与无法交付时的释放；release 可为空（无预占的后结算）。
// 同一执行只接受第一次登记，账号重试不能覆盖已确认产图的结算。
func (d *AsyncImageDeferredCapture) Defer(capture, release func(context.Context) error) bool {
	if d == nil || capture == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.capture != nil || d.captured || d.released {
		return false
	}
	d.capture, d.release = capture, release
	return true
}

// Pending 表示已登记且既未扣费也未释放。
func (d *AsyncImageDeferredCapture) Pending() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.capture != nil && !d.captured && !d.released
}

// Captured 表示已经成功扣费。
func (d *AsyncImageDeferredCapture) Captured() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.captured
}

// Capture 执行一次捕获；成功后幂等返回，失败保持待结算以便重试。已释放时拒绝扣费。
// 持锁执行回调，保证同一预占的捕获与释放互斥，不会并发地既扣费又退款。
func (d *AsyncImageDeferredCapture) Capture(ctx context.Context) error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.capture == nil || d.captured {
		return nil
	}
	if d.released {
		return errors.New("async image billing already released")
	}
	if err := d.capture(ctx); err != nil {
		return err
	}
	d.captured = true
	return nil
}

// Release 执行一次释放；成功后幂等返回，失败保持待结算以便重试。已扣费时拒绝释放。
func (d *AsyncImageDeferredCapture) Release(ctx context.Context) error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.capture == nil || d.released {
		return nil
	}
	if d.captured {
		return ErrAsyncImageAlreadyCaptured
	}
	if d.release != nil {
		if err := d.release(ctx); err != nil {
			return errors.Join(errors.New("release undelivered async image reservation"), err)
		}
	}
	d.released = true
	return nil
}
