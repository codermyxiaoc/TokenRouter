//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// 释放生成容量与释放资金都会发生：上游过期即用户拿不到成品，失败并只释放一次，不重新生成也不扣费。
func TestVideoRecoveryExpiredRetainsLedgerWithoutRepostingOrRefund(t *testing.T) {
	svc, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideoToken)
	response, err := svc.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	task, err := svc.repo.Get(context.Background(), response.LocalTaskID)
	require.NoError(t, err)
	task.Status = "expired"
	for i := 0; i < 3; i++ {
		svc.advance(context.Background(), task)
	}
	stored, err := svc.repo.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", stored.Status)
	require.Equal(t, "released", stored.BillingStatus)
	require.Equal(t, 1, upstream.submitted)
	require.Zero(t, upstream.polls)
	require.Zero(t, billing.captures)
	require.Equal(t, 1, billing.releases)
	require.Empty(t, logs.logs)
}

// 日志故障窗口中的完成任务不能被迟到取消退款，其他 Key 也不能驱动该任务。
func TestVideoRecoveryUsageFailureRejectsLateAndForeignCancellation(t *testing.T) {
	svc, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideoToken)
	response, err := svc.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	tokens := int64(200_000)
	upstream.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", VideoURL: "https://cdn.example.test/video.mp4", Metadata: VideoRequestMetadata{Tokens: &tokens}, Body: []byte(`{"status":"completed"}`)}
	logs.err = errors.New("usage unavailable")
	task, err := svc.repo.Get(context.Background(), response.LocalTaskID)
	require.NoError(t, err)
	svc.advance(context.Background(), task)
	require.Equal(t, "settled", task.BillingStatus)
	require.False(t, task.EffectsDone)
	_, err = svc.Cancel(context.Background(), key, task.ID, "compat")
	require.ErrorIs(t, err, ErrVideoTaskConflict)
	foreign := *key
	foreign.ID++
	_, err = svc.Cancel(context.Background(), &foreign, task.ID, "compat")
	require.ErrorIs(t, err, ErrVideoTaskNotFound)
	_, err = svc.Query(context.Background(), &foreign, task.ID, "compat")
	require.ErrorIs(t, err, ErrVideoTaskNotFound)
	require.Zero(t, upstream.cancels)
	require.Zero(t, billing.releases)
	logs.err = nil
	svc.advance(context.Background(), task)
	require.True(t, task.EffectsDone)
	require.Equal(t, 1, billing.captures)
	require.Equal(t, 1, upstream.submitted)
	require.Len(t, logs.logs, 1)
}
