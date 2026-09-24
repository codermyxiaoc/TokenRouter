package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

type mediaTaskRepository struct{ db *sql.DB }

func NewMediaTaskRepository(db *sql.DB) service.MediaTaskRepository {
	return &mediaTaskRepository{db: db}
}

// Observe 按供应商、任务和 Key 去重；已观测到终态后不被迟到的 processing 覆盖。
func (r *mediaTaskRepository) Observe(ctx context.Context, o service.MediaTaskObservation) error {
	result, err := r.db.ExecContext(ctx, `INSERT INTO media_tasks
 (source,task_id,media_type,platform,model,status,upstream_status,user_id,api_key_id,group_id,account_id,
 http_status,error_message,request_id,created_at,completed_at,expires_at)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
 ON CONFLICT (source,task_id,api_key_id) DO UPDATE SET
 status=CASE WHEN media_tasks.status IN ('completed','failed','cancelled','expired') THEN media_tasks.status ELSE EXCLUDED.status END,
 upstream_status=CASE WHEN media_tasks.status IN ('completed','failed','cancelled','expired') THEN media_tasks.upstream_status ELSE EXCLUDED.upstream_status END,
 http_status=CASE WHEN media_tasks.status IN ('completed','failed','cancelled','expired') THEN media_tasks.http_status ELSE EXCLUDED.http_status END,
 error_message=CASE WHEN media_tasks.status IN ('completed','failed','cancelled','expired') THEN media_tasks.error_message ELSE EXCLUDED.error_message END,
 group_id=COALESCE(media_tasks.group_id,EXCLUDED.group_id),
 account_id=COALESCE(media_tasks.account_id,EXCLUDED.account_id),
 model=CASE WHEN media_tasks.model='' THEN EXCLUDED.model ELSE media_tasks.model END,
 request_id=CASE WHEN media_tasks.request_id='' THEN EXCLUDED.request_id ELSE media_tasks.request_id END,
 created_at=LEAST(media_tasks.created_at,EXCLUDED.created_at),
 completed_at=COALESCE(media_tasks.completed_at,EXCLUDED.completed_at),
 expires_at=GREATEST(media_tasks.expires_at,EXCLUDED.expires_at), updated_at=NOW()
 WHERE media_tasks.user_id=EXCLUDED.user_id`,
		o.Source, o.TaskID, o.MediaType, o.Platform, o.Model, o.Status, o.UpstreamStatus, o.UserID,
		o.APIKeyID, o.GroupID, o.AccountID, o.HTTPStatus, o.ErrorMessage, o.RequestID, o.CreatedAt, o.CompletedAt, o.ExpiresAt)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err == nil && changed == 0 {
		return service.ErrMediaTaskNotFound
	}
	return err
}

// 过期的未完成任务在展示时标为过期；不推断上游未消费，也不触发重试或退款。
const mediaTaskStatusSQL = `CASE WHEN t.status IN ('queued','processing') AND t.expires_at <= NOW() THEN 'expired' ELSE t.status END`

// 智能路由可能切换实际执行分组，以同用户、同 Key 的使用记录修正最终分组和账号。
// 模型保留提交任务时的名称，避免列表筛选与模型映射后的别名不一致。
const mediaTaskColumns = `t.id,t.source,t.task_id,t.media_type,COALESCE(g.platform,t.platform),t.model,` + mediaTaskStatusSQL + `,
 t.upstream_status,t.user_id,t.api_key_id,COALESCE(u.group_id,t.group_id),g.name,COALESCE(u.account_id,t.account_id),t.http_status,t.error_message,t.request_id,
 t.created_at,t.updated_at,t.completed_at,t.expires_at,u.actual_cost,u.billing_mode,
 task_user.id,task_user.email,task_user.username,task_user.deleted_at`
