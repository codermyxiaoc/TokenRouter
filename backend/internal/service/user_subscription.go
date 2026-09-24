package service

import (
	"math"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

const (
	subscriptionDailyWindow   = 24 * time.Hour
	subscriptionWeeklyWindow  = 7 * 24 * time.Hour
	subscriptionMonthlyWindow = 30 * 24 * time.Hour
)

type UserSubscription struct {
	ID     int64
	UserID int64
	PlanID int64

	StartsAt  time.Time
	ExpiresAt time.Time
	Status    string

	DailyWindowStart   *time.Time
	WeeklyWindowStart  *time.Time
	MonthlyWindowStart *time.Time

	DailyLimitUSD   *float64
	WeeklyLimitUSD  *float64
	MonthlyLimitUSD *float64

	DailyUsageUSD   float64
	WeeklyUsageUSD  float64
	MonthlyUsageUSD float64

	// 重置次数按符合额度规则的时间点累计，与用户是否发起请求无关。
	DailyResetCount   int64
	WeeklyResetCount  int64
	MonthlyResetCount int64
	// 计数水位与次数同事务提交，避免后台扫描、请求维护和管理员操作重复计次。
	ResetCountedAt time.Time

	AssignedBy    *int64
	AssignedAt    time.Time
	SourceOrderID *int64
	Notes         string

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time

	User           *User
	Plan           *SubscriptionPlan
	AssignedByUser *User
}

// SubscriptionWindowActivation 表示本次维护允许首次激活的订阅额度窗口。
type SubscriptionWindowActivation struct {
	Daily   bool
	Weekly  bool
	Monthly bool
}

func (a SubscriptionWindowActivation) Any() bool {
	return a.Daily || a.Weekly || a.Monthly
}

func (s *UserSubscription) IsActive() bool {
	now := time.Now()
	return s.DeletedAt == nil && s.Status == SubscriptionStatusActive && !now.Before(s.StartsAt) && now.Before(s.ExpiresAt)
}

func (s *UserSubscription) IsPending() bool {
	if s == nil {
		return false
	}
	return s.DeletedAt == nil && s.Status == SubscriptionStatusPending && time.Now().Before(s.StartsAt)
}

func (s *UserSubscription) IsEffective() bool {
	if s == nil {
		return false
	}
	now := time.Now()
	return !now.Before(s.StartsAt) && now.Before(s.ExpiresAt) && s.EffectiveStatus(now) == SubscriptionStatusActive
}

func (s *UserSubscription) EffectiveStatus(now time.Time) string {
	if s == nil {
		return SubscriptionStatusExpired
	}
	if s.DeletedAt != nil {
		return SubscriptionStatusRevoked
	}
	if !s.ExpiresAt.After(now) {
		return SubscriptionStatusExpired
	}
	if now.Before(s.StartsAt) {
		return SubscriptionStatusPending
	}
	switch s.Status {
	case SubscriptionStatusSuspended:
		return SubscriptionStatusSuspended
	default:
		return SubscriptionStatusActive
	}
}

func (s *UserSubscription) IsExpired() bool {
	return !s.ExpiresAt.After(time.Now())
}

func (s *UserSubscription) DaysRemaining() int {
	return s.daysRemainingAt(time.Now())
}

func (s *UserSubscription) daysRemainingAt(now time.Time) int {
	remaining := s.ExpiresAt.Sub(now)
	if remaining <= 0 {
		return 0
	}

	days := int(remaining / subscriptionDailyWindow)
	if remaining%subscriptionDailyWindow != 0 {
		days++
	}
	return days
}

func (s *UserSubscription) IsWindowActivated() bool {
	return s.DailyWindowStart != nil || s.WeeklyWindowStart != nil || s.MonthlyWindowStart != nil
}

func (s *UserSubscription) HasQuotaLimit() bool {
	return positiveSubscriptionLimit(s.DailyLimitUSD) ||
		positiveSubscriptionLimit(s.WeeklyLimitUSD) ||
		positiveSubscriptionLimit(s.MonthlyLimitUSD)
}

func (s *UserSubscription) HasOneTimeDailyQuota() bool {
	if s == nil || s.StartsAt.IsZero() || s.ExpiresAt.IsZero() {
		return false
	}
	return !s.ExpiresAt.After(s.StartsAt.AddDate(0, 0, 1))
}

func (s *UserSubscription) NeedsWindowActivationAt(now time.Time) bool {
	return s.WindowActivationAt(now).Any()
}

// WindowActivationAt 与上游保持一致：生效期内首次使用即可激活，不要求剩余完整周期。
func (s *UserSubscription) WindowActivationAt(now time.Time) SubscriptionWindowActivation {
	var activation SubscriptionWindowActivation
	if s == nil || !s.HasQuotaLimit() || now.Before(s.StartsAt) || !now.Before(s.ExpiresAt) {
		return activation
	}

	if positiveSubscriptionLimit(s.DailyLimitUSD) && s.DailyWindowStart == nil {
		activation.Daily = true
	}
	if positiveSubscriptionLimit(s.WeeklyLimitUSD) && s.WeeklyWindowStart == nil {
		activation.Weekly = true
	}
	if positiveSubscriptionLimit(s.MonthlyLimitUSD) && s.MonthlyWindowStart == nil {
		activation.Monthly = true
	}
	return activation
}

func (s *UserSubscription) NeedsDailyReset() bool {
	return s.NeedsDailyResetAt(time.Now())
}

func (s *UserSubscription) NeedsDailyResetAt(now time.Time) bool {
	_, ok := s.automaticDailyWindowStartAt(now)
	return ok
}

// automaticDailyWindowStartAt 计算按项目时区日历日对齐的日窗口起点。
// 历史非零点锚点会在下一个零点自愈；1 日卡仍为一次性日额度。
func (s *UserSubscription) automaticDailyWindowStartAt(now time.Time) (time.Time, bool) {
	if s == nil || s.DailyWindowStart == nil || s.ExpiresAt.IsZero() || !now.Before(s.ExpiresAt) {
		return time.Time{}, false
	}
	if s.HasOneTimeDailyQuota() {
		return time.Time{}, false
	}
	today := timezone.StartOfDay(now)
	if !today.After(timezone.StartOfDay(*s.DailyWindowStart)) {
		return time.Time{}, false
	}
	return today, true
}

func (s *UserSubscription) NeedsWeeklyReset() bool {
	return s.NeedsWeeklyResetAt(time.Now())
}

func (s *UserSubscription) NeedsWeeklyResetAt(now time.Time) bool {
	if s == nil {
		return false
	}
	_, ok := s.automaticWindowStartAt(s.WeeklyWindowStart, subscriptionWeeklyWindow, now)
	return ok
}

func (s *UserSubscription) NeedsMonthlyReset() bool {
	return s.NeedsMonthlyResetAt(time.Now())
}

func (s *UserSubscription) NeedsMonthlyResetAt(now time.Time) bool {
	if s == nil {
		return false
	}
	_, ok := s.automaticWindowStartAt(s.MonthlyWindowStart, subscriptionMonthlyWindow, now)
	return ok
}

// windowResetAnchor 仅修正旧版本明确早于开通时刻的初始零点锚点。
// 后续零点可能来自管理员手动重置，必须保持权威，不能向前回推整个历史。
func (s *UserSubscription) windowResetAnchor(previous time.Time) time.Time {
	legacyAnchor := timezone.StartOfDay(s.StartsAt)
	if legacyAnchor.Before(s.StartsAt) && previous.Equal(legacyAnchor) {
		return s.StartsAt
	}
	return previous
}

// @project-doc docs/domains/payments_and_entitlements.md#subscription_quota_windows
// automaticWindowStartAt 按上游的期限对齐规则推进周/月窗口，迟来的请求不改变原刷新节奏。
// 只要求下个窗口起点早于到期时间；尾段不足一个完整周期也允许刷新。
func (s *UserSubscription) automaticWindowStartAt(previous *time.Time, period time.Duration, now time.Time) (time.Time, bool) {
	if s == nil || previous == nil || period <= 0 || s.ExpiresAt.IsZero() ||
		now.Before(s.StartsAt) || !now.Before(s.ExpiresAt) {
		return time.Time{}, false
	}
	anchor := s.windowResetAnchor(*previous)
	next := anchor.Add(period)
	if now.Before(next) || !next.Before(s.ExpiresAt) {
		return time.Time{}, false
	}
	periods := now.Sub(anchor) / period
	lastPeriodBeforeExpiry := (s.ExpiresAt.Sub(anchor) - 1) / period
	if periods > lastPeriodBeforeExpiry {
		periods = lastPeriodBeforeExpiry
	}
	return anchor.Add(periods * period), true
}

// NormalizeQuotaWindowsAt 在内存中统一激活与推进额度窗口，供锁内事务扣费复用。
// 调用者须先结算重置次数；首次补齐空锚点保留历史用量，只有到期的旧窗口才清零。
func (s *UserSubscription) NormalizeQuotaWindowsAt(now time.Time) bool {
	if s == nil || s.EffectiveStatus(now) != SubscriptionStatusActive || s.Status == SubscriptionStatusRevoked {
		return false
	}
	changed := false
	activation := s.WindowActivationAt(now)
	if activation.Daily {
		start := timezone.StartOfDay(now)
		s.DailyWindowStart = &start
		changed = true
	}
	if activation.Weekly {
		start := now
		s.WeeklyWindowStart = &start
		changed = true
	}
	if activation.Monthly {
		start := now
		s.MonthlyWindowStart = &start
		changed = true
	}
	if positiveSubscriptionLimit(s.DailyLimitUSD) {
		if start, ok := s.automaticDailyWindowStartAt(now); ok {
			s.DailyWindowStart, s.DailyUsageUSD = &start, 0
			changed = true
		}
	}
	for _, window := range []struct {
		limit  *float64
		start  **time.Time
		used   *float64
		period time.Duration
	}{
		{s.WeeklyLimitUSD, &s.WeeklyWindowStart, &s.WeeklyUsageUSD, subscriptionWeeklyWindow},
		{s.MonthlyLimitUSD, &s.MonthlyWindowStart, &s.MonthlyUsageUSD, subscriptionMonthlyWindow},
	} {
		if positiveSubscriptionLimit(window.limit) {
			if start, ok := s.automaticWindowStartAt(*window.start, window.period, now); ok {
				*window.start, *window.used = &start, 0
				changed = true
			}
		}
	}
	return changed
}

func (s *UserSubscription) DailyResetTime() *time.Time {
	if s.DailyWindowStart == nil {
		return nil
	}
	if s.HasOneTimeDailyQuota() {
		t := s.ExpiresAt
		return &t
	}
	// 日额度按日历日刷新，旧的非零点锚点也应展示其所在日的下一个零点。
	t := timezone.StartOfDay(*s.DailyWindowStart).AddDate(0, 0, 1)
	return &t
}

func (s *UserSubscription) WeeklyResetTime() *time.Time {
	if s.WeeklyWindowStart == nil {
		return nil
	}
	t := s.windowResetAnchor(*s.WeeklyWindowStart).Add(subscriptionWeeklyWindow)
	return &t
}

func (s *UserSubscription) MonthlyResetTime() *time.Time {
	if s.MonthlyWindowStart == nil {
		return nil
	}
	t := s.windowResetAnchor(*s.MonthlyWindowStart).Add(subscriptionMonthlyWindow)
	return &t
}

func (s *UserSubscription) CheckDailyLimit(additionalCost float64) bool {
	if s.DailyLimitUSD == nil || *s.DailyLimitUSD <= 0 {
		return true
	}
	return s.DailyUsageUSD+additionalCost <= *s.DailyLimitUSD
}

func (s *UserSubscription) CheckWeeklyLimit(additionalCost float64) bool {
	if s.WeeklyLimitUSD == nil || *s.WeeklyLimitUSD <= 0 {
		return true
	}
	return s.WeeklyUsageUSD+additionalCost <= *s.WeeklyLimitUSD
}

func (s *UserSubscription) CheckMonthlyLimit(additionalCost float64) bool {
	if s.MonthlyLimitUSD == nil || *s.MonthlyLimitUSD <= 0 {
		return true
	}
	return s.MonthlyUsageUSD+additionalCost <= *s.MonthlyLimitUSD
}

func (s *UserSubscription) CheckAllLimits(additionalCost float64) (daily, weekly, monthly bool) {
	daily = s.CheckDailyLimit(additionalCost)
	weekly = s.CheckWeeklyLimit(additionalCost)
	monthly = s.CheckMonthlyLimit(additionalCost)
	return
}

func (s *UserSubscription) RemainingDailyUSD() *float64 {
	return remainingWindowAmount(s.DailyLimitUSD, s.DailyUsageUSD)
}

func (s *UserSubscription) RemainingWeeklyUSD() *float64 {
	return remainingWindowAmount(s.WeeklyLimitUSD, s.WeeklyUsageUSD)
}

func (s *UserSubscription) RemainingMonthlyUSD() *float64 {
	return remainingWindowAmount(s.MonthlyLimitUSD, s.MonthlyUsageUSD)
}

func (s *UserSubscription) AvailableQuotaUSD() float64 {
	return minRemainingWindowAmount(
		s.RemainingDailyUSD(),
		s.RemainingWeeklyUSD(),
		s.RemainingMonthlyUSD(),
	)
}

// @project-doc docs/domains/payments_and_entitlements.md#subscription_self_revoke
// HighestQuotaExhausted 判断最高层有限额度是否已耗尽。
// 月、周、日按优先级选择第一个正数额度；低层窗口耗尽但高层仍有额度时不能撤销套餐。
func (s *UserSubscription) HighestQuotaExhausted() bool {
	if s == nil {
		return false
	}
	for _, quota := range []struct {
		limit float64
		used  float64
		ok    bool
	}{
		{limit: valueOrZeroLimit(s.MonthlyLimitUSD), used: s.MonthlyUsageUSD, ok: finitePositiveSubscriptionLimit(s.MonthlyLimitUSD)},
		{limit: valueOrZeroLimit(s.WeeklyLimitUSD), used: s.WeeklyUsageUSD, ok: finitePositiveSubscriptionLimit(s.WeeklyLimitUSD)},
		{limit: valueOrZeroLimit(s.DailyLimitUSD), used: s.DailyUsageUSD, ok: finitePositiveSubscriptionLimit(s.DailyLimitUSD)},
	} {
		if quota.ok {
			return quota.used >= quota.limit
		}
	}
	return false
}

func valueOrZeroLimit(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func finitePositiveSubscriptionLimit(limit *float64) bool {
	return positiveSubscriptionLimit(limit) && !math.IsNaN(*limit) && !math.IsInf(*limit, 0)
}

func remainingWindowAmount(limit *float64, used float64) *float64 {
	if limit == nil || *limit <= 0 {
		return nil
	}
	remaining := *limit - used
	if remaining < 0 {
		remaining = 0
	}
	return &remaining
}

func minRemainingWindowAmount(values ...*float64) float64 {
	var (
		min   float64
		found bool
	)
	for _, value := range values {
		if value == nil {
			continue
		}
		if !found || *value < min {
			min = *value
			found = true
		}
	}
	if !found {
		return 0
	}
	return min
}

func positiveSubscriptionLimit(limit *float64) bool {
	return limit != nil && *limit > 0
}
