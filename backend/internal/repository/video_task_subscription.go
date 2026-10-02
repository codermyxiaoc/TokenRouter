package repository

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

// 视频任务可能跨多个额度周期，只能退回确实预占过且仍未切换的原窗口。
type videoSubscriptionWindow struct {
	Daily, Weekly, Monthly                               sql.NullTime
	HasDaily, HasWeekly, HasMonthly                      bool
	DailyGeneration, WeeklyGeneration, MonthlyGeneration int64
}

func saveVideoSubscriptionWindows(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand) error {
	windows := map[int64]videoSubscriptionWindow{}
	// 预留分配由资金函数落库，不依赖其是否回写调用方命令。
	var allocationJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT subscription_hold_allocations FROM video_tasks WHERE id=$1`, cmd.BatchID).Scan(&allocationJSON); err != nil {
		return err
	}
	var allocations []domain.BillingAllocation
	if err := json.Unmarshal(allocationJSON, &allocations); err != nil {
		return err
	}
	for _, a := range allocations {
		if a.SubscriptionID == nil || a.Type != domain.BillingAllocationTypeSubscription || a.AmountUSD <= 0 {
			continue
		}
		var w videoSubscriptionWindow
		err := tx.QueryRowContext(ctx, `SELECT daily_window_start,weekly_window_start,monthly_window_start,
 COALESCE(daily_limit_usd,0)>0,COALESCE(weekly_limit_usd,0)>0,COALESCE(monthly_limit_usd,0)>0,
 daily_reset_generation,weekly_reset_generation,monthly_reset_generation
 FROM user_subscriptions WHERE id=$1 AND user_id=$2`, *a.SubscriptionID, cmd.UserID).
			Scan(&w.Daily, &w.Weekly, &w.Monthly, &w.HasDaily, &w.HasWeekly, &w.HasMonthly, &w.DailyGeneration, &w.WeeklyGeneration, &w.MonthlyGeneration)
		if err != nil {
			return err
		}
		windows[*a.SubscriptionID] = w
	}
	raw, err := json.Marshal(windows)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE video_tasks SET subscription_window_holds=$2::jsonb WHERE id=$1`, cmd.BatchID, string(raw))
	return err
}

func releaseVideoSubscriptionAllocations(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand, allocations []domain.BillingAllocation) error {
	if len(allocations) == 0 {
		return nil
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT subscription_window_holds FROM video_tasks WHERE id=$1`, cmd.BatchID).Scan(&raw); err != nil {
		return err
	}
	var windows map[int64]videoSubscriptionWindow
	if err := json.Unmarshal(raw, &windows); err != nil {
		return err
	}
	for _, a := range allocations {
		if a.SubscriptionID == nil || a.Type != domain.BillingAllocationTypeSubscription || a.AmountUSD <= 0 {
			continue
		}
		w, ok := windows[*a.SubscriptionID]
		if !ok {
			return service.ErrVideoTaskConflict
		}
		res, err := tx.ExecContext(ctx, `UPDATE user_subscriptions SET
 daily_usage_usd=CASE WHEN $4 AND daily_window_start=$5 AND daily_reset_generation=$10 THEN GREATEST(0,daily_usage_usd-$1) ELSE daily_usage_usd END,
 weekly_usage_usd=CASE WHEN $6 AND weekly_window_start=$7 AND weekly_reset_generation=$11 THEN GREATEST(0,weekly_usage_usd-$1) ELSE weekly_usage_usd END,
 monthly_usage_usd=CASE WHEN $8 AND monthly_window_start=$9 AND monthly_reset_generation=$12 THEN GREATEST(0,monthly_usage_usd-$1) ELSE monthly_usage_usd END,
 updated_at=NOW() WHERE id=$2 AND user_id=$3`, a.AmountUSD, *a.SubscriptionID, cmd.UserID, w.HasDaily, w.Daily, w.HasWeekly, w.Weekly, w.HasMonthly, w.Monthly, w.DailyGeneration, w.WeeklyGeneration, w.MonthlyGeneration)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return service.ErrSubscriptionNotFound
		}
	}
	return nil
}
