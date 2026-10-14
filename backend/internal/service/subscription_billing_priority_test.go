//go:build unit

package service

import (
	"context"
	"math"
	"sort"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

// 固定时刻覆盖窗口边界，避免临近午夜运行时产生偶发排序差异。
func priorityTestSubscription(now time.Time, id int64, resetIn, expiresIn time.Duration) UserSubscription {
	anchor := now.Add(resetIn - subscriptionWeeklyWindow)
	return UserSubscription{
		ID: id, UserID: 1, PlanID: id, Status: SubscriptionStatusActive,
		StartsAt: now.Add(-40 * 24 * time.Hour), ExpiresAt: now.Add(expiresIn),
		WeeklyWindowStart: &anchor, WeeklyLimitUSD: billingEligibilityLimitPtr(10),
	}
}

func TestSubscriptionBillingPriority_NearestResetBeforeSubscriptionExpiry(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, timezone.Location())
	soonReset := priorityTestSubscription(now, 2, time.Hour, 40*24*time.Hour)
	soonExpiry := priorityTestSubscription(now, 1, 2*24*time.Hour, 5*24*time.Hour)
	require.True(t, SubscriptionBillingLess(&soonReset, &soonExpiry, now))
	require.False(t, SubscriptionBillingLess(&soonExpiry, &soonReset, now))

	// 月窗口也参加统一比较，不固定优先日卡、周卡或月卡。
	monthlyAnchor := now.Add(time.Hour/2 - subscriptionMonthlyWindow)
	monthly := priorityTestSubscription(now, 3, 4*24*time.Hour, 60*24*time.Hour)
	monthly.MonthlyWindowStart, monthly.MonthlyLimitUSD = &monthlyAnchor, billingEligibilityLimitPtr(100)
	require.True(t, SubscriptionBillingLess(&monthly, &soonReset, now))
}

func TestSubscriptionBillingPriority_ProjectsWindowWithoutMutation(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, timezone.Location())
	for _, resetIn := range []time.Duration{-2 * subscriptionWeeklyWindow, -time.Hour, 0} {
		t.Run(resetIn.String(), func(t *testing.T) {
			stale := priorityTestSubscription(now, 1, resetIn, 40*24*time.Hour)
			stale.WeeklyUsageUSD = 10
			original := stale
			anchor := *stale.WeeklyWindowStart
			soon := priorityTestSubscription(now, 2, time.Hour, 50*24*time.Hour)
			require.True(t, SubscriptionBillingLess(&soon, &stale, now), "历史重置点应推进后再比较")
			require.Equal(t, original, stale)
			require.Equal(t, anchor, *stale.WeeklyWindowStart)
		})
	}
	unused := priorityTestSubscription(now, 1, time.Hour, 40*24*time.Hour)
	unused.WeeklyWindowStart = nil
	unused.WeeklyUsageUSD = 3
	require.Equal(t, now.Add(subscriptionWeeklyWindow), unused.billingDeadlineAt(now))
	require.Nil(t, unused.WeeklyWindowStart)
	require.Equal(t, 3.0, unused.WeeklyUsageUSD)
}

func TestSubscriptionBillingPriority_DailyAndOneTimeWindows(t *testing.T) {
	now := time.Date(2026, 10, 14, 23, 30, 0, 0, timezone.Location())
	day := priorityTestSubscription(now, 1, 3*24*time.Hour, 40*24*time.Hour)
	day.DailyLimitUSD = billingEligibilityLimitPtr(5)
	// 旧日锚点应投影到今天，不继续使用历史零点。
	oldDay := timezone.StartOfDay(now).AddDate(0, 0, -5)
	day.DailyWindowStart = &oldDay
	require.Equal(t, timezone.StartOfDay(now).AddDate(0, 0, 1), day.billingDeadlineAt(now))
	require.Equal(t, oldDay, *day.DailyWindowStart)
	weekly := priorityTestSubscription(now, 2, time.Hour, 50*24*time.Hour)
	require.True(t, SubscriptionBillingLess(&day, &weekly, now))

	// 一日卡跨零点不刷新，不能错误地抢占一小时后重置的周额度。
	day.StartsAt, day.ExpiresAt = now.Add(-time.Hour), now.Add(2*time.Hour)
	require.Equal(t, day.ExpiresAt, day.billingDeadlineAt(now))
	require.True(t, SubscriptionBillingLess(&weekly, &day, now))
}

