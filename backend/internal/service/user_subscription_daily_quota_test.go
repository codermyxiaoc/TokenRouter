package service

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

type dailyResetTrackingUserSubRepo struct {
	userSubRepoNoop

	resetDailyCalled   bool
	resetWeeklyCalled  bool
	resetMonthlyCalled bool
	activateCalled     bool
	lastActivation     SubscriptionWindowActivation
	lastDailyStart     time.Time
	lastActivationAt   time.Time
}

func (r *dailyResetTrackingUserSubRepo) ActivateWindows(_ context.Context, _ int64, at time.Time, activation SubscriptionWindowActivation) error {
	r.activateCalled = true
	r.lastActivation = activation
	r.lastActivationAt = at
	return nil
}

func (r *dailyResetTrackingUserSubRepo) ResetDailyUsage(_ context.Context, _ int64, _ *time.Time, windowStart time.Time) error {
	r.resetDailyCalled = true
	r.lastDailyStart = windowStart
	return nil
}

func (r *dailyResetTrackingUserSubRepo) ResetWeeklyUsage(context.Context, int64, *time.Time, time.Time) error {
	r.resetWeeklyCalled = true
	return nil
}

func (r *dailyResetTrackingUserSubRepo) ResetMonthlyUsage(context.Context, int64, *time.Time, time.Time) error {
	r.resetMonthlyCalled = true
	return nil
}

func TestAssignOrExtendSubscription_ExpiredDailyCardStartsNewOneTimeQuota(t *testing.T) {
	subRepo := newSubscriptionUserSubRepoStub()
	limit := 10.0
	oldStart := time.Now().AddDate(0, 0, -3)
	oldWindowStart := startOfDay(oldStart)
	subRepo.seed(&UserSubscription{
		ID:                 100,
		UserID:             200,
		PlanID:             1,
		StartsAt:           oldStart,
		ExpiresAt:          oldStart.AddDate(0, 0, 1),
		Status:             SubscriptionStatusExpired,
		DailyWindowStart:   &oldWindowStart,
		WeeklyWindowStart:  &oldWindowStart,
		MonthlyWindowStart: &oldWindowStart,
		DailyLimitUSD:      &limit,
		DailyUsageUSD:      10,
		WeeklyUsageUSD:     20,
		MonthlyUsageUSD:    30,
		Notes:              "old",
	})
	svc := NewSubscriptionService(groupRepoNoop{}, subRepo, nil, nil, nil)

	created, queued, err := svc.AssignOrExtendSubscription(context.Background(), &AssignSubscriptionInput{
		UserID:        200,
		PlanID:        1,
		ValidityDays:  1,
		DailyLimitUSD: &limit,
		Notes:         "new",
	})

	require.NoError(t, err)
	require.False(t, queued)
	require.True(t, created.HasOneTimeDailyQuota(), "过期后重新购买 1 日卡仍应被识别为一次性日额度")
	require.Equal(t, SubscriptionStatusActive, created.Status)
	require.True(t, created.StartsAt.After(oldStart), "重新购买过期订阅时应创建新的当前周期")
	require.False(t, created.ExpiresAt.After(created.StartsAt.AddDate(0, 0, 1)))
	require.Nil(t, created.DailyWindowStart, "fork 当前订阅窗口保持首次使用时激活")
	require.Equal(t, 0.0, created.DailyUsageUSD)
	require.Equal(t, 0.0, created.WeeklyUsageUSD)
	require.Equal(t, 0.0, created.MonthlyUsageUSD)
	require.Equal(t, "new", created.Notes)
	require.Equal(t, 1, subRepo.createCalls)
}

func TestUserSubscriptionNeedsDailyReset_DailyCardKeepsOneTimeQuota(t *testing.T) {
	start := time.Date(2026, 5, 18, 12, 0, 0, 0, timezone.Location())
	dailyWindowStart := time.Date(2026, 5, 18, 0, 0, 0, 0, timezone.Location())
	sub := &UserSubscription{
		StartsAt:         start,
		ExpiresAt:        start.Add(24 * time.Hour),
		DailyWindowStart: &dailyWindowStart,
		DailyUsageUSD:    10,
	}

	require.True(t, sub.HasOneTimeDailyQuota())
	require.False(t, sub.NeedsDailyResetAt(dailyWindowStart.Add(25*time.Hour)), "日卡应作为一次性配额，跨 0 点后不再刷新日额度")
}

