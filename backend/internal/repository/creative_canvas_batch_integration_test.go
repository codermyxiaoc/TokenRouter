//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 使用集成测试隔离 PostgreSQL，验证同批并发重放、跨用户隔离与事务失败无半批残留。
func TestCreativeCanvasBatchConcurrentIntegration(t *testing.T) {
	ctx := context.Background()
	u := mustCreateUser(t, integrationEntClient, &service.User{})
	other := mustCreateUser(t, integrationEntClient, &service.User{})
	r := &creativeCanvasRepository{db: integrationDB}
	c := &service.CreativeCanvas{ID: uuid.NewString(), UserID: u.ID, Title: "批次回归", Document: json.RawMessage(`{"nodes":[{"id":"image","type":"image"}],"connections":[],"viewport":{"x":0,"y":0,"k":1}}`)}
	require.NoError(t, r.Create(ctx, c))
	batchID := uuid.NewString()
	makeItems := func(key string, n int) []*service.CreativeCanvasExecution {
		items := make([]*service.CreativeCanvasExecution, 0, n)
		for i := 0; i < n; i++ {
			items = append(items, &service.CreativeCanvasExecution{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("%d:%s:%d", u.ID, key, i))).String(), UserID: u.ID, CanvasID: c.ID, NodeID: "image", Kind: "image", IdempotencyKey: fmt.Sprintf("batch:%s:%d", key, i), Fingerprint: fmt.Sprintf("same-%d", n), Status: "queued", Params: service.CreativeCanvasExecutionParams{BatchID: key, BatchIndex: i, BatchSize: n, BatchOutputCount: n}})
		}
		return items
	}
	start := make(chan struct{})
	errs := make(chan error, 48)
	ids := make(chan string, 48)
	var wg sync.WaitGroup
	for i := 0; i < 48; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out, err := r.CreateExecutionBatch(ctx, makeItems(batchID, 3))
			errs <- err
			if err == nil {
				ids <- out[0].ID
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	close(ids)
	for err := range errs {
		require.NoError(t, err)
	}
	expected := makeItems(batchID, 3)[0].ID
	for id := range ids {
		require.Equal(t, expected, id)
	}
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM creative_canvas_executions WHERE user_id=$1`, u.ID).Scan(&count))
	require.Equal(t, 3, count)
	foreign, err := r.GetExecutionBatch(ctx, other.ID, batchID)
	require.NoError(t, err)
	require.Empty(t, foreign)
	restored, err := r.GetExecutionBatch(ctx, u.ID, batchID)
	require.NoError(t, err)
	require.Len(t, restored, 3)
	for i, e := range restored {
		require.Equal(t, i, e.Params.BatchIndex)
	}
	_, err = r.CreateExecutionBatch(ctx, makeItems(batchID, 2))
	require.ErrorIs(t, err, service.ErrCanvasConflict)
	// 第二次 INSERT 主键冲突须回滚第一行；测试数据库也不得留下待执行任务。
	badKey := uuid.NewString()
	bad := makeItems(badKey, 2)
	bad[1].ID = bad[0].ID
	_, err = r.CreateExecutionBatch(ctx, bad)
	require.Error(t, err)
	failed, err := r.GetExecutionBatch(ctx, u.ID, badKey)
	require.NoError(t, err)
	require.Empty(t, failed)
	// 全批容量需一起核算：已有三个预留，再追加八个成功；再追加八个应全批拒绝。
	_, err = r.CreateExecutionBatch(ctx, makeItems(uuid.NewString(), 8))
	require.NoError(t, err)
	quotaKey := uuid.NewString()
	_, err = r.CreateExecutionBatch(ctx, makeItems(quotaKey, 8))
	require.ErrorIs(t, err, service.ErrCanvasExecutionLimit)
	denied, err := r.GetExecutionBatch(ctx, u.ID, quotaKey)
	require.NoError(t, err)
	require.Empty(t, denied)
}

// 真实事务验证执行预留不足与素材占满分别报错，且并发拒绝不能留下执行或碰到资金。
func TestCreativeCanvasExecutionAdmissionDistinguishesInFlightAndStorageIntegration(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	u := mustCreateUser(t, client, &service.User{Balance: 10})
	other := mustCreateUser(t, client, &service.User{Balance: 10})
	r := &creativeCanvasRepository{db: integrationDB}
	makeCanvas := func(userID int64) *service.CreativeCanvas {
		c := &service.CreativeCanvas{ID: uuid.NewString(), UserID: userID, Title: "执行准入回归", Document: json.RawMessage(`{"nodes":[{"id":"video","type":"video"}],"connections":[],"viewport":{"x":0,"y":0,"k":1}}`)}
		require.NoError(t, r.Create(ctx, c))
		return c
	}
	c, otherCanvas := makeCanvas(u.ID), makeCanvas(other.ID)
	makeExecution := func(canvas *service.CreativeCanvas) *service.CreativeCanvasExecution {
		id := uuid.NewString()
		return &service.CreativeCanvasExecution{ID: id, UserID: canvas.UserID, CanvasID: canvas.ID, NodeID: "video", Kind: "video", IdempotencyKey: id, Fingerprint: "same-video-params", Status: "queued"}
	}
	previous := makeExecution(c)
	_, err := r.CreateExecution(ctx, previous)
	require.NoError(t, err)
	start := make(chan struct{})
	results := make(chan error, 12)
	var wg sync.WaitGroup
	for range 12 {
		item := makeExecution(c)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, createErr := r.CreateExecution(ctx, item)
			results <- createErr
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	accepted, rejected := 0, 0
	for createErr := range results {
		if createErr == nil {
			accepted++
			continue
		}
		rejected++
		require.ErrorIs(t, createErr, service.ErrCanvasExecutionLimit)
		require.Equal(t, http.StatusTooManyRequests, infraerrors.Code(createErr))
	}
	require.Equal(t, 3, accepted)
	require.Equal(t, 9, rejected)
	_, count, err := r.ListExecutions(ctx, u.ID, c.ID, 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 4, count)
	// 已受理任务在满额时仍允许幂等恢复；其他用户有独立的素材预算。
	replay, err := r.CreateExecution(ctx, previous)
	require.NoError(t, err)
	require.Equal(t, previous.ID, replay.ID)
	_, err = r.CreateExecution(ctx, makeExecution(otherCanvas))
	require.NoError(t, err)
	var balance, frozen float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance,frozen_balance FROM users WHERE id=$1`, u.ID).Scan(&balance, &frozen))
	require.Equal(t, float64(10), balance)
	require.Zero(t, frozen)
	// 原任务进入终态后预算自然恢复，无需清理素材或提高原有配额。
	_, err = integrationDB.ExecContext(ctx, `UPDATE creative_canvas_executions SET status='failed' WHERE user_id=$1`, u.ID)
	require.NoError(t, err)
	next := makeExecution(c)
	_, err = r.CreateExecution(ctx, next)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE creative_canvas_executions SET status='failed' WHERE id=$1`, next.ID)
	require.NoError(t, err)
	// 这里只保存测试用素材索引，不写真实文件；实际存储占满必须继续返回413。
	_, err = r.ReserveAsset(ctx, &service.CreativeCanvasAsset{ID: uuid.NewString(), UserID: u.ID, CanvasID: c.ID, MediaType: "video", MIMEType: "video/mp4", ByteSize: service.CreativeCanvasUserQuota, ExpiresAt: time.Now().Add(time.Hour)}, service.CreativeCanvasUserQuota)
	require.NoError(t, err)
	_, err = r.CreateExecution(ctx, makeExecution(c))
	require.ErrorIs(t, err, service.ErrCanvasQuota)
	require.Equal(t, http.StatusRequestEntityTooLarge, infraerrors.Code(err))
}
