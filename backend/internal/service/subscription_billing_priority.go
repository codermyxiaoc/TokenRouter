package service

import "time"

// @project-doc docs/domains/routing_and_billing.md#automatic_subscription_priority
// SubscriptionBillingLess 统一自动结算的订阅顺序，优先消耗最早重置或到期的额度。
// 调用方仍须校验归属、分组和剩余额度；事务锁顺序不能随消费顺序变化。
func SubscriptionBillingLess(left, right *UserSubscription, now time.Time) bool {
	if left == nil || right == nil {
		return left != nil && right == nil
	}
	leftDeadline, rightDeadline := left.billingDeadlineAt(now), right.billingDeadlineAt(now)
	if !leftDeadline.Equal(rightDeadline) {
		return leftDeadline.Before(rightDeadline)
	}
	if !left.ExpiresAt.Equal(right.ExpiresAt) {
		return left.ExpiresAt.Before(right.ExpiresAt)
	}
	if !left.StartsAt.Equal(right.StartsAt) {
		return left.StartsAt.Before(right.StartsAt)
	}
	return left.ID < right.ID
}

// billingDeadlineAt 只投影窗口，不提前激活或清零调用方的持久化/缓存快照。
// 先推进已经过期的窗口，避免把历史重置时间误认为当前额度即将过期。
func (s *UserSubscription) billingDeadlineAt(now time.Time) time.Time {
	projected := *s
	projected.NormalizeQuotaWindowsAt(now)
	deadline := projected.ExpiresAt
	for _, window := range []struct {
		limit *float64
		reset *time.Time
	}{
		{projected.DailyLimitUSD, projected.DailyResetTime()},
		{projected.WeeklyLimitUSD, projected.WeeklyResetTime()},
		{projected.MonthlyLimitUSD, projected.MonthlyResetTime()},
	} {
		// 无限额度没有周期损失；一次性额度、尾段及到期后的候选继续按到期处理。
		if finitePositiveSubscriptionLimit(window.limit) && window.reset != nil &&
			window.reset.After(now) && window.reset.Before(deadline) {
			deadline = *window.reset
		}
	}
	return deadline
}
