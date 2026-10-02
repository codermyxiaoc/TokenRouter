package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/shopspring/decimal"
)

// videoPlatformQuotaHold 保存额度行身份和原窗口；管理员重置或重建额度后不能回退新窗口。
type videoPlatformQuotaHold struct {
	ID                                                   int64
	Amount                                               float64
	Daily, Weekly, Monthly                               time.Time
	DailyGeneration, WeeklyGeneration, MonthlyGeneration int64
}

// applyVideoPlatformQuota 与冻结余额处于同一事务，只统计余额分配，订阅分配不占平台额度。
// video 的额度以数据库为准，不使用普通请求的 Redis 增量与异步快照覆盖。
func applyVideoPlatformQuota(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand, op batchImageAllowanceOperation, result *service.BatchImageBalanceHoldResult) error {
	if op != batchImageAllowanceReserve {
		var raw []byte
		if err := tx.QueryRowContext(ctx, `SELECT platform_quota_hold FROM video_tasks WHERE id=$1`, cmd.BatchID).Scan(&raw); err != nil {
			return err
		}
		if len(raw) == 0 {
			return nil
		}
		var hold videoPlatformQuotaHold
		if err := json.Unmarshal(raw, &hold); err != nil {
			return err
		}
		actual := 0.0
		if op == batchImageAllowanceCapture {
			actual = result.BalanceAmountUSD
		}
		adjustment := service.QuantizeUsageBillingAmount(hold.Amount - actual)
		if adjustment <= 0 {
			return nil
		}
		_, err := tx.ExecContext(ctx, `UPDATE user_platform_quotas SET
 daily_usage_usd=CASE WHEN daily_window_start=$3 AND daily_reset_generation=$7 THEN GREATEST(0,daily_usage_usd-$2) ELSE daily_usage_usd END,
 weekly_usage_usd=CASE WHEN weekly_window_start=$4 AND weekly_reset_generation=$8 THEN GREATEST(0,weekly_usage_usd-$2) ELSE weekly_usage_usd END,
 monthly_usage_usd=CASE WHEN monthly_window_start=$5 AND monthly_reset_generation=$9 THEN GREATEST(0,monthly_usage_usd-$2) ELSE monthly_usage_usd END,
 updated_at=NOW() WHERE id=$1 AND user_id=$6 AND platform='video' AND deleted_at IS NULL`, hold.ID, adjustment, hold.Daily, hold.Weekly, hold.Monthly, cmd.UserID, hold.DailyGeneration, hold.WeeklyGeneration, hold.MonthlyGeneration)
		return err
	}
	amount := service.QuantizeUsageBillingAmount(result.BalanceAmountUSD)
	if amount <= 0 {
		return nil
	}
	var id int64
	var limits [3]sql.NullFloat64
	var usage [3]float64
	var starts [3]sql.NullTime
	var generations [3]int64
	err := tx.QueryRowContext(ctx, `SELECT id,daily_limit_usd,weekly_limit_usd,monthly_limit_usd,daily_usage_usd,weekly_usage_usd,monthly_usage_usd,daily_window_start,weekly_window_start,monthly_window_start,daily_reset_generation,weekly_reset_generation,monthly_reset_generation
 FROM user_platform_quotas WHERE user_id=$1 AND platform='video' AND deleted_at IS NULL FOR UPDATE`, cmd.UserID).
		Scan(&id, &limits[0], &limits[1], &limits[2], &usage[0], &usage[1], &usage[2], &starts[0], &starts[1], &starts[2], &generations[0], &generations[1], &generations[2])
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// PostgreSQL 以微秒保存时间，快照必须使用相同精度才能精确匹配。
	now := time.Now().UTC().Truncate(time.Microsecond)
	windows := [3]time.Time{timezone.StartOfDay(now), timezone.StartOfWeek(now), now}
	quotaErrors := [3]error{service.ErrUserPlatformDailyQuotaExhausted, service.ErrUserPlatformWeeklyQuotaExhausted, service.ErrUserPlatformMonthlyQuotaExhausted}
	for i := range usage {
		if i < 2 {
			if !starts[i].Valid || !starts[i].Time.Equal(windows[i]) {
				usage[i] = 0
			}
		} else if starts[i].Valid && now.Sub(starts[i].Time) < 30*24*time.Hour {
			windows[i] = starts[i].Time
		} else {
			usage[i] = 0
		}
		value := decimal.NewFromFloat(usage[i]).Add(decimal.NewFromFloat(amount))
		if limits[i].Valid && value.GreaterThan(decimal.NewFromFloat(limits[i].Float64)) {
			return quotaErrors[i]
		}
		usage[i], _ = value.Float64()
	}
	if _, err = tx.ExecContext(ctx, `UPDATE user_platform_quotas SET daily_usage_usd=$2,weekly_usage_usd=$3,monthly_usage_usd=$4,daily_window_start=$5,weekly_window_start=$6,monthly_window_start=$7,updated_at=NOW() WHERE id=$1`, id, usage[0], usage[1], usage[2], windows[0], windows[1], windows[2]); err != nil {
		return err
	}
	raw, err := json.Marshal(videoPlatformQuotaHold{ID: id, Amount: amount, Daily: windows[0], Weekly: windows[1], Monthly: windows[2], DailyGeneration: generations[0], WeeklyGeneration: generations[1], MonthlyGeneration: generations[2]})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE video_tasks SET platform_quota_hold=$2::jsonb WHERE id=$1`, cmd.BatchID, string(raw))
	return err
}
