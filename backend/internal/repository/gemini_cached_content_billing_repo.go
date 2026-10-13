package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

// checkGeminiCacheReservationAllowance 只扩展显式缓存固定预留，不改变原图片任务的额度口径。
// 调用方已锁付款用户，同一事务内把尚未捕获的缓存预留计入密钥和成员预算。
func checkGeminiCacheReservationAllowance(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand, quote json.RawMessage) error {
	var meta struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(quote, &meta) != nil || meta.Kind != "gemini_cache" {
		return nil
	}
	var status string
	var disabled bool
	var quota, used, l5, l1, l7, u5, u1, u7 float64
	var s5, s1, s7, expires sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT status, team_owner_disabled, quota, quota_used, rate_limit_5h, rate_limit_1d, rate_limit_7d, usage_5h, usage_1d, usage_7d, window_5h_start, window_1d_start, window_7d_start, expires_at FROM api_keys WHERE id=$1 AND deleted_at IS NULL FOR NO KEY UPDATE`, cmd.APIKeyID).Scan(&status, &disabled, &quota, &used, &l5, &l1, &l7, &u5, &u1, &u7, &s5, &s1, &s7, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrAPIKeyNotFound
	}
	if err != nil {
		return err
	}
	if status == service.StatusAPIKeyQuotaExhausted {
		return service.ErrAPIKeyQuotaExhausted
	}
	now := time.Now()
	if disabled || status != service.StatusAPIKeyActive || (expires.Valid && !expires.Time.After(now)) {
		return service.ErrAPIKeyNotFound
	}
	var pending float64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM((snapshot->>'HoldAmount')::numeric),0) FROM image_billing_reservations WHERE api_key_id=$1 AND state IN ('reserved','reconciliation') AND quote->>'kind'='gemini_cache'`, cmd.APIKeyID).Scan(&pending); err != nil {
		return err
	}
	amount := pending + cmd.HoldAmount
	if quota > 0 && used+amount > quota+1e-10 {
		return service.ErrAPIKeyQuotaExhausted
	}
	if l5 > 0 && effectiveSQLWindowUsage(u5, s5, service.RateLimitWindow5h, now)+amount > l5+1e-10 {
		return service.ErrAPIKeyRateLimit5hExceeded
	}
	if l1 > 0 && effectiveSQLWindowUsage(u1, s1, service.RateLimitWindow1d, now)+amount > l1+1e-10 {
		return service.ErrAPIKeyRateLimit1dExceeded
	}
	if l7 > 0 && effectiveSQLWindowUsage(u7, s7, service.RateLimitWindow7d, now)+amount > l7+1e-10 {
		return service.ErrAPIKeyRateLimit7dExceeded
	}
	if cmd.TeamID == nil || cmd.ActorUserID == cmd.UserID {
		return nil
	}
	var role string
	var dl, wl, ml, du, wu, mu float64
	var ds, ws, ms sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT role,daily_limit_usd,weekly_limit_usd,monthly_limit_usd,daily_usage_usd,weekly_usage_usd,monthly_usage_usd,daily_window_start,weekly_window_start,monthly_window_start FROM team_memberships WHERE team_id=$1 AND user_id=$2 AND left_at IS NULL FOR NO KEY UPDATE`, *cmd.TeamID, cmd.ActorUserID).Scan(&role, &dl, &wl, &ml, &du, &wu, &mu, &ds, &ws, &ms)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrTeamMembershipRequired
	}
	if err != nil {
		return err
	}
	if role == service.TeamRoleOwner {
		return nil
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM((snapshot->>'HoldAmount')::numeric),0) FROM image_billing_reservations WHERE user_id=$1 AND state IN ('reserved','reconciliation') AND quote->>'kind'='gemini_cache' AND (snapshot->>'ActorUserID')::bigint=$2 AND (snapshot->>'TeamID')::bigint=$3`, cmd.UserID, cmd.ActorUserID, *cmd.TeamID).Scan(&pending); err != nil {
		return err
	}
	amount = pending + cmd.HoldAmount
	if dl > 0 && effectiveNaturalWindowUsage(du, ds, timezone.StartOfDay(now))+amount > dl+1e-10 {
		return service.ErrTeamMemberDailyExceeded
	}
	if wl > 0 && effectiveNaturalWindowUsage(wu, ws, timezone.StartOfWeek(now))+amount > wl+1e-10 {
		return service.ErrTeamMemberWeeklyExceeded
	}
	if ml > 0 && effectiveNaturalWindowUsage(mu, ms, timezone.StartOfMonth(now))+amount > ml+1e-10 {
		return service.ErrTeamMemberMonthlyExceeded
	}
	return nil
}
