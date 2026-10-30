package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 幂等重放只查询已受理任务：同键同正文返回原任务且不再预扣或调用上游；正文不同返回冲突；没有记录时交回创建流程。
func TestVideoTaskReplayIdempotentReturnsExistingTask(t *testing.T) {
	s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideoToken)
	req := VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","resolution":"720p"}`), IdempotencyKey: "replay-one"}
	_, found, err := s.ReplayIdempotent(context.Background(), key, req)
	require.NoError(t, err)
	require.False(t, found)
	created, err := s.Submit(context.Background(), key, req)
	require.NoError(t, err)
	replay, found, err := s.ReplayIdempotent(context.Background(), key, req)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, created.LocalTaskID, replay.LocalTaskID)
	require.Equal(t, 1, b.reserves)
	require.Equal(t, 1, u.submitted)
	other := req
	other.Body = []byte(`{"model":"video-model","resolution":"1080p"}`)
	_, _, err = s.ReplayIdempotent(context.Background(), key, other)
	require.ErrorIs(t, err, ErrVideoTaskConflict)
}
