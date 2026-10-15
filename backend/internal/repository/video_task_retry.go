package repository

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

// SwitchRejected 沿用原任务、价卡和预扣；目标账号容量与租约在同一事务内验证。
// @project-doc docs/domains/video_tasks.md#video_task_lifecycle
func (r *videoTaskRepository) SwitchRejected(ctx context.Context, id, lease string, target service.VideoUpstreamTarget, metadata service.VideoRequestMetadata, accountRate float64, limit int) (*service.VideoTaskRecord, error) {
	if target.AccountID <= 0 || target.Version != 1 || limit <= 0 || accountRate < 0 || math.IsNaN(accountRate) || math.IsInf(accountRate, 0) {
		return nil, service.ErrVideoTaskConflict
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// 与 Create 保持先账号容量锁、后任务行锁的顺序，不能因换号绕过并发上限。
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", -target.AccountID); err != nil {
		return nil, err
	}
	task, err := scanVideoTask(tx.QueryRowContext(ctx, "SELECT "+videoTaskColumns+" FROM video_tasks WHERE id=$1 FOR UPDATE", id))
	if err != nil {
		return nil, err
	}
	var valid bool
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(lease_until>clock_timestamp(),FALSE) FROM video_tasks WHERE id=$1", id).Scan(&valid); err != nil {
		return nil, err
	}
	if lease == "" || task.LeaseToken != lease || !valid || !service.VideoTaskCanRetrySubmission(task) ||
		task.AccountID == target.AccountID || task.Target.Endpoint != target.Endpoint {
		return nil, service.ErrVideoTaskConflict
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM video_tasks WHERE account_id=$1 AND status NOT IN ('completed','failed','cancelled','expired')`, target.AccountID).Scan(&count); err != nil {
		return nil, err
	}
	if count >= limit {
		return nil, service.ErrVideoTaskBusy
	}
	// 只迁移非资金归属；旧拒绝包已经持久化，禁止把调用方传来的新价格覆盖原合同。
	task.RejectedAttempts = append(task.RejectedAttempts, service.VideoRejectedAttempt{Target: task.Target, StatusCode: task.CreateResponseStatus, RejectedAt: time.Now().UTC()})
	task.AccountID, task.Target, task.Metadata, task.AccountRateMultiplier = target.AccountID, target, metadata, accountRate
	task.Hold.VideoAccountID = target.AccountID
	task.Status, task.UpstreamStatus, task.ErrorMessage = "prepared", "", ""
	task.CompletedAt, task.CreateResponse, task.RawResponse = nil, nil, nil
	task.CreateResponseStatus, task.ResponseStatus = 0, 0
	task.NextPollAt = time.Now().UTC().Add(15 * time.Second)
	raw, err := json.Marshal(task)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE video_tasks SET account_id=$2,record=$3,status='prepared',upstream_task_id='',updated_at=NOW(),
 next_poll_at=$4,lease_until=clock_timestamp()+INTERVAL '3 minutes' WHERE id=$1`, id, target.AccountID, string(raw), task.NextPollAt); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		// 提交结果未知时不得调用新上游，由恢复器按持久状态释放尚未发送的 prepared 任务。
		return nil, err
	}
	return task, nil
}
