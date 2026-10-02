package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
)

// 视频状态以数据库为归属真源，租约与状态写入共同隔离多实例恢复器。
// @project-doc docs/domains/video_tasks.md#video_task_storage
type videoTaskRepository struct{ db *sql.DB }

func NewVideoTaskRepository(db *sql.DB) service.VideoTaskRepository {
	return &videoTaskRepository{db: db}
}

const videoTaskColumns = `record,status,billing_status,upstream_task_id,allowance_reserved,balance_hold_amount,
 subscription_hold_allocations,hold_amount,estimated_cost,billing_result,lease_token,lease_until,next_poll_at`

func scanVideoTask(row interface{ Scan(...any) error }) (*service.VideoTaskRecord, error) {
	var raw, allocations, result []byte
	var status, billingStatus, upstreamID, lease string
	var reserved bool
	var balance, hold, estimated float64
	var until sql.NullTime
	var next time.Time
	if err := row.Scan(&raw, &status, &billingStatus, &upstreamID, &reserved, &balance, &allocations, &hold, &estimated, &result, &lease, &until, &next); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrVideoTaskNotFound
		}
		return nil, err
	}
	var task service.VideoTaskRecord
	if err := json.Unmarshal(raw, &task); err != nil {
		return nil, err
	}
	task.Status, task.BillingStatus, task.UpstreamTaskID = status, billingStatus, upstreamID
	task.Hold.AllowanceReserved, task.Hold.BalanceHoldAmount, task.Hold.HoldAmount = reserved, balance, hold
	if err := json.Unmarshal(allocations, &task.Hold.SubscriptionHoldAllocations); err != nil {
		return nil, err
	}
	if len(result) > 0 {
		if err := json.Unmarshal(result, &task.BillingResult); err != nil {
			return nil, err
		}
	}
	task.LeaseToken, task.LeaseUntil, task.NextPollAt = lease, until.Time, next
	return &task, nil
}

func (r *videoTaskRepository) Get(ctx context.Context, id string) (*service.VideoTaskRecord, error) {
	return scanVideoTask(r.db.QueryRowContext(ctx, "SELECT "+videoTaskColumns+" FROM video_tasks WHERE id=$1", id))
}

func (r *videoTaskRepository) FindIdempotent(ctx context.Context, keyID int64, key string) (*service.VideoTaskRecord, error) {
	return scanVideoTask(r.db.QueryRowContext(ctx, "SELECT "+videoTaskColumns+" FROM video_tasks WHERE api_key_id=$1 AND idempotency_key=$2", keyID, key))
}

