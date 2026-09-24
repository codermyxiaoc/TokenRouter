package service

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// ScheduledSubscriptionResetCounter 由持久化仓储实现，后台计数不修改额度用量或窗口。
type ScheduledSubscriptionResetCounter interface {
	RefreshScheduledResetCounts(ctx context.Context, now time.Time) error
}

// @project-doc docs/domains/payments_and_entitlements.md#subscription_quota_windows
// AccrueScheduledResetCounts 结算上次水位之后已到达的合法重置点；调用者负责锁行与原子持久化。
// 修改有效期、状态或实际窗口前必须先按旧状态结算，避免延期追补原本已过期的历史次数。
func (s *UserSubscription) AccrueScheduledResetCounts(now time.Time) bool {
	if s == nil || now.IsZero() || !now.After(s.ResetCountedAt) {
		return false
	}
	since := s.ResetCountedAt
	s.ResetCountedAt = now
	// 升级历史只建立基线；缺少水位时不能把无法核实的历史重新累计。
	if since.IsZero() || s.StartsAt.IsZero() || s.ExpiresAt.IsZero() ||
		s.DeletedAt != nil || s.Status == SubscriptionStatusRevoked || s.Status == SubscriptionStatusSuspended {
		return true
	}
	if since.Before(s.StartsAt) {
		since = s.StartsAt
	}
	if !now.After(since) {
		return true
	}
	if positiveSubscriptionLimit(s.DailyLimitUSD) && !s.HasOneTimeDailyQuota() {
		s.DailyResetCount += s.scheduledResetCountBetween(s.DailyWindowStart, subscriptionDailyWindow, since, now)
	}
	if positiveSubscriptionLimit(s.WeeklyLimitUSD) {
		s.WeeklyResetCount += s.scheduledResetCountBetween(s.WeeklyWindowStart, subscriptionWeeklyWindow, since, now)
	}
	if positiveSubscriptionLimit(s.MonthlyLimitUSD) {
		s.MonthlyResetCount += s.scheduledResetCountBetween(s.MonthlyWindowStart, subscriptionMonthlyWindow, since, now)
	}
	return true
}

// scheduledResetCountBetween 不依赖用量；未使用的日窗口从生效日零点计算，周/月从生效时刻计算。
// 已有窗口复用额度维护的锚点修正规则，首次使用或手动重置后按实际锚点继续累计。
func (s *UserSubscription) scheduledResetCountBetween(window *time.Time, duration time.Duration, since, until time.Time) int64 {
	anchor := s.StartsAt
	if window != nil && !window.IsZero() {
		anchor = s.windowResetAnchor(*window)
	}
	var next time.Time
	if duration == subscriptionDailyWindow {
		anchor = timezone.StartOfDay(anchor)
		next = anchor.AddDate(0, 0, 1)
		if !next.After(since) {
			next = timezone.StartOfDay(since).AddDate(0, 0, 1)
		}
	} else {
		next = anchor.Add(duration)
		if !next.After(since) {
			next = next.Add((since.Sub(next)/duration + 1) * duration)
		}
	}
	var count int64
	for !next.After(until) && next.Before(s.ExpiresAt) {
		// 与实际重置一致：重置时刻必须严格早于到期，不要求剩余一个完整周期。
		count++
		if duration == subscriptionDailyWindow {
			next = next.AddDate(0, 0, 1)
		} else {
			next = next.Add(duration)
		}
	}
	return count
}
