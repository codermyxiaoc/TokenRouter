package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
)

// 画布所有读写均由数据库同时核对用户，软删除不移除异步任务及账本。
type creativeCanvasRepository struct{ db *sql.DB }

func NewCreativeCanvasRepository(db *sql.DB) service.CreativeCanvasRepository {
	return &creativeCanvasRepository{db: db}
}

type canvasScanner interface{ Scan(...any) error }

func scanCanvas(row canvasScanner) (*service.CreativeCanvas, error) {
	v := new(service.CreativeCanvas)
	err := row.Scan(&v.ID, &v.UserID, &v.Title, &v.Document, &v.Revision, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrCanvasNotFound
	}
	return v, err
}

const canvasColumns = `id,user_id,title,document,revision,created_at,updated_at`

func (r *creativeCanvasRepository) Create(ctx context.Context, v *service.CreativeCanvas) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, v.UserID); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM creative_canvases WHERE user_id=$1 AND deleted_at IS NULL`, v.UserID).Scan(&count); err != nil {
		return err
	}
	if count >= 100 {
		return service.ErrCanvasQuota
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO creative_canvases(id,user_id,title,document) VALUES($1,$2,$3,$4)`, v.ID, v.UserID, v.Title, string(v.Document))
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (r *creativeCanvasRepository) Get(ctx context.Context, u int64, id string) (*service.CreativeCanvas, error) {
	return scanCanvas(r.db.QueryRowContext(ctx, `SELECT `+canvasColumns+` FROM creative_canvases WHERE id=$1 AND user_id=$2 AND deleted_at IS NULL`, id, u))
}
func (r *creativeCanvasRepository) List(ctx context.Context, u int64, limit, offset int) ([]*service.CreativeCanvas, int64, error) {
	var total int64
	err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM creative_canvases WHERE user_id=$1 AND deleted_at IS NULL`, u).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+canvasColumns+` FROM creative_canvases WHERE user_id=$1 AND deleted_at IS NULL ORDER BY updated_at DESC,id LIMIT $2 OFFSET $3`, u, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]*service.CreativeCanvas, 0)
	for rows.Next() {
		v, e := scanCanvas(rows)
		if e != nil {
			return nil, 0, e
		}
		items = append(items, v)
	}
	return items, total, rows.Err()
}
func (r *creativeCanvasRepository) Update(ctx context.Context, v *service.CreativeCanvas, revision int64) error {
	res, err := r.db.ExecContext(ctx, `UPDATE creative_canvases SET title=$1,document=$2,revision=revision+1,updated_at=NOW() WHERE id=$3 AND user_id=$4 AND revision=$5 AND deleted_at IS NULL`, v.Title, string(v.Document), v.ID, v.UserID, revision)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		if _, e := r.Get(ctx, v.UserID, v.ID); e != nil {
			return e
		}
		return service.ErrCanvasConflict
	}
	return nil
}
func (r *creativeCanvasRepository) Delete(ctx context.Context, u int64, id string, revision int64) error {
	res, err := r.db.ExecContext(ctx, `UPDATE creative_canvases SET deleted_at=NOW(),updated_at=NOW(),revision=revision+1 WHERE id=$1 AND user_id=$2 AND revision=$3 AND deleted_at IS NULL`, id, u, revision)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		if _, e := r.Get(ctx, u, id); e != nil {
			return e
		}
		return service.ErrCanvasConflict
	}
	return nil
}

const canvasAssetColumns = `id,user_id,canvas_id,COALESCE(execution_id::text,''),output_index,media_type,mime_type,byte_size,sha256,status,created_at,expires_at`

func scanCanvasAsset(row canvasScanner) (*service.CreativeCanvasAsset, error) {
	v := new(service.CreativeCanvasAsset)
	err := row.Scan(&v.ID, &v.UserID, &v.CanvasID, &v.ExecutionID, &v.OutputIndex, &v.MediaType, &v.MIMEType, &v.ByteSize, &v.SHA256, &v.Status, &v.CreatedAt, &v.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrCanvasNotFound
	}
	return v, err
}

// 存储预留按用户事务锁串行化，并计入正在生成的最大成品预算，避免并发写满磁盘。
func (r *creativeCanvasRepository) ReserveAsset(ctx context.Context, v *service.CreativeCanvasAsset, quota int64) (*service.CreativeCanvasAsset, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, v.UserID); err != nil {
		return nil, err
	}
	old, e := scanCanvasAsset(tx.QueryRowContext(ctx, `SELECT `+canvasAssetColumns+` FROM creative_canvas_assets WHERE id=$1 AND user_id=$2`, v.ID, v.UserID))
	if e == nil {
		if old.CanvasID != v.CanvasID || old.ExecutionID != v.ExecutionID || old.OutputIndex != v.OutputIndex {
			return nil, service.ErrCanvasConflict
		}
		return old, nil
	}
	if !errors.Is(e, service.ErrCanvasNotFound) {
		return nil, e
	}
	var owned bool
	if v.ExecutionID == "" {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM creative_canvases WHERE id=$1 AND user_id=$2 AND deleted_at IS NULL)`, v.CanvasID, v.UserID).Scan(&owned)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM creative_canvas_executions WHERE id=$1 AND user_id=$2 AND canvas_id=$3)`, v.ExecutionID, v.UserID, v.CanvasID).Scan(&owned)
	}
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, service.ErrCanvasNotFound
	}
	var used int64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT SUM(byte_size) FROM creative_canvas_assets WHERE user_id=$1 AND status IN ('ready','staging') AND expires_at>NOW()),0)+COALESCE((SELECT SUM(CASE WHEN kind='video' THEN 536870912 ELSE 134217728 END) FROM creative_canvas_executions WHERE user_id=$1 AND status IN ('queued','running') AND id::text<>$2),0)`, v.UserID, v.ExecutionID).Scan(&used)
	if err != nil {
		return nil, err
	}
	if v.ByteSize > quota-used {
		return nil, service.ErrCanvasQuota
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO creative_canvas_assets(id,user_id,canvas_id,execution_id,output_index,media_type,mime_type,byte_size,status,expires_at) VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,$7,$8,'staging',$9)`, v.ID, v.UserID, v.CanvasID, v.ExecutionID, v.OutputIndex, v.MediaType, v.MIMEType, v.ByteSize, v.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return r.GetAsset(ctx, v.UserID, v.ID)
}
func (r *creativeCanvasRepository) ReadyAsset(ctx context.Context, u int64, id string, size int64, hash string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE creative_canvas_assets SET status='ready',byte_size=$3,sha256=$4 WHERE id=$1 AND user_id=$2 AND status='staging' AND byte_size>=$3 AND expires_at>NOW()`, id, u, size, hash)
	if err != nil {
		return err
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		old, e := r.GetAsset(ctx, u, id)
		if e == nil && old.Status == "ready" && old.ByteSize == size && old.SHA256 == hash {
			return nil
		}
		return service.ErrCanvasConflict
	}
	return nil
}
func (r *creativeCanvasRepository) GetAsset(ctx context.Context, u int64, id string) (*service.CreativeCanvasAsset, error) {
	return scanCanvasAsset(r.db.QueryRowContext(ctx, `SELECT `+canvasAssetColumns+` FROM creative_canvas_assets WHERE id=$1 AND user_id=$2`, id, u))
}
func (r *creativeCanvasRepository) ListAssets(ctx context.Context, u int64, canvasID, executionID string, limit, offset int) ([]*service.CreativeCanvasAsset, int64, error) {
	where := `user_id=$1 AND ($2='' OR canvas_id::text=$2) AND ($3='' OR execution_id::text=$3) AND status<>'staging'`
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM creative_canvas_assets WHERE `+where, u, canvasID, executionID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+canvasAssetColumns+` FROM creative_canvas_assets WHERE `+where+` ORDER BY created_at DESC,output_index LIMIT $4 OFFSET $5`, u, canvasID, executionID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]*service.CreativeCanvasAsset, 0)
	for rows.Next() {
		v, e := scanCanvasAsset(rows)
		if e != nil {
			return nil, 0, e
		}
		items = append(items, v)
	}
	return items, total, rows.Err()
}
func (r *creativeCanvasRepository) DeleteAsset(ctx context.Context, u int64, id string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE creative_canvas_assets SET status='deleted',expires_at=LEAST(expires_at,NOW()) WHERE id=$1 AND user_id=$2 AND status<>'purged'`, id, u)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return service.ErrCanvasNotFound
	}
	return err
}
func (r *creativeCanvasRepository) ExpiredAssets(ctx context.Context, now time.Time, limit int) ([]*service.CreativeCanvasAsset, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+canvasAssetColumns+` FROM creative_canvas_assets WHERE expires_at<=$1 AND status<>'purged' ORDER BY expires_at LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*service.CreativeCanvasAsset, 0)
	for rows.Next() {
		v, e := scanCanvasAsset(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (r *creativeCanvasRepository) MarkAssetPurged(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE creative_canvas_assets SET status='purged' WHERE id=$1 AND expires_at<=NOW()`, id)
	return err
}

