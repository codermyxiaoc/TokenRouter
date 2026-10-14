package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
)

// legacyVideoTaskRepository 只维护恢复状态，不能直接修改用户余额或替代账务幂等账本。
type legacyVideoTaskRepository struct{ db *sql.DB }

func NewLegacyVideoTaskRepository(db *sql.DB) service.LegacyVideoTaskRepository {
	return &legacyVideoTaskRepository{db: db}
}

const legacyVideoTaskColumns = `id,source,task_id,user_id,billing_user_id,api_key_id,group_id,account_id,
 model,request_id,pending,status,billing_status,settlement_result,created_at,updated_at,next_poll_at,
 lease_token,lease_until,attempts,last_error,completed_at,upstream_deleted_at`

func scanLegacyVideoTask(row interface{ Scan(...any) error }) (*service.LegacyVideoTask, error) {
	t := &service.LegacyVideoTask{}
	var pending, result []byte
	if err := row.Scan(&t.ID, &t.Source, &t.TaskID, &t.UserID, &t.BillingUserID, &t.APIKeyID, &t.GroupID, &t.AccountID,
		&t.Model, &t.RequestID, &pending, &t.Status, &t.BillingStatus, &result, &t.CreatedAt, &t.UpdatedAt,
		&t.NextPollAt, &t.LeaseToken, &t.LeaseUntil, &t.Attempts, &t.LastError, &t.CompletedAt, &t.UpstreamDeletedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrLegacyVideoTaskNotFound
		}
		return nil, err
	}
	if len(pending) > 0 {
		if err := json.Unmarshal(pending, &t.Pending); err != nil {
			return nil, fmt.Errorf("读取旧视频任务快照失败: %w", err)
		}
	}
	t.SettlementResult = json.RawMessage(result)
	return t, nil
}

// 快照必须是有界对象；仓储不接收任意响应体或素材字节作为恢复数据。
func legacyVideoTaskJSON(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return nil, nil
	}
	if len(raw) > 256*1024 || len(raw) == 0 || raw[0] != '{' {
		return nil, fmt.Errorf("旧视频任务快照必须为不超过 256 KiB 的对象")
	}
	return string(raw), nil
}

func validateLegacyVideoTask(t *service.LegacyVideoTask) error {
	if t == nil || (t.Source != "grok_video" && t.Source != "seedance_video") ||
		strings.TrimSpace(t.TaskID) == "" || len(t.TaskID) > 255 || t.UserID <= 0 || t.APIKeyID <= 0 ||
		t.GroupID <= 0 || t.AccountID <= 0 || t.BillingUserID < 0 {
		return service.ErrLegacyVideoTaskConflict
	}
	switch t.Status {
	case "queued", "processing", "completed", "failed", "cancelled", "expired":
	default:
		return service.ErrLegacyVideoTaskConflict
	}
	switch t.BillingStatus {
	case "pending", "settled", "not_billable", "reconciliation":
	default:
		return service.ErrLegacyVideoTaskConflict
	}
	return nil
}

// Save 并发去重且保留第一次保存的完整快照；旧任务只允许补齐原本缺失的信息。
func (r *legacyVideoTaskRepository) Save(ctx context.Context, t *service.LegacyVideoTask) error {
	return saveLegacyVideoTask(ctx, r.db, t)
}

