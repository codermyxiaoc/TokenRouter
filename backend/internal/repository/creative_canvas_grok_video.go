package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
)

// BeginGrokVideoExecution 把发送栅栏与画布合成任务号放在同一事务，只有当前租约可首次发送。
// @project-doc docs/domains/cloud_canvas.md#grok_video_execution
func (r *creativeCanvasRepository) BeginGrokVideoExecution(ctx context.Context, e *service.CreativeCanvasExecution, keyID int64) (bool, error) {
	if e == nil || e.Kind != "video" || e.Params.Endpoint != string(service.CreativeVideoEndpointGrok) || e.UserID <= 0 || e.Params.GroupID <= 0 || keyID <= 0 || e.LeaseToken == "" {
		return false, service.ErrCanvasInvalid
	}
	if _, err := uuid.Parse(e.ID); err != nil {
		return false, service.ErrCanvasInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE creative_canvas_executions e
 SET task_id=$4,status='running',updated_at=NOW()
 WHERE id=$1 AND user_id=$2 AND kind='video' AND params->>'endpoint'='grok'
 AND (params->>'group_id')::bigint=$5 AND lease_token=$3 AND lease_until>NOW()
 AND status IN ('queued','running') AND task_id=''
 AND NOT EXISTS(SELECT 1 FROM creative_grok_video_executions g WHERE g.execution_id=e.id)`,
		e.ID, e.UserID, e.LeaseToken, "creative_grok_video:"+e.ID, e.Params.GroupID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO creative_grok_video_executions
 (execution_id,user_id,api_key_id,group_id,phase) VALUES($1,$2,$3,$4,'provider_inflight')`, e.ID, e.UserID, keyID, e.Params.GroupID)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// 即使尚未发送也先校验真实执行归属，禁止用其它用户的 ID 探测持久状态。
func (r *creativeCanvasRepository) GetGrokVideoExecutionState(ctx context.Context, userID int64, executionID string) (*service.CreativeGrokVideoExecutionState, error) {
	s := &service.CreativeGrokVideoExecutionState{}
	err := r.db.QueryRowContext(ctx, `SELECT COALESCE(g.phase,''),COALESCE(g.api_key_id,0),
 COALESCE(g.group_id,0),COALESCE(g.task_id,''),COALESCE(g.account_id,0),
 COALESCE(g.http_status,0),COALESCE(g.request_sent,false),COALESCE(g.error_code,'')
 FROM creative_canvas_executions e LEFT JOIN creative_grok_video_executions g ON g.execution_id=e.id AND g.user_id=e.user_id
 WHERE e.id=$1 AND e.user_id=$2 AND e.kind='video' AND e.params->>'endpoint'='grok'`, executionID, userID).
		Scan(&s.Phase, &s.APIKeyID, &s.GroupID, &s.TaskID, &s.AccountID, &s.HTTPStatus, &s.RequestSent, &s.ErrorCode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrCanvasNotFound
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// 已受理原任务优先于迟到的 HTTP 错误；响应只写一次，同值重放不会改变已冻结的事实。
func (r *creativeCanvasRepository) SaveGrokVideoResponse(ctx context.Context, userID int64, executionID string, status int, sent bool, code string) error {
	if status < 0 || status > 599 || len(code) > 128 || strings.ContainsAny(code, "\r\n") {
		return service.ErrCanvasInvalid
	}
	result, err := r.db.ExecContext(ctx, `UPDATE creative_grok_video_executions SET
 http_status=CASE WHEN phase='provider_inflight' THEN $3 ELSE http_status END,
 request_sent=CASE WHEN phase='provider_inflight' THEN $4 ELSE request_sent END,
 error_code=CASE WHEN phase='provider_inflight' THEN $5 ELSE error_code END,
 phase=CASE WHEN phase='provider_inflight' THEN 'response_saved' ELSE phase END,updated_at=NOW()
 WHERE execution_id=$1 AND user_id=$2 AND
 (phase IN ('provider_inflight','accepted') OR (phase='response_saved' AND http_status=$3 AND request_sent=$4 AND error_code=$5))`, executionID, userID, status, sent, code)
	return grokVideoUpdated(result, err)
}

func (r *creativeCanvasRepository) BindGrokVideoTask(ctx context.Context, userID int64, executionID string, task *service.LegacyVideoTask) error {
	return bindCreativeGrokVideoTask(ctx, r.db, userID, executionID, task)
}

// 同事务关联原任务，状态恢复只认原用户、Key、分组和账号，不允许替换第一次绑定。
func bindCreativeGrokVideoTask(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, userID int64, executionID string, task *service.LegacyVideoTask) error {
	if task == nil || task.Source != "grok_video" || task.UserID != userID || task.BillingUserID != userID || task.APIKeyID <= 0 || task.AccountID <= 0 || task.GroupID <= 0 || strings.TrimSpace(task.TaskID) == "" || len(task.TaskID) > 255 {
		return service.ErrLegacyVideoTaskConflict
	}
	if _, err := uuid.Parse(executionID); err != nil {
		return service.ErrCanvasInvalid
	}
	result, err := db.ExecContext(ctx, `UPDATE creative_grok_video_executions g
 SET phase='accepted',task_id=$3,account_id=$4,request_sent=true,error_code='',updated_at=NOW()
 WHERE execution_id=$1 AND user_id=$2 AND api_key_id=$5 AND group_id=$6
 AND (task_id='' OR (task_id=$3 AND account_id=$4))
 AND EXISTS(SELECT 1 FROM legacy_video_tasks l WHERE l.source='grok_video' AND l.task_id=$3
 AND l.user_id=$2 AND l.billing_user_id=$2 AND l.api_key_id=$5 AND l.group_id=$6 AND l.account_id=$4)`,
		executionID, userID, task.TaskID, task.AccountID, task.APIKeyID, task.GroupID)
	return grokVideoUpdated(result, err)
}

func grokVideoUpdated(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return service.ErrCanvasConflict
	}
	return err
}

var _ service.CreativeGrokVideoRepository = (*creativeCanvasRepository)(nil)