const mediaTaskJoins = ` FROM media_tasks t
 LEFT JOIN LATERAL (SELECT actual_cost,billing_mode,group_id,account_id FROM usage_logs
 WHERE t.request_id<>'' AND request_id=t.request_id AND api_key_id=t.api_key_id AND user_id=t.user_id ORDER BY id DESC LIMIT 1) u ON TRUE
 LEFT JOIN groups g ON g.id=COALESCE(u.group_id,t.group_id)
 LEFT JOIN users task_user ON task_user.id=t.user_id`

func mediaTaskConditions(actor service.MediaTaskActor, f service.MediaTaskFilter) (string, []any) {
	conditions := []string{"1=1"}
	args := []any{}
	add := func(column string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf("%s=$%d", column, len(args)))
	}
	if !actor.IsAdmin {
		add("t.user_id", actor.UserID)
	} else if f.UserID > 0 {
		add("t.user_id", f.UserID)
	}
	if f.MediaType != "" {
		add("t.media_type", f.MediaType)
	}
	if f.Status != "" {
		add("("+mediaTaskStatusSQL+")", f.Status)
	}
	if f.Source != "" {
		add("t.source", f.Source)
	}
	if f.Model != "" {
		// 搜索条件始终绑定参数，LIKE 通配符按普通模型名字符处理。
		model := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Model)
		args = append(args, "%"+model+"%")
		conditions = append(conditions, fmt.Sprintf("t.model ILIKE $%d", len(args)))
	}
	return strings.Join(conditions, " AND "), args
}

type mediaTaskScanner interface{ Scan(...any) error }

func scanMediaTask(row mediaTaskScanner) (*service.MediaTask, error) {
	t := &service.MediaTask{}
	var userID sql.NullInt64
	var email, username sql.NullString
	var deletedAt sql.NullTime
	err := row.Scan(&t.ID, &t.Source, &t.TaskID, &t.MediaType, &t.Platform, &t.Model, &t.Status,
		&t.UpstreamStatus, &t.UserID, &t.APIKeyID, &t.GroupID, &t.GroupName, &t.AccountID,
		&t.HTTPStatus, &t.ErrorMessage, &t.RequestID, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt, &t.ExpiresAt,
		&t.ActualCost, &t.BillingMode, &userID, &email, &username, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrMediaTaskNotFound
	}
	if err == nil && userID.Valid {
		// 不排除软删除用户，便于追溯历史归属；缺失关联时保留原 user_id 供界面安全回退。
		t.User = &service.MediaTaskUser{ID: userID.Int64, Email: email.String, Username: username.String}
		if deletedAt.Valid {
			t.User.DeletedAt = &deletedAt.Time
		}
	}
	return t, err
}

func (r *mediaTaskRepository) List(ctx context.Context, actor service.MediaTaskActor, filter service.MediaTaskFilter) (*service.MediaTaskList, error) {
	where, args := mediaTaskConditions(actor, filter)
	result := &service.MediaTaskList{Items: []service.MediaTask{}, Page: filter.Page, PageSize: filter.PageSize}
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM media_tasks t WHERE "+where, args...).Scan(&result.Total); err != nil {
		return nil, err
	}
	result.Pages = int((result.Total + int64(filter.PageSize) - 1) / int64(filter.PageSize))
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := r.db.QueryContext(ctx, "SELECT "+mediaTaskColumns+mediaTaskJoins+" WHERE "+where+
		fmt.Sprintf(" ORDER BY t.created_at DESC,t.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		task, err := scanMediaTask(rows)
		if err != nil {
			return nil, err
		}
		result.Items = append(result.Items, *task)
	}
	return result, rows.Err()
}

func (r *mediaTaskRepository) Get(ctx context.Context, actor service.MediaTaskActor, id int64) (*service.MediaTask, error) {
	// 归属约束与主键查询共同下推 SQL，跨用户任务与不存在任务使用同一种响应。
	return scanMediaTask(r.db.QueryRowContext(ctx, "SELECT "+mediaTaskColumns+mediaTaskJoins+
		" WHERE t.id=$1 AND ($2 OR t.user_id=$3)", id, actor.IsAdmin, actor.UserID))
}
