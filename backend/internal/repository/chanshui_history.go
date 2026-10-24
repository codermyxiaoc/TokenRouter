package repository

import (
	"context"
	"encoding/json"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/lib/pq"
)

// ListRecentRuns 按配置索引一次查询各自最新 60 条，不读取独立报告与恢复凭据列。
func (r *chanshuiRepository) ListRecentRuns(ctx context.Context, ids []int64) (map[int64][]service.ChanshuiRun, error) {
	out := make(map[int64][]service.ChanshuiRun, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT recent.config_id,recent.record-'report',recent.status
FROM (SELECT DISTINCT unnest($1::bigint[]) AS config_id) requested
CROSS JOIN LATERAL (
    SELECT config_id,record,status,created_at,id
    FROM chanshui_test_runs
    WHERE config_id=requested.config_id
    ORDER BY created_at DESC,id DESC
    LIMIT 60
) recent
ORDER BY recent.config_id,recent.created_at DESC,recent.id DESC`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var configID int64
		var raw []byte
		var status string
		if err := rows.Scan(&configID, &raw, &status); err != nil {
			return nil, err
		}
		var run service.ChanshuiRun
		if err := json.Unmarshal(raw, &run); err != nil {
			return nil, err
		}
		// 归属与执行状态以持久列为准，避免旧摘要覆盖当前状态。
		run.ConfigID = configID
		run.Status = status
		out[configID] = append(out[configID], run)
	}
	return out, rows.Err()
}
