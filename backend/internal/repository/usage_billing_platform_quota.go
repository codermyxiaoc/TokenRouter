package repository

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

// applyUsageBillingPlatformQuota 与资金和请求去重同事务提交，只累计真实余额消费。
// 已完成消费超过平台限额仍应入账，限额准入由请求前检查负责；独立 Video 另有预留账本。
// @project-doc docs/domains/platform_quotas.md#platform_quota_settlement_and_flush
func applyUsageBillingPlatformQuota(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand, amount float64) error {
	if cmd == nil || cmd.UserID <= 0 || amount == 0 {
		return nil
	}
	if amount < 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return errors.New("平台额度结算金额无效")
	}
	platform, err := usageBillingQuotaPlatform(ctx, tx, cmd)
	if err != nil {
		return err
	}
	if platform == "" || platform == service.PlatformVideo {
		return nil
	}
	return incrementUsageBillingPlatformQuota(ctx, tx, cmd.UserID, platform, amount, time.Time{})
}

// checkUsageBillingPlatformQuotaReserve 仅对新预占的真实余额部分检查额度，失败随资金分配一起回滚。
// 此处不预占平台额度，也不按本次预计金额提前拒绝；已受理任务完成时必须正常结算。
func checkUsageBillingPlatformQuotaReserve(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand, amount float64) error {
	if amount <= 0 {
		return nil
	}
	platform, err := usageBillingQuotaPlatform(ctx, tx, cmd)
	if err != nil || platform == "" || platform == service.PlatformVideo {
		return err
	}
	var dailyLimit, weeklyLimit, monthlyLimit sql.NullFloat64
	var dailyUsage, weeklyUsage, monthlyUsage float64
	var dailyStart, weeklyStart, monthlyStart sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT daily_limit_usd,weekly_limit_usd,monthly_limit_usd,
 daily_usage_usd,weekly_usage_usd,monthly_usage_usd,daily_window_start,weekly_window_start,monthly_window_start
 FROM user_platform_quotas WHERE user_id=$1 AND platform=$2 AND deleted_at IS NULL
 AND (daily_limit_usd IS NOT NULL OR weekly_limit_usd IS NOT NULL OR monthly_limit_usd IS NOT NULL) FOR UPDATE`, cmd.UserID, platform).
		Scan(&dailyLimit, &weeklyLimit, &monthlyLimit, &dailyUsage, &weeklyUsage, &monthlyUsage, &dailyStart, &weeklyStart, &monthlyStart)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// 等待管理操作的行锁后再取时钟，避免跨窗口后仍按旧窗口拒绝。
	var now time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	dayStart, weekStart := timezone.StartOfDay(now), timezone.StartOfWeek(now)
	if !dailyStart.Valid || dailyStart.Time.Before(dayStart) {
		dailyUsage = 0
	}
	if !weeklyStart.Valid || weeklyStart.Time.Before(weekStart) {
		weeklyUsage = 0
	}
	monthReset := now.Add(30 * 24 * time.Hour)
	if !monthlyStart.Valid || now.Sub(monthlyStart.Time) >= 30*24*time.Hour {
		monthlyUsage = 0
	} else {
		monthReset = monthlyStart.Time.Add(30 * 24 * time.Hour)
	}
	if dailyLimit.Valid && dailyUsage >= dailyLimit.Float64 {
		return service.ErrUserPlatformDailyQuotaExhausted.WithMetadata(map[string]string{"window_resets_at": dayStart.AddDate(0, 0, 1).Format(time.RFC3339)})
	}
	if weeklyLimit.Valid && weeklyUsage >= weeklyLimit.Float64 {
		return service.ErrUserPlatformWeeklyQuotaExhausted.WithMetadata(map[string]string{"window_resets_at": weekStart.AddDate(0, 0, 7).Format(time.RFC3339)})
	}
	if monthlyLimit.Valid && monthlyUsage >= monthlyLimit.Float64 {
		return service.ErrUserPlatformMonthlyQuotaExhausted.WithMetadata(map[string]string{"window_resets_at": monthReset.Format(time.RFC3339)})
	}
	return nil
}

// usageBillingQuotaPlatform 优先使用请求冻结的平台；旧命令仅从原任务或原分组恢复，禁止改读当前 Key。
func usageBillingQuotaPlatform(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) (string, error) {
	platform := strings.TrimSpace(cmd.QuotaPlatform)
	if platform == "" && cmd.LegacyVideoTaskID > 0 {
		var source string
		err := tx.QueryRowContext(ctx, `SELECT source,COALESCE(pending#>>'{seedance_billing,quota_platform}','')
 FROM legacy_video_tasks WHERE id=$1 AND user_id=$2 AND billing_user_id=$3 AND api_key_id=$4 AND account_id=$5 AND group_id=$6 AND request_id=$7`,
			cmd.LegacyVideoTaskID, cmd.ActorUserID, cmd.UserID, cmd.APIKeyID, cmd.AccountID, cmd.GroupID, cmd.RequestID).Scan(&source, &platform)
		if err != nil {
			return "", err
		}
		platform = strings.TrimSpace(platform)
		if platform == "" {
			switch source {
			case "grok_video":
				platform = service.PlatformGrok
			case "seedance_video":
				platform = service.PlatformOpenAI
			default:
				return "", errors.New("旧视频任务平台归属无效")
			}
		}
	}
	if platform == "" && cmd.GroupID != nil && *cmd.GroupID > 0 {
		// 软删除不改变已受理请求的原平台；不存在的旧内部分组不能被猜测成其它平台。
		err := tx.QueryRowContext(ctx, `SELECT platform FROM groups WHERE id=$1`, *cmd.GroupID).Scan(&platform)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		platform = strings.TrimSpace(platform)
	}
	if platform != "" && !service.IsAllowedQuotaPlatform(platform) {
		return "", errors.New("平台额度归属无效")
	}
	return platform, nil
}

// 先获得额度行锁再读取数据库时钟，避免排队跨日时使用旧时间回退新窗口。
// 三个窗口和管理员重置串行更新；自然过期不推进手动重置代次。
// 缺失、软删除和无限额行都不建立新统计，避免把取消限额误当作重新启用。
func incrementUsageBillingPlatformQuota(ctx context.Context, tx *sql.Tx, userID int64, platform string, amount float64, now time.Time) error {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM user_platform_quotas WHERE user_id=$1 AND platform=$2 AND deleted_at IS NULL
 AND (daily_limit_usd IS NOT NULL OR weekly_limit_usd IS NOT NULL OR monthly_limit_usd IS NOT NULL) FOR UPDATE`, userID, platform).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// 非零时钟只供确定性窗口测试；生产调用必须在取得行锁后取当前时间。
	if now.IsZero() {
		if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
	}
	now = now.UTC().Truncate(time.Microsecond)
	_, err = tx.ExecContext(ctx, `UPDATE user_platform_quotas SET
 daily_usage_usd=CASE WHEN daily_window_start IS DISTINCT FROM $4::timestamptz THEN $3::numeric ELSE daily_usage_usd+$3::numeric END,
 weekly_usage_usd=CASE WHEN weekly_window_start IS DISTINCT FROM $5::timestamptz THEN $3::numeric ELSE weekly_usage_usd+$3::numeric END,
 monthly_usage_usd=CASE WHEN monthly_window_start IS NULL OR monthly_window_start<=$6::timestamptz-INTERVAL '720 hours' THEN $3::numeric ELSE monthly_usage_usd+$3::numeric END,
 daily_window_start=$4,weekly_window_start=$5,
 monthly_window_start=CASE WHEN monthly_window_start IS NULL OR monthly_window_start<=$6::timestamptz-INTERVAL '720 hours' THEN $6 ELSE monthly_window_start END,
 updated_at=$6
 WHERE user_id=$1 AND platform=$2 AND deleted_at IS NULL
 AND (daily_limit_usd IS NOT NULL OR weekly_limit_usd IS NOT NULL OR monthly_limit_usd IS NOT NULL)`,
		userID, platform, amount, timezone.StartOfDay(now), timezone.StartOfWeek(now), now)
	return err
}
