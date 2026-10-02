package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 写期限替身既记录真实调用，也允许阻塞调用以检查租约释放时序。
type opsDeadlineResponseWriter struct {
	gin.ResponseWriter
	deadlines []time.Time
	err       error
	started   chan struct{}
	release   chan struct{}
}

func (w *opsDeadlineResponseWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	if w.started != nil {
		close(w.started)
	}
	if w.release != nil {
		<-w.release
	}
	return w.err
}

func TestOpsCaptureWriter_WriteDeadlineForwardsAndRejectsStaleLease(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pool := &deterministicOpsCaptureWriterStatePool{}
	firstContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	first := &opsDeadlineResponseWriter{ResponseWriter: firstContext.Writer}
	stale := acquireOpsCaptureWriterFromPool(pool, first)
	deadline := time.Now().Add(time.Minute)

	// 调用方经 ResponseController 设置与清除期限，底层错误也必须原样返回。
	controller := http.NewResponseController(stale)
	require.NoError(t, controller.SetWriteDeadline(deadline))
	require.NoError(t, controller.SetWriteDeadline(time.Time{}))
	wantErr := errors.New("deadline unavailable")
	first.err = wantErr
	require.ErrorIs(t, controller.SetWriteDeadline(deadline), wantErr)
	require.Equal(t, []time.Time{deadline, {}, deadline}, first.deadlines)
	releaseOpsCaptureWriter(stale)

	secondContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	second := &opsDeadlineResponseWriter{ResponseWriter: secondContext.Writer}
	current := acquireOpsCaptureWriterFromPool(pool, second)
	defer releaseOpsCaptureWriter(current)
	require.Same(t, stale.state, current.state)

	// 确认旧句柄不能透过复用后的状态触达下一请求，当前句柄仍可正常设置。
	require.Error(t, controller.SetWriteDeadline(deadline))
	require.Empty(t, second.deadlines)
	require.NoError(t, http.NewResponseController(current).SetWriteDeadline(deadline))
	require.Equal(t, []time.Time{deadline}, second.deadlines)
	require.Error(t, (&opsCaptureWriter{}).SetWriteDeadline(deadline))
}

func TestOpsCaptureWriter_WriteDeadlineKeepsLeaseUntilDelegatedCallReturns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pool := &deterministicOpsCaptureWriterStatePool{}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	inner := &opsDeadlineResponseWriter{
		ResponseWriter: ctx.Writer,
		started:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	w := acquireOpsCaptureWriterFromPool(pool, inner)
	var unblockOnce sync.Once
	unblock := func() { unblockOnce.Do(func() { close(inner.release) }) }
	defer unblock()

	callDone := make(chan error, 1)
	go func() { callDone <- w.SetWriteDeadline(time.Now().Add(time.Minute)) }()
	select {
	case <-inner.started:
	case <-time.After(time.Second):
		t.Fatal("写期限未转发到底层 writer")
	}
	if !w.state.mu.TryLock() {
		t.Fatal("转发写期限期间不应持有状态锁")
	}
	w.state.mu.Unlock()

	releaseDone := make(chan struct{})
	go func() {
		releaseOpsCaptureWriter(w)
		close(releaseDone)
	}()
	select {
	case <-releaseDone:
		t.Fatal("底层写期限调用尚未返回时不应释放租约")
	case <-time.After(20 * time.Millisecond):
	}

	// 已委派的调用完成后才允许状态入池，避免下一请求抢用仍在操作的状态。
	unblock()
	select {
	case err := <-callDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("底层解除阻塞后写期限调用未完成")
	}
	select {
	case <-releaseDone:
	case <-time.After(time.Second):
		t.Fatal("写期限调用完成后租约未释放")
	}
	require.Len(t, pool.states, 1)
}
