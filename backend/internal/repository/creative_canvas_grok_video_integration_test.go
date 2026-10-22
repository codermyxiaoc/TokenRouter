//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 用真实事务检验发送栅栏与原任务绑定，不调用供应商或使用真实余额。
func newCanvasGrokVideoExecution(t *testing.T, f *legacyVideoFixture) (*creativeCanvasRepository, *service.CreativeCanvasExecution) {
	t.Helper()
	ctx := context.Background()
	r := &creativeCanvasRepository{db: integrationDB}
	c := &service.CreativeCanvas{ID: uuid.NewString(), UserID: f.user, Title: "Grok video transaction test",
		Document: json.RawMessage(`{"nodes":[{"id":"video","type":"video"}],"connections":[],"viewport":{"x":0,"y":0,"k":1}}`)}
	require.NoError(t, r.Create(ctx, c))
	e, err := r.CreateExecution(ctx, &service.CreativeCanvasExecution{ID: uuid.NewString(), UserID: f.user,
		CanvasID: c.ID, NodeID: "video", Kind: "video", Status: "queued", IdempotencyKey: uuid.NewString(), Fingerprint: uuid.NewString(),
		Params: service.CreativeCanvasExecutionParams{GroupID: f.group, Model: "grok-imagine-video", Endpoint: "grok"}})
	require.NoError(t, err)
	e.LeaseToken = uuid.NewString()
	_, err = integrationDB.ExecContext(ctx, `UPDATE creative_canvas_executions SET lease_token=$2,lease_until=NOW()+INTERVAL '5 minutes' WHERE id=$1`, e.ID, e.LeaseToken)
	require.NoError(t, err)
	return r, e
}

