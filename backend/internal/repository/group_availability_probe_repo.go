package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

type groupAvailabilityProbeRepository struct {
	db *sql.DB
}

func NewGroupAvailabilityProbeRepository(db *sql.DB) service.GroupAvailabilityProbeRepository {
	return &groupAvailabilityProbeRepository{db: db}
}

// ClaimGroup 原子保存探测配置并领取租约，失败请求不会覆盖获胜请求或其它分组设置。
func (r *groupAvailabilityProbeRepository) ClaimGroup(ctx context.Context, groupID int64, config service.GroupAvailabilityProbeConfig, now time.Time, lockUntil time.Time, _ string) (*service.GroupAvailabilityProbeDueGroup, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("availability probe database is not configured")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var group service.GroupAvailabilityProbeDueGroup
	// 锁定平台快照，同时允许外键检查；不把网络探测放入数据库事务。
	err = tx.QueryRowContext(ctx, `SELECT id, name, platform FROM groups
		WHERE id = $1 AND deleted_at IS NULL AND status = 'active'
		FOR NO KEY UPDATE`, groupID).Scan(&group.GroupID, &group.Name, &group.Platform)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !config.Enabled {
		return nil, infraerrors.BadRequest("INVALID_AVAILABILITY_PROBE_CONFIG", "Enable availability probing before testing")
	}
	if err := service.ValidateGroupAvailabilityProbeProtocol(group.Platform, config.Protocol); err != nil {
		return nil, infraerrors.BadRequest("INVALID_AVAILABILITY_PROBE_CONFIG", err.Error())
	}
	// 新开启的分组可能尚未被 cron 扫描；只补建状态，绝不覆盖正在运行的租约。
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO group_availability_probe_states (group_id, next_run_at, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (group_id) DO NOTHING
	`, groupID, now); err != nil {
		return nil, err
	}
	var claimedID int64
	// 每次领取独立代次；同一实例重启或租约过期后的旧结果不能释放后续任务。
	group.LeaseToken = uuid.NewString()
	err = tx.QueryRowContext(ctx, `
		UPDATE group_availability_probe_states
		SET locked_until = $3, locked_by = $4, updated_at = NOW()
		WHERE group_id = $1 AND (locked_until IS NULL OR locked_until <= $2)
		RETURNING group_id
	`, groupID, now, lockUntil, group.LeaseToken).Scan(&claimedID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rawConfig, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	// 只更新探测 JSON，避免管理服务整行写回覆盖并发修改的倍率、定价或路由。
	if _, err = tx.ExecContext(ctx, `UPDATE groups SET availability_probe_config=$2::jsonb, updated_at=NOW() WHERE id=$1`, groupID, string(rawConfig)); err != nil {
		return nil, err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventGroupChanged, nil, &groupID, nil); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	group.Config = config
	return &group, nil
}

func (r *groupAvailabilityProbeRepository) ClaimDue(ctx context.Context, now time.Time, lockUntil time.Time, _ string, limit int) ([]service.GroupAvailabilityProbeDueGroup, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	// 先将启用探测的分组补进状态表；分组被禁用时再清掉状态，避免长期扫描无效记录。
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO group_availability_probe_states (group_id, next_run_at, created_at, updated_at)
		SELECT id, $1, NOW(), NOW()
		FROM groups
		WHERE deleted_at IS NULL
		  AND status = 'active'
		  AND availability_probe_config @> '{"enabled": true}'::jsonb
		ON CONFLICT (group_id) DO NOTHING
	`, now); err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM group_availability_probe_states s
		WHERE (s.locked_until IS NULL OR s.locked_until <= $1) AND NOT EXISTS (
			SELECT 1
			FROM groups g
			WHERE g.id = s.group_id
			  AND g.deleted_at IS NULL
			  AND g.status = 'active'
			  AND g.availability_probe_config @> '{"enabled": true}'::jsonb
		)
	`, now); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, `
			SELECT g.id, g.name, g.platform, g.availability_probe_config
			FROM group_availability_probe_states s
			JOIN groups g ON g.id = s.group_id
			WHERE g.deleted_at IS NULL
			  AND g.status = 'active'
			  AND g.availability_probe_config @> '{"enabled": true}'::jsonb
			  AND (s.next_run_at IS NULL OR s.next_run_at <= $1)
			  AND (s.locked_until IS NULL OR s.locked_until <= $1)
			ORDER BY COALESCE(s.next_run_at, 'epoch'::timestamptz), s.group_id
			LIMIT $2
			FOR UPDATE OF s SKIP LOCKED
	`, now, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	dueGroups := make([]service.GroupAvailabilityProbeDueGroup, 0)
	for rows.Next() {
		var item service.GroupAvailabilityProbeDueGroup
		var rawConfig []byte
		if err := rows.Scan(&item.GroupID, &item.Name, &item.Platform, &rawConfig); err != nil {
			return nil, err
		}
		if len(rawConfig) > 0 {
			if err := json.Unmarshal(rawConfig, &item.Config); err != nil {
				return nil, fmt.Errorf("decode availability probe config: %w", err)
			}
		}
		item.LeaseToken = uuid.NewString()
		dueGroups = append(dueGroups, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// 保持同一事务的行锁，按各分组配置领取；不能将短探测统一锁住二十五分钟。
	for _, group := range dueGroups {
		groupLockUntil := now.Add(service.GroupAvailabilityProbeLeaseDuration(group.Config, false))
		if lockUntil.Before(groupLockUntil) {
			groupLockUntil = lockUntil
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE group_availability_probe_states
			SET locked_until=$2, locked_by=$3, updated_at=NOW() WHERE group_id=$1
		`, group.GroupID, groupLockUntil, group.LeaseToken); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dueGroups, nil
}

