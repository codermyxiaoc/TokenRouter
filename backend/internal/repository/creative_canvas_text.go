package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

// 发送栅栏与任务关联同事务更新；失去执行租约的旧实例不能再次发送。
func (r *creativeCanvasRepository) BeginTextExecution(ctx context.Context, e *service.CreativeCanvasExecution, taskID string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE creative_canvas_executions SET task_id=$4,text_phase='provider_inflight',status='running',updated_at=NOW() WHERE id=$1 AND user_id=$2 AND kind='text' AND text_phase='' AND task_id='' AND lease_token=$3 AND lease_until>NOW()`, e.ID, e.UserID, e.LeaseToken, taskID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (r *creativeCanvasRepository) GetTextExecutionState(ctx context.Context, u int64, id string) (*service.CreativeTextExecutionState, error) {
	state := &service.CreativeTextExecutionState{}
	var evidence, receipt []byte
	err := r.db.QueryRowContext(ctx, `SELECT text_phase,text_response_status,output_text,text_billing_evidence,text_billing_result FROM creative_canvas_executions WHERE id=$1 AND user_id=$2 AND kind='text'`, id, u).Scan(&state.Phase, &state.HTTPStatus, &state.OutputText, &evidence, &receipt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrCanvasNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(evidence) > 0 {
		if err = json.Unmarshal(evidence, &state.Evidence); err != nil {
			return nil, err
		}
	}
	if len(receipt) > 0 {
		if err = json.Unmarshal(receipt, &state.Receipt); err != nil {
			return nil, err
		}
	}
	return state, nil
}

func (r *creativeCanvasRepository) SaveTextResponse(ctx context.Context, u int64, id string, status int, text string) error {
	if len(text) > service.CreativeTextOutputLimit {
		return service.ErrCanvasInvalid
	}
	result, err := r.db.ExecContext(ctx, `UPDATE creative_canvas_executions SET text_response_status=$3,output_text=CASE WHEN output_text='' THEN $4 ELSE output_text END,text_phase=CASE WHEN text_billing_evidence IS NULL THEN 'response_saved' ELSE 'settlement_pending' END,updated_at=NOW() WHERE id=$1 AND user_id=$2 AND kind='text' AND text_phase<>'' AND status IN ('queued','running')`, id, u, status, text)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return service.ErrCanvasConflict
	}
	return err
}

func (r *creativeCanvasRepository) SaveTextBillingEvidence(ctx context.Context, u int64, id string, e *service.CreativeTextBillingEvidence, status int, text string) error {
	if e == nil || e.Version != 1 || len(text) > service.CreativeTextOutputLimit || e.Command.UserID != u || e.Command.ActorUserID != u || e.Command.CreativeTextExecutionID != id || e.Command.RequestID != "creative_text:"+id || e.Usage.RequestID != e.Command.RequestID {
		return service.ErrCanvasInvalid
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(raw) > 64<<10 {
		return service.ErrCanvasInvalid
	}
	result, err := r.db.ExecContext(ctx, `UPDATE creative_canvas_executions SET text_billing_evidence=COALESCE(text_billing_evidence,$3::jsonb),text_response_status=CASE WHEN text_response_status=0 THEN $4 ELSE text_response_status END,output_text=CASE WHEN output_text='' THEN $5 ELSE output_text END,text_phase='settlement_pending',updated_at=NOW() WHERE id=$1 AND user_id=$2 AND kind='text' AND text_phase<>'' AND status IN ('queued','running') AND (text_billing_evidence IS NULL OR text_billing_evidence=$3::jsonb)`, id, u, string(raw), status, strings.TrimSpace(text))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return service.ErrCanvasConflict
	}
	return err
}
