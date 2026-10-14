package repository

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/ent/creativerun"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

// ListCreativeRunsForUsageRecovery 只返回已有捕获账本且保留真实金额的近期终态任务。
func (r *creativeRunRepository) ListCreativeRunsForUsageRecovery(ctx context.Context, afterID int64, since time.Time, limit int) ([]*service.CreativeRun, error) {
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	rows, err := r.client.QueryContext(ctx, `
		SELECT r.id FROM creative_runs r
		WHERE r.id > $1 AND r.usage_recorded_at IS NULL
		  AND r.status IN ('succeeded', 'result_lost', 'cancelled')
		  AND r.completed_at >= $2 AND r.actual_cost IS NOT NULL AND r.account_id IS NOT NULL
		  AND r.provider_result_recorded_at IS NOT NULL
		  AND (EXISTS (SELECT 1 FROM usage_billing_dedup d
		               WHERE d.api_key_id=r.api_key_id AND d.request_id='creative_capture:' || r.run_id)
		       OR EXISTS (SELECT 1 FROM usage_billing_dedup_archive d
		                  WHERE d.api_key_id=r.api_key_id AND d.request_id='creative_capture:' || r.run_id))
		ORDER BY r.id LIMIT $3`, afterID, since, limit)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	entities, err := r.client.CreativeRun.Query().Where(creativerun.IDIn(ids...)).Order(creativerun.ByID()).All(ctx)
	if err != nil {
		return nil, err
	}
	runs := make([]*service.CreativeRun, 0, len(entities))
	for _, entity := range entities {
		runs = append(runs, creativeRunEntityToService(entity))
	}
	return runs, nil
}

// MarkCreativeUsageRecorded 必须确认同一用户和执行 Key 的日志存在，才能持久标记投递完成。
func (r *creativeRunRepository) MarkCreativeUsageRecorded(ctx context.Context, runID string) (bool, error) {
	rows, err := r.client.QueryContext(ctx, `
		UPDATE creative_runs r SET usage_recorded_at=COALESCE(r.usage_recorded_at, CURRENT_TIMESTAMP)
		WHERE r.run_id=$1 AND (r.usage_recorded_at IS NOT NULL OR EXISTS (
		    SELECT 1 FROM usage_logs u WHERE u.request_id='creative_settle:' || r.run_id
		      AND u.api_key_id=r.api_key_id AND u.user_id=r.user_id))
		RETURNING r.id`, runID)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	recorded := rows.Next()
	return recorded, rows.Err()
}

var _ service.CreativeUsageRecoveryRepository = (*creativeRunRepository)(nil)