func TestUserSubscriptionNeedsDailyReset_MultiDaySubscriptionStillRefreshes(t *testing.T) {
	start := time.Date(2026, 5, 18, 12, 0, 0, 0, timezone.Location())
	dailyWindowStart := time.Date(2026, 5, 18, 0, 0, 0, 0, timezone.Location())
	sub := &UserSubscription{
		StartsAt:         start,
		ExpiresAt:        start.AddDate(0, 0, 2),
		DailyWindowStart: &dailyWindowStart,
	}

	require.False(t, sub.HasOneTimeDailyQuota())
	require.True(t, sub.NeedsDailyResetAt(dailyWindowStart.Add(24*time.Hour)), "多日订阅仍应在次日零点刷新")
}

func TestUserSubscriptionNeedsDailyReset_LegacyAnchorHealsAtNextMidnight(t *testing.T) {
	base := timezone.StartOfDay(time.Now())
	legacyWindowStart := base.Add(16*time.Hour + 49*time.Minute)
	sub := &UserSubscription{
		StartsAt:         base.AddDate(0, 0, -3),
		ExpiresAt:        base.AddDate(0, 0, 10),
		DailyWindowStart: &legacyWindowStart,
	}

	require.False(t, sub.NeedsDailyResetAt(base.Add(23*time.Hour+59*time.Minute)), "同一日历日内不应重置日额度")
	require.True(t, sub.NeedsDailyResetAt(base.AddDate(0, 0, 1).Add(time.Minute)), "旧的非零点锚点应在下一个零点后自愈")
}

func TestUserSubscriptionNeedsDailyReset_AllowsExpiryTailWindow(t *testing.T) {
	start := time.Date(2026, 4, 30, 8, 0, 0, 0, timezone.Location())
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, timezone.Location())
	dailyWindowStart := time.Date(2026, 5, 29, 0, 0, 0, 0, timezone.Location())
	now := time.Date(2026, 5, 30, 0, 5, 0, 0, timezone.Location())
	sub := &UserSubscription{
		StartsAt:         start,
		ExpiresAt:        expiresAt,
		DailyWindowStart: &dailyWindowStart,
	}

	require.True(t, sub.NeedsDailyResetAt(now), "下一日零点早于到期即可刷新，不要求剩余完整一天")
	require.False(t, sub.NeedsDailyResetAt(expiresAt), "到期时刻不再刷新日额度")
}

func TestUserSubscriptionNeedsDailyReset_ExpiryTailDoesNotDependOnOuterLimit(t *testing.T) {
	start := time.Date(2026, 4, 30, 8, 0, 0, 0, timezone.Location())
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, timezone.Location())
	dailyWindowStart := time.Date(2026, 5, 29, 0, 0, 0, 0, timezone.Location())
	now := time.Date(2026, 5, 30, 0, 5, 0, 0, timezone.Location())
	dailyLimit := 10.0
	outerLimit := 100.0
	unlimited := 0.0

	tests := []struct {
		name         string
		weeklyLimit  *float64
		monthlyLimit *float64
		want         bool
	}{
		{name: "有限周额度", weeklyLimit: &outerLimit, want: true},
		{name: "有限月额度", monthlyLimit: &outerLimit, want: true},
		{name: "零值周额度", weeklyLimit: &unlimited, want: true},
		{name: "没有外层额度", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := &UserSubscription{
				StartsAt:         start,
				ExpiresAt:        expiresAt,
				DailyWindowStart: &dailyWindowStart,
				DailyLimitUSD:    &dailyLimit,
				WeeklyLimitUSD:   tt.weeklyLimit,
				MonthlyLimitUSD:  tt.monthlyLimit,
			}

			require.Equal(t, tt.want, sub.NeedsDailyResetAt(now))
		})
	}
}