const canvasExecutionColumns = `id,user_id,canvas_id,node_id,kind,params,idempotency_key,fingerprint,task_id,status,error_code,error_message,COALESCE(lease_token::text,''),created_at,updated_at,CASE WHEN status='succeeded' THEN output_text ELSE '' END`

func scanCanvasExecution(row canvasScanner) (*service.CreativeCanvasExecution, error) {
	v := new(service.CreativeCanvasExecution)
	var raw []byte
	err := row.Scan(&v.ID, &v.UserID, &v.CanvasID, &v.NodeID, &v.Kind, &raw, &v.IdempotencyKey, &v.Fingerprint, &v.TaskID, &v.Status, &v.ErrorCode, &v.ErrorMessage, &v.LeaseToken, &v.CreatedAt, &v.UpdatedAt, &v.OutputText)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrCanvasNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &v.Params); err != nil {
		return nil, err
	}
	v.Assets = make([]*service.CreativeCanvasAsset, 0)
	return v, nil
}
func (r *creativeCanvasRepository) CreateExecution(ctx context.Context, v *service.CreativeCanvasExecution) (*service.CreativeCanvasExecution, error) {
	items, err := r.CreateExecutionBatch(ctx, []*service.CreativeCanvasExecution{v})
	if err != nil {
		return nil, err
	}
	return items[0], nil
}
func (r *creativeCanvasRepository) GetExecution(ctx context.Context, u int64, id string) (*service.CreativeCanvasExecution, error) {
	return scanCanvasExecution(r.db.QueryRowContext(ctx, `SELECT `+canvasExecutionColumns+` FROM creative_canvas_executions WHERE id=$1 AND user_id=$2`, id, u))
}
func (r *creativeCanvasRepository) FindExecutionByTaskID(ctx context.Context, taskID string) (*service.CreativeCanvasExecution, error) {
	return scanCanvasExecution(r.db.QueryRowContext(ctx, `SELECT `+canvasExecutionColumns+` FROM creative_canvas_executions WHERE task_id=$1 LIMIT 1`, taskID))
}
func (r *creativeCanvasRepository) ListExecutions(ctx context.Context, u int64, canvasID string, limit, offset int) ([]*service.CreativeCanvasExecution, int64, error) {
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM creative_canvas_executions WHERE user_id=$1 AND canvas_id=$2`, u, canvasID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+canvasExecutionColumns+` FROM creative_canvas_executions WHERE user_id=$1 AND canvas_id=$2 ORDER BY created_at DESC,id LIMIT $3 OFFSET $4`, u, canvasID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]*service.CreativeCanvasExecution, 0)
	for rows.Next() {
		v, e := scanCanvasExecution(rows)
		if e != nil {
			return nil, 0, e
		}
		items = append(items, v)
	}
	return items, total, rows.Err()
}
func (r *creativeCanvasRepository) ClaimExecutions(ctx context.Context, limit int, ttl time.Duration) ([]*service.CreativeCanvasExecution, error) {
	token := uuid.NewString()
	rows, err := r.db.QueryContext(ctx, `UPDATE creative_canvas_executions SET lease_token=$1,lease_until=NOW()+($2 * INTERVAL '1 second') WHERE id IN (SELECT id FROM creative_canvas_executions WHERE status IN ('queued','running') AND next_poll_at<=NOW() AND (lease_until IS NULL OR lease_until<NOW()) ORDER BY next_poll_at FOR UPDATE SKIP LOCKED LIMIT $3) RETURNING `+canvasExecutionColumns, token, ttl.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*service.CreativeCanvasExecution, 0)
	for rows.Next() {
		v, e := scanCanvasExecution(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (r *creativeCanvasRepository) SaveExecution(ctx context.Context, v *service.CreativeCanvasExecution, next time.Time) error {
	res, err := r.db.ExecContext(ctx, `UPDATE creative_canvas_executions SET task_id=$3,status=$4,error_code=$5,error_message=$6,next_poll_at=$7,lease_until=NULL,lease_token=NULL,updated_at=NOW() WHERE id=$1 AND lease_token=$2 AND lease_until>NOW() AND status IN ('queued','running')`, v.ID, v.LeaseToken, v.TaskID, v.Status, v.ErrorCode, v.ErrorMessage, next)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return service.ErrCanvasConflict
	}
	return err
}