func (r *groupAvailabilityProbeRepository) SaveResultAndScheduleNext(ctx context.Context, result *service.GroupAvailabilityProbeResult, nextRunAt time.Time) error {
	if r == nil || r.db == nil || result == nil {
		return nil
	}
	if result.LeaseToken == "" {
		return service.ErrGroupAvailabilityProbeLeaseLost
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	// 先原子核对领取代次再更新状态；不匹配时连历史观测也不写入，避免旧结果污染可用率。
	updated, err := tx.ExecContext(ctx, `
		UPDATE group_availability_probe_states SET
			next_run_at=$2, locked_until=NULL, locked_by=NULL,
			last_status=$3, last_success=$4, last_latency_ms=$5,
			last_error=$6, last_checked_at=$7, updated_at=NOW()
		WHERE group_id=$1 AND locked_by=$8
	`, result.GroupID, nextRunAt, result.Status, result.Success, result.LatencyMs,
		nullableProbeString(result.ErrorMessage), result.FinishedAt, result.LeaseToken)
	if err != nil {
		return err
	}
	affected, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return service.ErrGroupAvailabilityProbeLeaseLost
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO group_availability_probe_results (
			group_id, account_id, model_id, status, success, latency_ms,
			error_message, started_at, finished_at, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
	`, result.GroupID, result.AccountID, result.ModelID, result.Status, result.Success, result.LatencyMs, nullableProbeString(result.ErrorMessage), result.StartedAt, result.FinishedAt); err != nil {
		return err
	}

	return tx.Commit()
}

func (r *groupAvailabilityProbeRepository) GetSummaryByGroupIDs(ctx context.Context, groupIDs []int64, days int, bucketMinutes int, timezoneName string, now time.Time) (map[int64]*service.GroupAvailabilitySummary, error) {
	out := make(map[int64]*service.GroupAvailabilitySummary, len(groupIDs))
	if r == nil || r.db == nil || len(groupIDs) == 0 {
		return out, nil
	}
	days, bucketMinutes = service.NormalizeMarketplaceAvailabilityWindow(days, bucketMinutes)

	loc := time.UTC
	if timezoneName != "" {
		if parsed, err := time.LoadLocation(timezoneName); err == nil && parsed != nil {
			loc = parsed
		}
	}
	localNow := now.In(loc)
	bucketDuration := time.Duration(bucketMinutes) * time.Minute
	endLocal := nextLocalBucketBoundary(localNow, bucketDuration)
	totalMinutes := days * 24 * 60
	bucketCount := (totalMinutes + bucketMinutes - 1) / bucketMinutes
	if bucketCount <= 0 {
		bucketCount = 1
	}
	startLocal := endLocal.Add(-time.Duration(bucketCount) * bucketDuration)
	startUTC := startLocal.UTC()
	endUTC := endLocal.UTC()

	for _, groupID := range groupIDs {
		summary := &service.GroupAvailabilitySummary{
			WindowDays:    days,
			BucketMinutes: bucketMinutes,
			Days:          make([]service.GroupAvailabilityBucket, 0, bucketCount),
		}
		for i := 0; i < bucketCount; i++ {
			bucketStart := startLocal.Add(time.Duration(i) * bucketDuration)
			summary.Days = append(summary.Days, service.GroupAvailabilityBucket{
				Date: bucketStart.Format(time.RFC3339),
			})
		}
		out[groupID] = summary
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT
			group_id,
			FLOOR((EXTRACT(EPOCH FROM started_at) - EXTRACT(EPOCH FROM $2::timestamptz)) / ($4::double precision * 60))::int AS bucket_index,
			COUNT(*) FILTER (WHERE success = true) AS success_count,
			COUNT(*) AS total_count
		FROM group_availability_probe_results
		WHERE group_id = ANY($1)
		  AND started_at >= $2
		  AND started_at < $3
		GROUP BY group_id, bucket_index
	`, pq.Array(groupIDs), startUTC, endUTC, bucketMinutes)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var groupID int64
		var bucketIndex int
		var successCount int64
		var totalCount int64
		if err := rows.Scan(&groupID, &bucketIndex, &successCount, &totalCount); err != nil {
			return nil, err
		}
		summary, ok := out[groupID]
		if !ok {
			continue
		}
		if bucketIndex < 0 || bucketIndex >= len(summary.Days) {
			continue
		}
		rate := availabilityRate(successCount, totalCount)
		summary.Days[bucketIndex].SuccessCount = successCount
		summary.Days[bucketIndex].TotalCount = totalCount
		summary.Days[bucketIndex].AvailabilityRate = rate
		summary.SuccessCount += successCount
		summary.TotalCount += totalCount
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, summary := range out {
		summary.AvailabilityRate = availabilityRate(summary.SuccessCount, summary.TotalCount)
	}

	lastRows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT ON (group_id)
			group_id, status, finished_at
		FROM group_availability_probe_results
		WHERE group_id = ANY($1)
		ORDER BY group_id, started_at DESC, id DESC
	`, pq.Array(groupIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = lastRows.Close() }()

	for lastRows.Next() {
		var groupID int64
		var status string
		var checkedAt time.Time
		if err := lastRows.Scan(&groupID, &status, &checkedAt); err != nil {
			return nil, err
		}
		if summary, ok := out[groupID]; ok {
			summary.LastStatus = status
			t := checkedAt
			summary.LastCheckedAt = &t
		}
	}
	if err := lastRows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

func nextLocalBucketBoundary(t time.Time, bucketDuration time.Duration) time.Time {
	if bucketDuration <= 0 {
		bucketDuration = 24 * time.Hour
	}
	// 以本地自然日零点对齐，避免非 UTC 时区下使用 time.Truncate 产生偏移桶。
	dayStart := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	bucketStart := dayStart.Add(t.Sub(dayStart) / bucketDuration * bucketDuration)
	if bucketStart.Before(t) {
		return bucketStart.Add(bucketDuration)
	}
	return bucketStart
}

func (r *groupAvailabilityProbeRepository) CleanupOldResults(ctx context.Context, before time.Time) error {
	if r == nil || r.db == nil {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM group_availability_probe_results
		WHERE created_at < $1
	`, before)
	return err
}

func availabilityRate(successCount int64, totalCount int64) *float64 {
	if totalCount <= 0 {
		return nil
	}
	value := float64(successCount) / float64(totalCount)
	return &value
}

func nullableProbeString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