func TestCreativeGrokVideoRepositoryConcurrentSendFence(t *testing.T) {
	f := newLegacyVideoFixture(t)
	r, e := newCanvasGrokVideoExecution(t, f)
	ctx := context.Background()
	state, err := r.GetGrokVideoExecutionState(ctx, f.user, e.ID)
	require.NoError(t, err)
	require.Empty(t, state.Phase)
	_, err = r.GetGrokVideoExecutionState(ctx, f.user+10000, e.ID)
	require.ErrorIs(t, err, service.ErrCanvasNotFound)
	for _, mutate := range []func(*service.CreativeCanvasExecution){
		func(v *service.CreativeCanvasExecution) { v.LeaseToken = uuid.NewString() },
		func(v *service.CreativeCanvasExecution) { v.UserID++ },
		func(v *service.CreativeCanvasExecution) { v.Params.GroupID++ },
	} {
		bad := *e
		mutate(&bad)
		started, err := r.BeginGrokVideoExecution(ctx, &bad, f.key)
		require.NoError(t, err)
		require.False(t, started)
	}
	// 同租约的并发重复请求只有一个能获得发送资格。
	type outcome struct {
		started bool
		err     error
	}
	results := make(chan outcome, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started, err := r.BeginGrokVideoExecution(ctx, e, f.key)
			results <- outcome{started, err}
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for result := range results {
		require.NoError(t, result.err)
		if result.started {
			winners++
		}
	}
	require.Equal(t, 1, winners)
	stored, err := r.GetExecution(ctx, f.user, e.ID)
	require.NoError(t, err)
	require.Equal(t, "creative_grok_video:"+e.ID, stored.TaskID)
	require.Equal(t, "running", stored.Status)
	require.NoError(t, r.SaveGrokVideoResponse(ctx, f.user, e.ID, 502, true, ""))
	require.NoError(t, r.SaveGrokVideoResponse(ctx, f.user, e.ID, 502, true, ""))
	require.ErrorIs(t, r.SaveGrokVideoResponse(ctx, f.user, e.ID, 400, false, "different"), service.ErrCanvasConflict)
	// 未知受理结果即使换新租约，也绝不能清空栅栏重发。
	e.LeaseToken = uuid.NewString()
	_, err = integrationDB.ExecContext(ctx, `UPDATE creative_canvas_executions SET lease_token=$2,lease_until=NOW()+INTERVAL '5 minutes' WHERE id=$1`, e.ID, e.LeaseToken)
	require.NoError(t, err)
	started, err := r.BeginGrokVideoExecution(ctx, e, f.key)
	require.NoError(t, err)
	require.False(t, started)
}

func TestCreativeGrokVideoRepositoryAtomicBindingAndReplay(t *testing.T) {
	f := newLegacyVideoFixture(t)
	r, e := newCanvasGrokVideoExecution(t, f)
	ctx := context.Background()
	started, err := r.BeginGrokVideoExecution(ctx, e, f.key)
	require.NoError(t, err)
	require.True(t, started)
	task := f.task()
	// 不接受只有内存任务号而没有正式任务凭证的绑定。
	require.ErrorIs(t, r.BindGrokVideoTask(ctx, f.user, e.ID, task), service.ErrCanvasConflict)
	task.CreativeExecutionID = e.ID
	require.NoError(t, f.repo.Save(ctx, task))
	require.Equal(t, e.ID, task.CreativeExecutionID)
	require.NoError(t, f.repo.Save(ctx, task), "同任务重复保存仍须幂等")
	require.NoError(t, r.SaveGrokVideoResponse(ctx, f.user, e.ID, 502, true, "late_error"))
	state, err := r.GetGrokVideoExecutionState(ctx, f.user, e.ID)
	require.NoError(t, err)
	require.Equal(t, "accepted", state.Phase)
	require.Equal(t, task.TaskID, state.TaskID)
	require.Equal(t, task.AccountID, state.AccountID)
	require.Empty(t, state.ErrorCode, "已确认的原任务不能被迟到的网络错误覆盖")
	require.True(t, state.RequestSent)
	// 同一原任务不能归属两个执行，否则两个节点会重复归档及混淆费用。
	_, second := newCanvasGrokVideoExecution(t, f)
	started, err = r.BeginGrokVideoExecution(ctx, second, f.key)
	require.NoError(t, err)
	require.True(t, started)
	duplicate := *task
	duplicate.CreativeExecutionID = second.ID
	require.Error(t, f.repo.Save(ctx, &duplicate))
	state, err = r.GetGrokVideoExecutionState(ctx, f.user, second.ID)
	require.NoError(t, err)
	require.Empty(t, state.TaskID)
	// 首个执行也不允许被另一个已存在的正式任务替换。
	other := f.task()
	require.NoError(t, f.repo.Save(ctx, other))
	require.ErrorIs(t, r.BindGrokVideoTask(ctx, f.user, e.ID, other), service.ErrCanvasConflict)
}

func TestCreativeGrokVideoRepositoryBindingRollbackAndLateAcceptance(t *testing.T) {
	f := newLegacyVideoFixture(t)
	r, e := newCanvasGrokVideoExecution(t, f)
	ctx := context.Background()
	started, err := r.BeginGrokVideoExecution(ctx, e, f.key)
	require.NoError(t, err)
	require.True(t, started)
	wrong := f.task()
	wrong.GroupID++
	wrong.CreativeExecutionID = e.ID
	require.Error(t, f.repo.Save(ctx, wrong))
	_, err = f.repo.Get(ctx, wrong.Source, wrong.TaskID, f.user, f.key)
	require.ErrorIs(t, err, service.ErrLegacyVideoTaskNotFound, "画布绑定失败必须一起回滚原任务插入")
	state, err := r.GetGrokVideoExecutionState(ctx, f.user, e.ID)
	require.NoError(t, err)
	require.Equal(t, "provider_inflight", state.Phase)
	// 原发送者租约过期后，迟到的真实受理仍可补齐绑定，后续 worker 只查询该任务。
	_, err = integrationDB.ExecContext(ctx, `UPDATE creative_canvas_executions SET lease_until=NOW()-INTERVAL '1 minute' WHERE id=$1`, e.ID)
	require.NoError(t, err)
	require.NoError(t, r.SaveGrokVideoResponse(ctx, f.user, e.ID, 504, true, ""))
	task := f.task()
	task.CreativeExecutionID = e.ID
	require.NoError(t, f.repo.Save(ctx, task))
	state, err = r.GetGrokVideoExecutionState(ctx, f.user, e.ID)
	require.NoError(t, err)
	require.Equal(t, "accepted", state.Phase)
	require.Equal(t, task.TaskID, state.TaskID)
	require.ErrorIs(t, r.SaveGrokVideoResponse(ctx, f.user+1, e.ID, 200, true, ""), service.ErrCanvasConflict)
}