func TestUserSubscriptionNeedsWeeklyReset_ExpiryTailDoesNotDependOnMonthlyLimit(t *testing.T) {
	start := time.Date(2026, 4, 30, 8, 0, 0, 0, timezone.Location())
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, timezone.Location())
	weeklyWindowStart := time.Date(2026, 5, 23, 0, 0, 0, 0, timezone.Location())
	now := time.Date(2026, 5, 30, 0, 5, 0, 0, timezone.Location())
	weeklyLimit := 50.0
	monthlyLimit := 100.0
	sub := &UserSubscription{
		StartsAt:          start,
		ExpiresAt:         expiresAt,
		WeeklyWindowStart: &weeklyWindowStart,
		WeeklyLimitUSD:    &weeklyLimit,
		MonthlyLimitUSD:   &monthlyLimit,
	}

	require.True(t, sub.NeedsWeeklyResetAt(now), "周重置点早于到期即可刷新")
	sub.MonthlyLimitUSD = nil
	require.True(t, sub.NeedsWeeklyResetAt(now), "周额度尾段刷新不要求额外月额度")
	require.False(t, sub.NeedsWeeklyResetAt(expiresAt))
}

func TestUserSubscriptionNeedsDailyReset_DailyCardIgnoresFiniteOuterLimit(t *testing.T) {
	start := time.Date(2026, 5, 30, 12, 0, 0, 0, timezone.Location())
	dailyWindowStart := time.Date(2026, 5, 30, 0, 0, 0, 0, timezone.Location())
	dailyLimit := 10.0
	monthlyLimit := 100.0
	sub := &UserSubscription{
		StartsAt:         start,
		ExpiresAt:        start.Add(24 * time.Hour),
		DailyWindowStart: &dailyWindowStart,
		DailyLimitUSD:    &dailyLimit,
		MonthlyLimitUSD:  &monthlyLimit,
	}

	require.False(t, sub.NeedsDailyResetAt(time.Date(2026, 5, 31, 0, 5, 0, 0, timezone.Location())), "1 日卡始终只有一份日额度")
}

func TestUserSubscriptionNeedsMonthlyReset_AllowsExpiryTailWindow(t *testing.T) {
	start := time.Date(2026, 3, 30, 8, 0, 0, 0, timezone.Location())
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, timezone.Location())
	monthlyWindowStart := time.Date(2026, 4, 30, 0, 0, 0, 0, timezone.Location())
	now := time.Date(2026, 5, 30, 0, 5, 0, 0, timezone.Location())
	sub := &UserSubscription{
		StartsAt:           start,
		ExpiresAt:          expiresAt,
		MonthlyWindowStart: &monthlyWindowStart,
	}

	require.True(t, sub.NeedsMonthlyResetAt(now), "月重置点早于到期即可刷新，不要求剩余完整月窗口")
	require.False(t, sub.NeedsMonthlyResetAt(expiresAt))
}

func TestUserSubscriptionDailyResetTime_DailyCardReturnsExpiry(t *testing.T) {
	start := time.Date(2026, 5, 18, 12, 0, 0, 0, timezone.Location())
	dailyWindowStart := time.Date(2026, 5, 18, 0, 0, 0, 0, timezone.Location())
	expiresAt := start.Add(24 * time.Hour)
	sub := &UserSubscription{
		StartsAt:         start,
		ExpiresAt:        expiresAt,
		DailyWindowStart: &dailyWindowStart,
	}

	resetAt := sub.DailyResetTime()
	require.NotNil(t, resetAt)
	require.Equal(t, expiresAt, *resetAt, "日卡展示的日额度结束时间应为订阅过期时间")
}

func TestUserSubscriptionDailyResetTime_LegacyAnchorReturnsNextMidnight(t *testing.T) {
	base := timezone.StartOfDay(time.Now())
	legacyWindowStart := base.Add(16*time.Hour + 49*time.Minute)
	sub := &UserSubscription{
		StartsAt:         base.AddDate(0, 0, -3),
		ExpiresAt:        base.AddDate(0, 0, 10),
		DailyWindowStart: &legacyWindowStart,
	}

	resetAt := sub.DailyResetTime()
	require.NotNil(t, resetAt)
	require.Equal(t, base.AddDate(0, 0, 1), *resetAt, "旧的非零点锚点应展示其所在日的下一个零点")
}

