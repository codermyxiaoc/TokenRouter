package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

// CreateExecutionBatch 在用户锁下原子检查幂等、所属节点和整批存储预留，不允许半批入库。
// 单任务也使用此路径；预留只限制素材磁盘容量，不触碰原图片和视频资金账本。
func (r *creativeCanvasRepository) CreateExecutionBatch(ctx context.Context, items []*service.CreativeCanvasExecution) ([]*service.CreativeCanvasExecution, error) {
	if len(items) < 1 || len(items) > 8 || items[0] == nil {
		return nil, service.ErrCanvasInvalid
	}
	first := items[0]
	keys := make(map[string]bool, len(items))
	for _, e := range items {
		if e == nil || e.UserID != first.UserID || e.CanvasID != first.CanvasID || e.NodeID != first.NodeID || e.Kind != first.Kind || keys[e.IdempotencyKey] {
			return nil, service.ErrCanvasInvalid
		}
		keys[e.IdempotencyKey] = true
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, first.UserID); err != nil {
		return nil, err
	}
	if first.Params.BatchID != "" {
		// 映射在两个并发请求之间变化时，已受理整批是唯一权威，不能只恢复当前重新推导的子集。
		rows, lookupErr := tx.QueryContext(ctx, canvasExecutionBatchQuery, first.UserID, first.Params.BatchID)
		if lookupErr != nil {
			return nil, lookupErr
		}
		accepted, lookupErr := scanCanvasExecutionBatch(rows)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if len(accepted) > 0 {
			for i, previous := range accepted {
				if previous.Fingerprint != first.Fingerprint || previous.Params.BatchID != first.Params.BatchID || previous.Params.BatchSize != len(accepted) || previous.Params.BatchIndex != i {
					return nil, service.ErrCanvasConflict
				}
			}
			return accepted, nil
		}
	}
	old := make([]*service.CreativeCanvasExecution, 0, len(items))
	for _, e := range items {
		previous, lookupErr := scanCanvasExecution(tx.QueryRowContext(ctx, `SELECT `+canvasExecutionColumns+` FROM creative_canvas_executions WHERE user_id=$1 AND idempotency_key=$2`, e.UserID, e.IdempotencyKey))
		if lookupErr == nil {
			if previous.Fingerprint != e.Fingerprint {
				return nil, service.ErrCanvasConflict
			}
			old = append(old, previous)
		} else if !errors.Is(lookupErr, service.ErrCanvasNotFound) {
			return nil, lookupErr
		}
	}
	if len(old) > 0 {
		// 批次按 batch_id 没有快照却命中子键属于不完整数据，不能静默沿用或补生成。
		if len(old) != len(items) || first.Params.BatchID != "" {
			return nil, service.ErrCanvasConflict
		}
		return old, nil
	}
	// 共享行锁阻止校验后节点被并发删除；事务提交后删除画布仍不能取消或重发已受理任务。
	var rawDocument []byte
	if err = tx.QueryRowContext(ctx, `SELECT document FROM creative_canvases WHERE id=$1 AND user_id=$2 AND deleted_at IS NULL FOR SHARE`, first.CanvasID, first.UserID).Scan(&rawDocument); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrCanvasNotFound
		}
		return nil, err
	}
	var document struct {
		Nodes []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"nodes"`
	}
	if json.Unmarshal(rawDocument, &document) != nil {
		return nil, service.ErrCanvasInvalid
	}
	owned := false
	for _, node := range document.Nodes {
		if node.ID == first.NodeID && (node.Type == first.Kind || node.Type == "config") {
			owned = true
		}
	}
	if !owned {
		return nil, service.ErrCanvasInvalid
	}
	var stored, inFlight int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT SUM(byte_size) FROM creative_canvas_assets WHERE user_id=$1 AND status IN ('ready','staging') AND expires_at>NOW()),0),COALESCE((SELECT SUM(CASE WHEN kind='video' THEN 536870912 ELSE 134217728 END) FROM creative_canvas_executions WHERE user_id=$1 AND status IN ('queued','running')),0)`, first.UserID).Scan(&stored, &inFlight); err != nil {
		return nil, err
	}
	reserved := int64(128 << 20)
	if first.Kind == "video" {
		reserved = 512 << 20
	}
	// 保持原容量预算，只有等待在途任务释放预留即可恢复时才报告临时执行上限。
	requested := reserved * int64(len(items))
	if stored+requested > service.CreativeCanvasUserQuota {
		return nil, service.ErrCanvasQuota
	}
	if stored+inFlight+requested > service.CreativeCanvasUserQuota {
		return nil, service.ErrCanvasExecutionLimit
	}
	out := make([]*service.CreativeCanvasExecution, 0, len(items))
	for _, e := range items {
		raw, marshalErr := json.Marshal(e.Params)
		if marshalErr != nil {
			return nil, marshalErr
		}
		created, insertErr := scanCanvasExecution(tx.QueryRowContext(ctx, `INSERT INTO creative_canvas_executions(id,user_id,canvas_id,node_id,kind,params,idempotency_key,fingerprint) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+canvasExecutionColumns, e.ID, e.UserID, e.CanvasID, e.NodeID, e.Kind, string(raw), e.IdempotencyKey, e.Fingerprint))
		if insertErr != nil {
			return nil, insertErr
		}
		out = append(out, created)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

const canvasExecutionBatchQuery = `SELECT ` + canvasExecutionColumns + ` FROM creative_canvas_executions WHERE user_id=$1 AND params->>'batch_id'=$2 ORDER BY COALESCE((params->>'batch_index')::int,0),id`

// GetExecutionBatch 只返回当前用户的持久子任务，按服务端生成的索引恢复顺序。
func (r *creativeCanvasRepository) GetExecutionBatch(ctx context.Context, u int64, batchID string) ([]*service.CreativeCanvasExecution, error) {
	rows, err := r.db.QueryContext(ctx, canvasExecutionBatchQuery, u, batchID)
	if err != nil {
		return nil, err
	}
	return scanCanvasExecutionBatch(rows)
}

func scanCanvasExecutionBatch(rows *sql.Rows) ([]*service.CreativeCanvasExecution, error) {
	defer rows.Close()
	items := make([]*service.CreativeCanvasExecution, 0)
	for rows.Next() {
		e, scanErr := scanCanvasExecution(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, e)
	}
	return items, rows.Err()
}
