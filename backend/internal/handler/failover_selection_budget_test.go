package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 冷却或共享故障集合导致没有可选账号时，503 兜底不能无界重复退避。
func TestSelectionExhausted503RecoveryIsBounded(t *testing.T) {
	fs := NewFailoverState(3, false)
	fs.SwitchCount = 1
	fs.LastFailoverErr = &service.UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable}
	fs.FailedAccountIDs[7] = struct{}{}
	require.Equal(t, FailoverContinue, fs.HandleSelectionExhausted(context.Background()))
	require.Empty(t, fs.FailedAccountIDs)
	require.Equal(t, 1, fs.SwitchCount)

	// 第二次选号仍失败时立即结束，不重置同号重试次数或再次清除失败集合。
	fs.FailedAccountIDs[8] = struct{}{}
	fs.SameAccountRetryCount[7] = 2
	started := time.Now()
	require.Equal(t, FailoverExhausted, fs.HandleSelectionExhausted(context.Background()))
	require.Less(t, time.Since(started), 100*time.Millisecond)
	require.Contains(t, fs.FailedAccountIDs, int64(8))
	require.Equal(t, 2, fs.SameAccountRetryCount[7])
}

// 模型404在单号/多号耗尽时均直接终结，不能继承503重探或绕过切换上限。
func TestSelectionExhaustedModel404NeverReenters(t *testing.T) {
	for _, maximum := range []int{0, 1, 3} {
		fs := NewFailoverState(maximum, false)
		fs.LastFailoverErr = &service.UpstreamFailoverError{StatusCode: http.StatusNotFound}
		fs.FailedAccountIDs[7] = struct{}{}
		for attempt := 0; attempt < 3; attempt++ {
			require.Equal(t, FailoverExhausted, fs.HandleSelectionExhausted(context.Background()))
		}
		require.Contains(t, fs.FailedAccountIDs, int64(7))
	}
	fs := NewFailoverState(1, false)
	fs.SwitchCount = 2
	fs.LastFailoverErr = &service.UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable}
	require.Equal(t, FailoverExhausted, fs.HandleSelectionExhausted(context.Background()))
	require.False(t, fs.selectionRecoveryUsed)
}