func TestUserSubscriptionMonthlyResetTime_TailWindowReturnsNextReset(t *testing.T) {
	start := time.Date(2026, 3, 30, 8, 0, 0, 0, timezone.Location())
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, timezone.Location())
	monthlyWindowStart := time.Date(2026, 4, 30, 0, 0, 0, 0, timezone.Location())
	sub := &UserSubscription{
		StartsAt:           start,
		ExpiresAt:          expiresAt,
		MonthlyWindowStart: &monthlyWindowStart,
	}

	resetAt := sub.MonthlyResetTime()
	require.NotNil(t, resetAt)
	require.Equal(t, monthlyWindowStart.Add(subscriptionMonthlyWindow), *resetAt, "到期尾段仍展示原月锚点对应的重置时间")
}

func TestUserSubscriptionDailyResetTime_TailWindowUsesFiniteMonthlyLimit(t *testing.T) {
	start := time.Date(2026, 4, 30, 8, 0, 0, 0, timezone.Location())
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, timezone.Location())
	dailyWindowStart := time.Date(2026, 5, 29, 0, 0, 0, 0, timezone.Location())
	monthlyLimit := 100.0
	sub := &UserSubscription{
		StartsAt:         start,
		ExpiresAt:        expiresAt,
		DailyWindowStart: &dailyWindowStart,
		MonthlyLimitUSD:  &monthlyLimit,
	}

	resetAt := sub.DailyResetTime()
	require.NotNil(t, resetAt)
	require.Equal(t, dailyWindowStart.AddDate(0, 0, 1), *resetAt, "尾段仍展示项目时区下一日零点")
}

func TestCheckAndResetWindows_DailyCardDoesNotResetDailyUsage(t *testing.T) {
	now := time.Now()
	startsAt := now.Add(-23 * time.Hour)
	dailyWindowStart := now.Add(-25 * time.Hour)
	repo := &dailyResetTrackingUserSubRepo{}
	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)
	sub := &UserSubscription{
		ID:               1,
		UserID:           10,
		PlanID:           20,
		StartsAt:         startsAt,
		ExpiresAt:        startsAt.Add(24 * time.Hour),
		DailyUsageUSD:    10,
		DailyWindowStart: &dailyWindowStart,
	}

	err := svc.CheckAndResetWindows(context.Background(), sub)

	require.NoError(t, err)
	require.False(t, repo.resetDailyCalled, "日卡作为一次性配额，过了 24 小时日窗口也不应重置 daily usage")
	require.Equal(t, 10.0, sub.DailyUsageUSD)
}

func TestCheckAndResetWindows_MultiDaySubscriptionStillResetsDailyUsage(t *testing.T) {
	now := time.Now()
	startsAt := now.Add(-48 * time.Hour)
	dailyWindowStart := now.Add(-25 * time.Hour)
	repo := &dailyResetTrackingUserSubRepo{}
	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)
	sub := &UserSubscription{
		ID:               1,
		UserID:           10,
		PlanID:           20,
		StartsAt:         startsAt,
		ExpiresAt:        startsAt.AddDate(0, 0, 4),
		DailyUsageUSD:    10,
		DailyWindowStart: &dailyWindowStart,
	}

	err := svc.CheckAndResetWindows(context.Background(), sub)

	require.NoError(t, err)
	require.True(t, repo.resetDailyCalled, "多日订阅仍应重置过期 daily window")
	require.Equal(t, 0.0, sub.DailyUsageUSD)
}

func TestCheckAndResetWindows_LegacyDailyAnchorHealsToMidnight(t *testing.T) {
	base := timezone.StartOfDay(time.Now())
	legacyWindowStart := base.AddDate(0, 0, -1).Add(16*time.Hour + 49*time.Minute)
	now := base.Add(5 * time.Minute)
	repo := &dailyResetTrackingUserSubRepo{}
	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)
	sub := &UserSubscription{
		ID:               1,
		UserID:           10,
		PlanID:           20,
		StartsAt:         base.AddDate(0, 0, -3),
		ExpiresAt:        base.AddDate(0, 0, 10),
		DailyUsageUSD:    10,
		DailyWindowStart: &legacyWindowStart,
	}

	err := svc.checkAndResetWindowsAt(context.Background(), sub, now)

	require.NoError(t, err)
	require.True(t, repo.resetDailyCalled, "跨零点后应重置旧的非零点日窗口")
	require.Equal(t, base, repo.lastDailyStart, "写回的日窗口起点应为当天零点")
	require.Equal(t, base, *sub.DailyWindowStart)
	require.Zero(t, sub.DailyUsageUSD)
}