// 创建保存与删除事实共用归属约束；事务调用者可把历史补录与删除标记原子提交。
func saveLegacyVideoTask(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, t *service.LegacyVideoTask) error {
	if t != nil {
		if t.Status == "" {
			t.Status = "queued"
		}
		if t.BillingStatus == "" {
			t.BillingStatus = "pending"
		}
	}
	if err := validateLegacyVideoTask(t); err != nil {
		return err
	}
	pending, err := legacyVideoTaskJSON(t.Pending)
	if err != nil {
		return err
	}
	result, err := legacyVideoTaskJSON(t.SettlementResult)
	if err != nil {
		return err
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	if t.NextPollAt.IsZero() {
		t.NextPollAt = t.CreatedAt
	}
	stored, err := scanLegacyVideoTask(db.QueryRowContext(ctx, `INSERT INTO legacy_video_tasks
 (source,task_id,user_id,billing_user_id,api_key_id,group_id,account_id,model,request_id,pending,status,billing_status,
 settlement_result,created_at,next_poll_at,completed_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
 ON CONFLICT(source,task_id,api_key_id) DO UPDATE SET
 pending=COALESCE(legacy_video_tasks.pending,EXCLUDED.pending),
 next_poll_at=CASE WHEN legacy_video_tasks.pending IS NULL AND EXCLUDED.pending IS NOT NULL
 AND legacy_video_tasks.billing_status IN ('pending','reconciliation')
 THEN LEAST(legacy_video_tasks.next_poll_at,NOW()) ELSE legacy_video_tasks.next_poll_at END,
 billing_user_id=CASE WHEN legacy_video_tasks.billing_user_id=0 THEN EXCLUDED.billing_user_id ELSE legacy_video_tasks.billing_user_id END,
 model=CASE WHEN legacy_video_tasks.model='' THEN EXCLUDED.model ELSE legacy_video_tasks.model END,
 request_id=CASE WHEN legacy_video_tasks.request_id='' THEN EXCLUDED.request_id ELSE legacy_video_tasks.request_id END,
 updated_at=NOW()
 WHERE legacy_video_tasks.user_id=EXCLUDED.user_id AND legacy_video_tasks.group_id=EXCLUDED.group_id
 AND legacy_video_tasks.account_id=EXCLUDED.account_id
 AND (legacy_video_tasks.billing_user_id=0 OR EXCLUDED.billing_user_id=0 OR legacy_video_tasks.billing_user_id=EXCLUDED.billing_user_id)
 AND (legacy_video_tasks.request_id='' OR EXCLUDED.request_id='' OR legacy_video_tasks.request_id=EXCLUDED.request_id)
 RETURNING `+legacyVideoTaskColumns, t.Source, t.TaskID, t.UserID, t.BillingUserID, t.APIKeyID, t.GroupID,
		t.AccountID, t.Model, t.RequestID, pending, t.Status, t.BillingStatus, result, t.CreatedAt, t.NextPollAt, t.CompletedAt))
	if errors.Is(err, service.ErrLegacyVideoTaskNotFound) {
		return service.ErrLegacyVideoTaskConflict
	}
	if err != nil {
		return err
	}
	*t = *stored
	return nil
}

// MarkUpstreamDeleted 不撤销工作租约，已在途查询仍可保存真实完成证据；空删除响应不推断免费。
func (r *legacyVideoTaskRepository) MarkUpstreamDeleted(ctx context.Context, task *service.LegacyVideoTask) error {
	if task == nil || task.Source != "seedance_video" {
		return service.ErrLegacyVideoTaskConflict
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := saveLegacyVideoTask(ctx, tx, task); err != nil {
		return err
	}
	err = tx.QueryRowContext(ctx, `UPDATE legacy_video_tasks SET
 upstream_deleted_at=COALESCE(upstream_deleted_at,NOW()),updated_at=NOW(),
 billing_status=CASE WHEN billing_status IN ('pending','reconciliation') THEN 'reconciliation' ELSE billing_status END,
 last_error=CASE WHEN billing_status IN ('pending','reconciliation') THEN '上游任务已删除，结算依据待核对' ELSE last_error END,
 next_poll_at=LEAST(next_poll_at,NOW())
 WHERE id=$1 AND source='seedance_video' AND task_id=$2 AND user_id=$3 AND billing_user_id=$4
 AND api_key_id=$5 AND group_id=$6 AND account_id=$7
 RETURNING upstream_deleted_at,billing_status`, task.ID, task.TaskID, task.UserID, task.BillingUserID,
		task.APIKeyID, task.GroupID, task.AccountID).Scan(&task.UpstreamDeletedAt, &task.BillingStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrLegacyVideoTaskConflict
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *legacyVideoTaskRepository) Get(ctx context.Context, source, taskID string, userID, apiKeyID int64) (*service.LegacyVideoTask, error) {
	if userID <= 0 || apiKeyID <= 0 {
		return nil, service.ErrLegacyVideoTaskNotFound
	}
	// 原客户端查询沿用付款用户身份；后台观测可按创建时行为用户恢复，二者均须持有原 Key。
	return scanLegacyVideoTask(r.db.QueryRowContext(ctx, "SELECT "+legacyVideoTaskColumns+
		" FROM legacy_video_tasks WHERE source=$1 AND task_id=$2 AND (user_id=$3 OR billing_user_id=$3) AND api_key_id=$4", source, taskID, userID, apiKeyID))
}

// ClaimDue 原子领取到期任务；不同实例跳过已锁行，过期执行者不能覆盖新租约。
func (r *legacyVideoTaskRepository) ClaimDue(ctx context.Context, now time.Time, limit int, duration time.Duration) ([]*service.LegacyVideoTask, error) {
	if limit <= 0 || limit > 1000 || duration <= 0 || duration > time.Hour {
		return nil, fmt.Errorf("旧视频任务领取参数无效")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	columns := strings.Split(legacyVideoTaskColumns, ",")
	for i := range columns {
		columns[i] = "t." + strings.TrimSpace(columns[i])
	}
	rows, err := r.db.QueryContext(ctx, `WITH due AS (
 SELECT id FROM legacy_video_tasks WHERE billing_status IN ('pending','reconciliation')
 AND next_poll_at <= $1 AND (lease_until IS NULL OR lease_until <= $1)
 AND (upstream_deleted_at IS NULL OR billing_result IS NOT NULL
 OR settlement_result->'Observation'->>'Status' IN ('completed','failed','cancelled','expired')
 OR (settlement_result->'Quote' IS NOT NULL AND settlement_result->'Quote'<>'null'::jsonb))
 ORDER BY next_poll_at,id LIMIT $2 FOR UPDATE SKIP LOCKED)
 UPDATE legacy_video_tasks t SET lease_token=$3,lease_until=$4,attempts=t.attempts+1,updated_at=NOW()
 FROM due WHERE t.id=due.id RETURNING `+strings.Join(columns, ","), now, limit, uuid.NewString(), now.Add(duration))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []*service.LegacyVideoTask{}
	for rows.Next() {
		t, err := scanLegacyVideoTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// Finish 只接受仍有效的租约和原归属；终态、计费终态及首次快照均不能被旧执行结果回退。
func (r *legacyVideoTaskRepository) Finish(ctx context.Context, t *service.LegacyVideoTask) error {
	return r.updateLeased(ctx, t, true)
}

// Checkpoint 在扣费前先保存可靠用量与计价依据，保持当前租约让同一执行者继续结算。
func (r *legacyVideoTaskRepository) Checkpoint(ctx context.Context, t *service.LegacyVideoTask) error {
	return r.updateLeased(ctx, t, false)
}

func (r *legacyVideoTaskRepository) updateLeased(ctx context.Context, t *service.LegacyVideoTask, release bool) error {
	if err := validateLegacyVideoTask(t); err != nil {
		return err
	}
	if t.ID <= 0 || t.LeaseToken == "" || t.NextPollAt.IsZero() {
		return service.ErrLegacyVideoTaskLeaseLost
	}
	pending, err := legacyVideoTaskJSON(t.Pending)
	if err != nil {
		return err
	}
	result, err := legacyVideoTaskJSON(t.SettlementResult)
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, `UPDATE legacy_video_tasks SET status=$3,
 billing_status=CASE WHEN upstream_deleted_at IS NOT NULL AND $3 NOT IN ('completed','failed','cancelled','expired') THEN 'reconciliation' ELSE $4 END,
 pending=COALESCE(pending,$5),settlement_result=COALESCE($6,settlement_result),
 billing_user_id=CASE WHEN billing_user_id=0 THEN $7 ELSE billing_user_id END,
 next_poll_at=$8,last_error=CASE WHEN upstream_deleted_at IS NOT NULL AND $3 NOT IN ('completed','failed','cancelled','expired') THEN '上游任务已删除，结算依据待核对' ELSE $9 END,
 completed_at=COALESCE(completed_at,$10),updated_at=NOW(),
 lease_token=CASE WHEN $17 THEN '' ELSE lease_token END,lease_until=CASE WHEN $17 THEN NULL ELSE lease_until END
 WHERE id=$1 AND lease_token=$2 AND lease_until>NOW()
 AND source=$11 AND task_id=$12 AND user_id=$13 AND api_key_id=$14 AND group_id=$15 AND account_id=$16
 AND (billing_user_id=0 OR billing_user_id=$7)
 AND (status NOT IN ('completed','failed','cancelled','expired') OR status=$3)
 AND (billing_status NOT IN ('settled','not_billable') OR billing_status=$4)`, t.ID, t.LeaseToken,
		t.Status, t.BillingStatus, pending, result, t.BillingUserID, t.NextPollAt, truncateLegacyVideoTaskError(t.LastError),
		t.CompletedAt, t.Source, t.TaskID, t.UserID, t.APIKeyID, t.GroupID, t.AccountID, release)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return service.ErrLegacyVideoTaskLeaseLost
	}
	return err
}

func truncateLegacyVideoTaskError(message string) string {
	// 这里只截断恢复器的安全分类信息，调用者不能传入含凭据的上游原文。
	runes := []rune(message)
	if len(runes) > 500 {
		runes = runes[:500]
	}
	return string(runes)
}

// Backfill 只恢复近七天已有任务，不生成新上游任务，不猜测历史价格或付款用户。
// 已有使用记录/持久账本的完成任务不重建结算；尚未结束的记录仍可恢复其最终状态。
func (r *legacyVideoTaskRepository) Backfill(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 1000 {
		return 0, fmt.Errorf("旧视频任务回填数量无效")
	}
	res, err := r.db.ExecContext(ctx, `WITH candidates AS (
 SELECT m.*,COALESCE(NULLIF(m.request_id,''),'grok-video:' || CASE WHEN m.source='seedance_video' THEN 'seedance:' ELSE '' END || m.task_id) AS billing_request_id
 FROM media_tasks m WHERE m.source IN ('grok_video','seedance_video')
 AND m.status IN ('queued','processing','completed') AND m.created_at>=NOW()-INTERVAL '7 days'
 AND m.group_id IS NOT NULL AND m.account_id IS NOT NULL)
 INSERT INTO legacy_video_tasks
 (source,task_id,user_id,api_key_id,group_id,account_id,model,request_id,status,created_at,completed_at,next_poll_at)
 SELECT m.source,m.task_id,m.user_id,m.api_key_id,m.group_id,m.account_id,m.model,
 m.billing_request_id,
 m.status,m.created_at,m.completed_at,NOW()
 FROM candidates m WHERE NOT EXISTS(SELECT 1 FROM legacy_video_tasks t WHERE t.source=m.source AND t.task_id=m.task_id AND t.api_key_id=m.api_key_id)
 AND (m.status<>'completed' OR (
 NOT EXISTS(SELECT 1 FROM usage_logs u WHERE u.api_key_id=m.api_key_id AND u.request_id=m.billing_request_id)
 AND NOT EXISTS(SELECT 1 FROM usage_billing_dedup b WHERE b.api_key_id=m.api_key_id AND b.request_id=m.billing_request_id)
 AND NOT EXISTS(SELECT 1 FROM usage_billing_dedup_archive b WHERE b.api_key_id=m.api_key_id AND b.request_id=m.billing_request_id)))
 ORDER BY m.created_at,m.id LIMIT $1 ON CONFLICT(source,task_id,api_key_id) DO NOTHING`, limit)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func (r *legacyVideoTaskRepository) HasBilling(ctx context.Context, requestID string, apiKeyID int64) (bool, error) {
	var found bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2
 UNION ALL SELECT 1 FROM usage_billing_dedup_archive WHERE request_id=$1 AND api_key_id=$2)`, requestID, apiKeyID).Scan(&found)
	return found, err
}

func (r *legacyVideoTaskRepository) HasUsage(ctx context.Context, requestID string, apiKeyID int64) (bool, error) {
	var found bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM usage_logs WHERE request_id=$1 AND api_key_id=$2)`, requestID, apiKeyID).Scan(&found)
	return found, err
}