// Find 原生ID必须在账号来源不歧义时使用；任何歧义都不能广播查询或覆盖旧归属。
func (r *videoTaskRepository) Find(ctx context.Context, keyID, userID int64, id, protocol string) (*service.VideoTaskRecord, error) {
	// 两种统一入口都按本站任务 ID 读取，不把本地 ID 当作供应商 ID 广播查找。
	if protocol == "" || protocol == "compat" || protocol == "unified" || protocol == "openai_videos" {
		return scanVideoTask(r.db.QueryRowContext(ctx, "SELECT "+videoTaskColumns+" FROM video_tasks WHERE id=$1 AND api_key_id=$2 AND user_id=$3", id, keyID, userID))
	}
	rows, err := r.db.QueryContext(ctx, "SELECT "+videoTaskColumns+" FROM video_tasks WHERE upstream_task_id=$1 AND api_key_id=$2 AND user_id=$3 AND protocol=$4 LIMIT 2", id, keyID, userID, protocol)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var task *service.VideoTaskRecord
	for rows.Next() {
		if task != nil {
			return nil, service.ErrVideoTaskConflict
		}
		task, err = scanVideoTask(rows)
		if err != nil {
			return nil, err
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if task == nil {
		return nil, service.ErrVideoTaskNotFound
	}
	return task, nil
}

func (r *videoTaskRepository) Create(ctx context.Context, t *service.VideoTaskRecord, limit int) (*service.VideoTaskRecord, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	// 账号任务上限在数据库串行检查，不受多实例并发提交绕过。
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", -t.AccountID); err != nil {
		return nil, false, err
	}
	if t.IdempotencyKey != "" {
		old, e := scanVideoTask(tx.QueryRowContext(ctx, "SELECT "+videoTaskColumns+" FROM video_tasks WHERE api_key_id=$1 AND idempotency_key=$2", t.APIKeyID, t.IdempotencyKey))
		if e == nil {
			return old, false, nil
		}
		if !errors.Is(e, service.ErrVideoTaskNotFound) {
			return nil, false, e
		}
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM video_tasks WHERE account_id=$1 AND status NOT IN ('completed','failed','cancelled')`, t.AccountID).Scan(&count); err != nil {
		return nil, false, err
	}
	if count >= limit {
		return nil, false, service.ErrVideoTaskBusy
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return nil, false, err
	}
	var key any
	if t.IdempotencyKey != "" {
		key = t.IdempotencyKey
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO video_tasks(id,user_id,api_key_id,account_id,group_id,protocol,idempotency_key,payload_hash,record,lease_token,lease_until,created_at,next_poll_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12) ON CONFLICT(api_key_id,idempotency_key) DO NOTHING`,
		t.ID, t.UserID, t.APIKeyID, t.AccountID, t.GroupID, t.Target.Endpoint, key, t.PayloadHash, string(raw), t.LeaseToken, t.LeaseUntil, t.CreatedAt)
	if err != nil {
		return nil, false, err
	}
	n, _ := res.RowsAffected()
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	if n == 0 {
		old, e := r.FindIdempotent(ctx, t.APIKeyID, t.IdempotencyKey)
		return old, false, e
	}
	return t, true, nil
}

func (r *videoTaskRepository) Claim(ctx context.Context, id string, duration time.Duration) (*service.VideoTaskRecord, error) {
	token := uuid.NewString()
	return scanVideoTask(r.db.QueryRowContext(ctx, `UPDATE video_tasks SET lease_token=$2,lease_until=NOW()+$3 * INTERVAL '1 second'
 WHERE id=$1 AND (lease_until IS NULL OR lease_until<=NOW()) RETURNING `+videoTaskColumns, id, token, duration.Seconds()))
}

func (r *videoTaskRepository) ClaimReady(ctx context.Context, limit int, duration time.Duration) ([]*service.VideoTaskRecord, error) {
	token := uuid.NewString()
	rows, err := r.db.QueryContext(ctx, `WITH ready AS (SELECT id FROM video_tasks
 WHERE next_poll_at<=NOW() AND (lease_until IS NULL OR lease_until<=NOW()) AND effects_done=FALSE
 ORDER BY next_poll_at LIMIT $1 FOR UPDATE SKIP LOCKED)
 UPDATE video_tasks t SET lease_token=$2,lease_until=NOW()+$3 * INTERVAL '1 second' FROM ready WHERE t.id=ready.id
 RETURNING `+prefixVideoColumns("t"), limit, token, duration.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []*service.VideoTaskRecord{}
	for rows.Next() {
		t, e := scanVideoTask(rows)
		if e != nil {
			return nil, e
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

func prefixVideoColumns(alias string) string {
	return fmt.Sprintf(`%[1]s.record,%[1]s.status,%[1]s.billing_status,%[1]s.upstream_task_id,%[1]s.allowance_reserved,%[1]s.balance_hold_amount,
 %[1]s.subscription_hold_allocations,%[1]s.hold_amount,%[1]s.estimated_cost,%[1]s.billing_result,%[1]s.lease_token,%[1]s.lease_until,%[1]s.next_poll_at`, alias)
}

func (r *videoTaskRepository) Save(ctx context.Context, t *service.VideoTaskRecord, release bool) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	// 金额和结算状态由资金事务更新，本方法不能用旧的JSON快照覆盖账务结果。
	res, err := r.db.ExecContext(ctx, `UPDATE video_tasks SET record=$3,status=$4,upstream_task_id=$5,next_poll_at=$6,updated_at=NOW(),effects_done=$8,
 billing_status=CASE WHEN billing_status IN ('settled','released') THEN billing_status ELSE $9 END,
 lease_until=CASE WHEN $7 THEN NULL ELSE NOW()+INTERVAL '3 minutes' END,lease_token=CASE WHEN $7 THEN '' ELSE lease_token END
 WHERE id=$1 AND lease_token=$2 AND lease_until>NOW()
 AND ($9<>'released' OR billing_status='released' OR (billing_status='pending' AND NOT allowance_reserved
 AND balance_hold_amount=0 AND hold_amount=0))`, t.ID, t.LeaseToken, string(raw), t.Status, t.UpstreamTaskID, t.NextPollAt, release, t.EffectsDone, t.BillingStatus)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return service.ErrVideoTaskConflict
	}
	return err
}