func TestCheckAndResetWindows_ExpiryTailResetsMonthlyUsageAtScheduledTime(t *testing.T) {
	start := time.Date(2026, 3, 30, 8, 0, 0, 0, timezone.Location())
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, timezone.Location())
	monthlyWindowStart := time.Date(2026, 4, 30, 0, 0, 0, 0, timezone.Location())
	repo := &dailyResetTrackingUserSubRepo{}
	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)
	sub := &UserSubscription{
		ID:                 1,
		UserID:             10,
		PlanID:             20,
		StartsAt:           start,
		ExpiresAt:          expiresAt,
		MonthlyUsageUSD:    10,
		MonthlyWindowStart: &monthlyWindowStart,
	}

	now := time.Date(2026, 5, 30, 0, 5, 0, 0, timezone.Location())
	err := svc.checkAndResetWindowsAt(context.Background(), sub, now)

	require.NoError(t, err)
	require.True(t, repo.resetMonthlyCalled, "到期前已经到达的月重置点应刷新额度")
	require.Zero(t, sub.MonthlyUsageUSD)
	require.Equal(t, monthlyWindowStart.Add(subscriptionMonthlyWindow), *sub.MonthlyWindowStart)
}

func TestValidateAndCheckLimits_ExpiryTailMissingWindowNeedsActivation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		monthlyLimit := 100.0
		sub := &UserSubscription{
			Status:          SubscriptionStatusActive,
			StartsAt:        now.AddDate(0, 0, -29),
			ExpiresAt:       now.Add(2 * time.Hour),
			MonthlyLimitUSD: &monthlyLimit,
			MonthlyUsageUSD: 90,
		}
		svc := NewSubscriptionService(groupRepoNoop{}, userSubRepoNoop{}, nil, nil, nil)

		needsMaintenance, err := svc.ValidateAndCheckLimits(sub, nil)

		require.NoError(t, err)
		require.True(t, needsMaintenance, "订阅生效期间的空月窗口应激活，不要求完整剩余周期")
		require.Equal(t, 90.0, sub.MonthlyUsageUSD, "首次补齐窗口不能清除历史用量")
	})
}

func TestDoWindowMaintenance_ExpiryTailMissingWindowActivatesWithoutClearingUsage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		monthlyLimit := 100.0
		repo := &dailyResetTrackingUserSubRepo{}
		svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)
		sub := &UserSubscription{
			ID:              1,
			Status:          SubscriptionStatusActive,
			StartsAt:        now.AddDate(0, 0, -29),
			ExpiresAt:       now.Add(2 * time.Hour),
			MonthlyLimitUSD: &monthlyLimit,
			MonthlyUsageUSD: 90,
		}

		svc.DoWindowMaintenance(sub)

		require.True(t, repo.activateCalled, "生效尾段也应首次建立月窗口")
		require.True(t, repo.lastActivation.Monthly)
		require.Equal(t, now, repo.lastActivationAt, "周/月首次激活使用当前实际时间")
		require.False(t, repo.resetMonthlyCalled)
		require.Equal(t, 90.0, sub.MonthlyUsageUSD)
	})
}

func TestDoWindowMaintenance_MissingDailyCardWindowStillActivates(t *testing.T) {
	now := time.Now()
	dailyLimit := 10.0
	repo := &dailyResetTrackingUserSubRepo{}
	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)
	sub := &UserSubscription{
		ID:            1,
		Status:        SubscriptionStatusActive,
		StartsAt:      now.Add(-time.Hour),
		ExpiresAt:     now.Add(time.Hour),
		DailyLimitUSD: &dailyLimit,
	}

	svc.DoWindowMaintenance(sub)

	require.True(t, repo.activateCalled, "一次性日额度首次使用仍应激活窗口")
	require.True(t, repo.lastActivation.Daily)
	require.False(t, repo.lastActivation.Weekly)
	require.False(t, repo.lastActivation.Monthly)
}