func TestSubscriptionBillingPriority_IgnoresUnlimitedAndPostExpiryResets(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, timezone.Location())
	for _, limit := range []*float64{nil, billingEligibilityLimitPtr(0), billingEligibilityLimitPtr(-1), billingEligibilityLimitPtr(math.Inf(1)), billingEligibilityLimitPtr(math.NaN())} {
		sub := priorityTestSubscription(now, 1, time.Minute, 40*24*time.Hour)
		sub.WeeklyLimitUSD = limit
		require.Equal(t, sub.ExpiresAt, sub.billingDeadlineAt(now), "非有限额度不能产生虚假的重置优先级")
	}
	for _, resetIn := range []time.Duration{time.Hour, 2 * time.Hour} {
		sub := priorityTestSubscription(now, 1, resetIn, time.Hour)
		require.Equal(t, sub.ExpiresAt, sub.billingDeadlineAt(now))
	}
}

func TestSubscriptionBillingPriority_StableTiesAndExpiryBeforeReset(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, timezone.Location())
	a := priorityTestSubscription(now, 1, time.Hour, 20*24*time.Hour)
	b, c, d := a, a, a
	b.ID, c.ID, d.ID = 2, 3, 4
	b.StartsAt = b.StartsAt.Add(time.Hour)
	c.ExpiresAt = c.ExpiresAt.Add(time.Hour)
	d.ExpiresAt = now.Add(time.Minute)
	subs := []UserSubscription{c, b, a, d}
	sort.Slice(subs, func(i, j int) bool { return SubscriptionBillingLess(&subs[i], &subs[j], now) })
	require.Equal(t, []int64{4, 1, 2, 3}, []int64{subs[0].ID, subs[1].ID, subs[2].ID, subs[3].ID})
	b = a
	b.ID++
	require.True(t, SubscriptionBillingLess(&a, &b, now))
	require.False(t, SubscriptionBillingLess(&a, &a, now))
	require.True(t, SubscriptionBillingLess(&a, nil, now))
	require.False(t, SubscriptionBillingLess(nil, &a, now))
	require.False(t, SubscriptionBillingLess(nil, nil, now))
}

func TestSubscriptionBillingPriority_ManualAndLegacyAnchors(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, timezone.Location())
	sub := priorityTestSubscription(now, 1, time.Hour, 40*24*time.Hour)
	manual := timezone.StartOfDay(now).AddDate(0, 0, -2)
	sub.WeeklyWindowStart = &manual
	require.Equal(t, manual.Add(subscriptionWeeklyWindow), sub.billingDeadlineAt(now))
	// 旧初始零点只在确实早于开通时间时修正，保留既有窗口规则。
	sub.StartsAt = now.Add(-2*24*time.Hour - time.Hour)
	legacy := timezone.StartOfDay(sub.StartsAt)
	sub.WeeklyWindowStart = &legacy
	require.Equal(t, sub.StartsAt.Add(subscriptionWeeklyWindow), sub.billingDeadlineAt(now))
}

func TestGetUsableSubscription_PrefersNearestResetAndSkipsUnavailable(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		t.Run(map[bool]string{false: "nearest reset", true: "exhausted fallback"}[exhausted], func(t *testing.T) {
			now := time.Now()
			repo := newSubscriptionUserSubRepoStub()
			near := priorityTestSubscription(now, 2, time.Hour, 40*24*time.Hour)
			far := priorityTestSubscription(now, 1, 2*24*time.Hour, 5*24*time.Hour)
			if exhausted {
				near.WeeklyUsageUSD = 10
			}
			repo.seed(&far)
			repo.seed(&near)
			svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)
			sub, maintenance, err := svc.GetUsableSubscription(context.Background(), 1)
			require.NoError(t, err)
			expected := int64(2)
			if exhausted {
				expected = 1
			}
			require.Equal(t, expected, sub.ID)
			require.False(t, maintenance)
		})
	}
}

func TestGetUsableSubscription_PriorityDoesNotHideWindowMaintenance(t *testing.T) {
	now := time.Now()
	repo := newSubscriptionUserSubRepoStub()
	sub := priorityTestSubscription(now, 1, 0, 40*24*time.Hour)
	sub.WeeklyUsageUSD = 10
	repo.seed(&sub)
	// 至少两个候选才会触发比较器，才能检查排序未吞掉待维护标记。
	later := priorityTestSubscription(now, 2, time.Hour, 60*24*time.Hour)
	later.WeeklyLimitUSD = nil
	repo.seed(&later)
	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)
	selected, maintenance, err := svc.GetUsableSubscription(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, maintenance, "排序不能提前改窗口导致放行前遗漏持久化维护")
	require.Zero(t, selected.WeeklyUsageUSD)
	require.Equal(t, *sub.WeeklyWindowStart, *selected.WeeklyWindowStart)
	require.Equal(t, 10.0, repo.byID[1].WeeklyUsageUSD)
}
