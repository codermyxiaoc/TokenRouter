package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	opsCleanupDefaultSchedule   = "0 3 * * *"
	opsCleanupDefaultBatchSize  = 1000
	opsCleanupDefaultBatchPause = 200 * time.Millisecond
	opsCleanupBatchTimeout      = 15 * time.Second
	opsCleanupCronStopTimeout   = 3 * time.Second
	opsCleanupRunTimeout        = 30 * time.Minute
	opsCleanupHeartbeatTimeout  = 2 * time.Second
)

type opsCleanupTarget struct {
	retentionDays int
	table         string
	timeCol       string
	castDate      bool
	counter       *int64
}

type opsCleanupDeletedCounts struct {
	errorLogs      int64
	ingressRejects int64
	alertEvents    int64
	systemLogs     int64
	logAudits      int64
	systemMetrics  int64
	hourlyPreagg   int64
	dailyPreagg    int64
	batches        int64
	throttled      time.Duration
}

func (c opsCleanupDeletedCounts) String() string {
	return fmt.Sprintf(
		"error_logs=%d ingress_rejects=%d alert_events=%d system_logs=%d log_audits=%d system_metrics=%d hourly_preagg=%d daily_preagg=%d batches=%d throttled_ms=%d",
		c.errorLogs,
		c.ingressRejects,
		c.alertEvents,
		c.systemLogs,
		c.logAudits,
		c.systemMetrics,
		c.hourlyPreagg,
		c.dailyPreagg,
		c.batches,
		c.throttled.Milliseconds(),
	)
}

type opsCleanupTargetResult struct {
	deleted   int64
	batches   int64
	throttled time.Duration
}

// opsCleanupPlan 把"保留天数"翻译成具体的清理动作。
//   - days < 0  → 跳过该项清理（ok=false），保留兼容老数据
//   - days == 0 → TRUNCATE TABLE（O(1) 全清），truncate=true
//   - days > 0  → 批量 DELETE 早于 now-N天 的行，cutoff = now - N 天
func opsCleanupPlan(now time.Time, days int) (cutoff time.Time, truncate, ok bool) {
	if days < 0 {
		return time.Time{}, false, false
	}
	if days == 0 {
		return time.Time{}, true, true
	}
	return now.AddDate(0, 0, -days), false, true
}

func opsCleanupRunOne(
	ctx context.Context,
	db *sql.DB,
	truncate bool,
	cutoff time.Time,
	table, timeCol string,
	castDate bool,
	batchSize int,
	batchPause time.Duration,
) (opsCleanupTargetResult, error) {
	if truncate {
		deleted, err := truncateOpsTable(ctx, db, table)
		result := opsCleanupTargetResult{deleted: deleted}
		if deleted > 0 {
			result.batches = 1
		}
		return result, err
	}
	return deleteOldRowsByCTID(ctx, db, table, timeCol, cutoff, batchSize, batchPause, castDate)
}

// deleteOldRowsByCTID 按时间顺序选取物理行并分批删除，避免为每批旧数据额外按主键排序。
func deleteOldRowsByCTID(
	ctx context.Context,
	db *sql.DB,
	table string,
	timeColumn string,
	cutoff time.Time,
	batchSize int,
	batchPause time.Duration,
	castCutoffToDate bool,
) (opsCleanupTargetResult, error) {
	result := opsCleanupTargetResult{}
	if db == nil {
		return result, nil
	}
	if batchSize <= 0 {
		batchSize = opsCleanupDefaultBatchSize
	}
	if batchPause <= 0 {
		batchPause = opsCleanupDefaultBatchPause
	}

	where := fmt.Sprintf("%s < $1", timeColumn)
	if castCutoffToDate {
		where = fmt.Sprintf("%s < $1::date", timeColumn)
	}

	q := fmt.Sprintf(`
WITH batch AS MATERIALIZED (
	  SELECT ctid AS row_ctid FROM %s
	  WHERE %s
	  ORDER BY %s ASC, id ASC
	  LIMIT $2
)
DELETE FROM %s AS target
USING batch
WHERE target.ctid = batch.row_ctid
`, table, where, timeColumn, table)

	for {
		batchCtx, cancel := context.WithTimeout(ctx, opsCleanupBatchTimeout)
		res, err := db.ExecContext(batchCtx, q, cutoff, batchSize)
		cancel()
		if err != nil {
			if isMissingRelationError(err) {
				return result, nil
			}
			return result, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return result, err
		}
		result.deleted += affected
		if affected == 0 {
			break
		}
		result.batches++
		if affected < int64(batchSize) {
			break
		}
		pauseStarted := time.Now()
		if err := sleepOpsCleanupWithContext(ctx, batchPause); err != nil {
			return result, err
		}
		result.throttled += time.Since(pauseStarted)
	}
	return result, nil
}

// sleepOpsCleanupWithContext 在节流等待期间响应任务取消，避免停机时无条件阻塞。
func sleepOpsCleanupWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// truncateOpsTable 用 TRUNCATE TABLE 清空指定表，并从统计信息读取近似行数用于 heartbeat。
// 这里不能执行 COUNT(*)，否则大表会在清空前再次触发全表扫描和文件页缓存压力。
func truncateOpsTable(ctx context.Context, db *sql.DB, table string) (int64, error) {
	if db == nil {
		return 0, nil
	}

	truncateCtx, cancel := context.WithTimeout(ctx, opsCleanupBatchTimeout)
	defer cancel()

	var estimatedRows int64
	const estimateQuery = `
		SELECT COALESCE((
			SELECT GREATEST(reltuples, 0)::bigint
			FROM pg_class
			WHERE oid = to_regclass($1)
		), 0)`
	if err := db.QueryRowContext(truncateCtx, estimateQuery, table).Scan(&estimatedRows); err != nil {
		return 0, fmt.Errorf("estimate %s rows: %w", table, err)
	}
	if _, err := db.ExecContext(truncateCtx, fmt.Sprintf("TRUNCATE TABLE %s", table)); err != nil {
		if isMissingRelationError(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("truncate %s: %w", table, err)
	}
	return estimatedRows, nil
}

func isMissingRelationError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "does not exist") && strings.Contains(s, "relation")
}