func TestUserSubscriptionWindowActivation_MixedDailyMonthlyTailActivatesBoth(t *testing.T) {
	now := time.Date(2026, 5, 30, 0, 5, 0, 0, timezone.Location())
	dailyLimit := 10.0
	monthlyLimit := 100.0
	sub := &UserSubscription{
		ID:              1,
		Status:          SubscriptionStatusActive,
		StartsAt:        now.AddDate(0, 0, -29),
		ExpiresAt:       now.Add(2 * time.Hour),
		DailyLimitUSD:   &dailyLimit,
		MonthlyLimitUSD: &monthlyLimit,
		MonthlyUsageUSD: 90,
	}

	activation := sub.WindowActivationAt(now)

	require.True(t, activation.Daily, "生效尾段仍应激活日额度窗口")
	require.False(t, activation.Weekly)
	require.True(t, activation.Monthly, "空月窗口也应激活，不受剩余周期长度限制")
}

func TestUserSubscriptionWindowActivation_MixedWeeklyMonthlyTailActivatesBoth(t *testing.T) {
	now := time.Date(2026, 5, 30, 0, 5, 0, 0, timezone.Location())
	weeklyLimit := 50.0
	monthlyLimit := 100.0
	sub := &UserSubscription{
		ID:              1,
		Status:          SubscriptionStatusActive,
		StartsAt:        now.AddDate(0, 0, -29),
		ExpiresAt:       now.Add(2 * time.Hour),
		WeeklyLimitUSD:  &weeklyLimit,
		MonthlyLimitUSD: &monthlyLimit,
		MonthlyUsageUSD: 90,
	}

	activation := sub.WindowActivationAt(now)

	require.False(t, activation.Daily)
	require.True(t, activation.Weekly, "生效尾段仍应激活周额度窗口")
	require.True(t, activation.Monthly, "空月窗口也应激活，不受外层额度限制")
}

func TestCheckEffectiveSubscriptionEligibility_DailyTailUsesFiniteMonthlyLimit(t *testing.T) {
	now := time.Now()
	dailyWindowStart := now.Add(-25 * time.Hour)
	dailyLimit := 200.0
	monthlyLimit := 1000.0
	sub := &UserSubscription{
		Status:           SubscriptionStatusActive,
		StartsAt:         now.AddDate(0, 0, -30),
		ExpiresAt:        now.Add(2 * time.Hour),
		DailyWindowStart: &dailyWindowStart,
		DailyLimitUSD:    &dailyLimit,
		MonthlyLimitUSD:  &monthlyLimit,
		DailyUsageUSD:    dailyLimit,
		MonthlyUsageUSD:  860.98,
	}

	require.NoError(t, checkEffectiveSubscriptionEligibility(sub), "有限月额度未耗尽时，尾段日窗口应通过计费热路径预检")
}

func TestValidateAndCheckLimits_DailyCardDoesNotAllowSecondQuotaAfterMidnight(t *testing.T) {
	start := time.Now().Add(-23 * time.Hour)
	dailyWindowStart := time.Now().Add(-25 * time.Hour)
	dailyLimit := 10.0
	sub := &UserSubscription{
		Status:           SubscriptionStatusActive,
		StartsAt:         start,
		ExpiresAt:        start.Add(24 * time.Hour),
		DailyLimitUSD:    &dailyLimit,
		DailyWindowStart: &dailyWindowStart,
		DailyUsageUSD:    dailyLimit + 0.01,
	}
	svc := NewSubscriptionService(groupRepoNoop{}, userSubRepoNoop{}, nil, nil, nil)

	needsMaintenance, err := svc.ValidateAndCheckLimits(sub, nil)

	require.False(t, needsMaintenance, "日卡跨过日窗口后不应触发 daily reset 维护")
	require.True(t, errors.Is(err, ErrDailyLimitExceeded))
	require.Equal(t, dailyLimit+0.01, sub.DailyUsageUSD, "热路径不应清零日卡已用额度")
}
