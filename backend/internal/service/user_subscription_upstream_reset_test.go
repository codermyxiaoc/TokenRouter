package service

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

// 固定项目日历时间，避免服务器时区与开发机 UTC 差异影响初始零点识别。
func upstreamResetTestTime(month time.Month, day, hour, minute int) time.Time {
	return time.Date(2026, month, day, hour, minute, 0, 0, timezone.Location())
}

func TestUpstreamSubscriptionReset_WeeklySeptember30BeforeOctober5(t *testing.T) {
	anchor := upstreamResetTestTime(time.September, 23, 0, 0)
	due := upstreamResetTestTime(time.September, 30, 0, 0)
	sub := &UserSubscription{
		ID: 1343, StartsAt: upstreamResetTestTime(time.September, 2, 10, 11),
		ExpiresAt:         upstreamResetTestTime(time.October, 5, 10, 18),
		WeeklyWindowStart: &anchor, WeeklyUsageUSD: 43.25,
	}
	require.Equal(t, due, *sub.WeeklyResetTime(), "截图中的周窗口应在 9 月 30 日刷新")
	repo := &dailyResetTrackingUserSubRepo{}
	svc := &SubscriptionService{userSubRepo: repo}
	require.NoError(t, svc.checkAndResetWindowsAt(context.Background(), sub, due.Add(-time.Nanosecond)))
	require.False(t, repo.resetWeeklyCalled)
	require.Equal(t, 43.25, sub.WeeklyUsageUSD)
	require.NoError(t, svc.checkAndResetWindowsAt(context.Background(), sub, due))
	require.True(t, repo.resetWeeklyCalled)
	require.Equal(t, due, *sub.WeeklyWindowStart)
	require.Zero(t, sub.WeeklyUsageUSD)
	sub.WeeklyUsageUSD = 2.5
	repo.resetWeeklyCalled = false
	require.NoError(t, svc.checkAndResetWindowsAt(context.Background(), sub, sub.ExpiresAt.Add(-time.Nanosecond)))
	require.False(t, repo.resetWeeklyCalled, "下一重置已晚于到期，不能再发周额度")
	require.Equal(t, 2.5, sub.WeeklyUsageUSD)
}

func TestUpstreamSubscriptionReset_IntegerCadenceDoesNotFollowRequestTime(t *testing.T) {
	for _, period := range []struct {
		name     string
		duration time.Duration
	}{{"weekly", subscriptionWeeklyWindow}, {"monthly", subscriptionMonthlyWindow}} {
		t.Run(period.name, func(t *testing.T) {
			anchor := upstreamResetTestTime(time.September, 1, 10, 18)
			sub := &UserSubscription{StartsAt: anchor, ExpiresAt: anchor.Add(10 * period.duration)}
			now := anchor.Add(3*period.duration + 5*time.Hour + 17*time.Minute)
			start, ok := sub.automaticWindowStartAt(&anchor, period.duration, now)
			require.True(t, ok)
			require.Equal(t, anchor.Add(3*period.duration), start, "迟来的请求只推进整数周期，不把请求时间变成锚点")
			_, ok = sub.automaticWindowStartAt(&start, period.duration, now)
			require.False(t, ok, "相同时间再次维护不能重置第二次")
			next, ok := sub.automaticWindowStartAt(&start, period.duration, anchor.Add(4*period.duration))
			require.True(t, ok)
			require.Equal(t, anchor.Add(4*period.duration), next)
		})
	}
}

func TestUpstreamSubscriptionReset_InitialLegacyAndLaterManualAnchors(t *testing.T) {
	startsAt := upstreamResetTestTime(time.September, 1, 10, 18)
	initialMidnight := timezone.StartOfDay(startsAt)
	for _, duration := range []time.Duration{subscriptionWeeklyWindow, subscriptionMonthlyWindow} {
		for _, test := range []struct {
			name   string
			anchor time.Time
			want   time.Time
		}{
			{"初始同日零点", initialMidnight, startsAt},
			{"后续手动零点", initialMidnight.AddDate(0, 0, 2), initialMidnight.AddDate(0, 0, 2)},
			{"后续非零点", startsAt.AddDate(0, 0, 2).Add(time.Minute), startsAt.AddDate(0, 0, 2).Add(time.Minute)},
			{"更早历史零点", initialMidnight.AddDate(0, 0, -1), initialMidnight.AddDate(0, 0, -1)},
		} {
			t.Run(duration.String()+"/"+test.name, func(t *testing.T) {
				sub := &UserSubscription{StartsAt: startsAt, ExpiresAt: startsAt.Add(100 * 24 * time.Hour)}
				require.Equal(t, test.want, sub.windowResetAnchor(test.anchor))
				due := test.want.Add(duration)
				_, early := sub.automaticWindowStartAt(&test.anchor, duration, due.Add(-time.Nanosecond))
				require.False(t, early)
				got, ok := sub.automaticWindowStartAt(&test.anchor, duration, due)
				require.True(t, ok)
				require.Equal(t, due, got)
				sub.WeeklyWindowStart, sub.MonthlyWindowStart = &test.anchor, &test.anchor
				resetTime := sub.WeeklyResetTime()
				if duration == subscriptionMonthlyWindow {
					resetTime = sub.MonthlyResetTime()
				}
				require.Equal(t, due, *resetTime, "展示时间和维护必须使用同一个修正后的锚点")
			})
		}
	}
}

func TestUpstreamSubscriptionReset_StrictExpiryBoundary(t *testing.T) {
	anchor := upstreamResetTestTime(time.September, 1, 10, 18)
	for _, duration := range []time.Duration{subscriptionWeeklyWindow, subscriptionMonthlyWindow} {
		for _, remaining := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond, time.Hour} {
			t.Run(duration.String()+"/"+remaining.String(), func(t *testing.T) {
				due := anchor.Add(duration)
				sub := &UserSubscription{StartsAt: anchor, ExpiresAt: due.Add(remaining)}
				_, ok := sub.automaticWindowStartAt(&anchor, duration, due)
				require.Equal(t, remaining > 0, ok, "只有重置点严格早于有效期终点才刷新")
				_, ok = sub.automaticWindowStartAt(&anchor, duration, sub.ExpiresAt)
				require.False(t, ok, "订阅已经到期时不再维护额度")
			})
		}
	}
}

func TestUpstreamSubscriptionReset_InitialTailActivationPreservesUsage(t *testing.T) {
	now := upstreamResetTestTime(time.September, 23, 10, 18)
	limit := 100.0
	sub := &UserSubscription{
		Status: SubscriptionStatusActive, StartsAt: now.AddDate(0, 0, -40), ExpiresAt: now.Add(time.Minute),
		DailyLimitUSD: &limit, WeeklyLimitUSD: &limit, MonthlyLimitUSD: &limit,
		DailyUsageUSD: 12, WeeklyUsageUSD: 45, MonthlyUsageUSD: 90,
	}
	require.True(t, sub.NormalizeQuotaWindowsAt(now))
	require.Equal(t, timezone.StartOfDay(now), *sub.DailyWindowStart)
	require.Equal(t, now, *sub.WeeklyWindowStart)
	require.Equal(t, now, *sub.MonthlyWindowStart)
	require.Equal(t, 12.0, sub.DailyUsageUSD)
	require.Equal(t, 45.0, sub.WeeklyUsageUSD)
	require.Equal(t, 90.0, sub.MonthlyUsageUSD, "首次补齐空锚点不能清零旧用量")
	require.False(t, sub.NormalizeQuotaWindowsAt(now))
}

// 服务接口使用实际当前时间，虚拟时钟避免秒级误差和跨零点导致偶发失败。
func TestUpstreamSubscriptionReset_ServiceActivationUsesExactNow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(17 * time.Minute)
		now := time.Now()
		limit := 100.0
		sub := &UserSubscription{ID: 1, StartsAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Minute), WeeklyLimitUSD: &limit, MonthlyLimitUSD: &limit}
		repo := &dailyResetTrackingUserSubRepo{}
		svc := &SubscriptionService{userSubRepo: repo}
		require.NoError(t, svc.CheckAndActivateWindow(context.Background(), sub))
		require.Equal(t, now, repo.lastActivationAt)
		require.True(t, repo.lastActivation.Weekly)
		require.True(t, repo.lastActivation.Monthly)
	})
}

// 只记录服务传给仓储的重置参数，避免测试桩自行对齐时间掩盖服务层回归。
type upstreamResetClockRepo struct {
	userSubRepoNoop
	sub         *UserSubscription
	resetAt     time.Time
	resetDaily  bool
	resetWeekly bool
	resetMonth  bool
}

func (r *upstreamResetClockRepo) GetByID(context.Context, int64) (*UserSubscription, error) {
	return r.sub, nil
}

func (r *upstreamResetClockRepo) ResetUsageWindows(_ context.Context, _ int64, daily, weekly, monthly bool, at time.Time) error {
	r.resetAt = at
	r.resetDaily, r.resetWeekly, r.resetMonth = daily, weekly, monthly
	return nil
}

func TestUpstreamSubscriptionReset_ServiceManualResetUsesExactNow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(17 * time.Minute)
		now := time.Now()
		repo := &upstreamResetClockRepo{sub: &UserSubscription{ID: 13}}
		svc := &SubscriptionService{userSubRepo: repo}
		result, err := svc.AdminResetQuota(context.Background(), 13, true, true, true)
		require.NoError(t, err)
		require.Same(t, repo.sub, result)
		require.Equal(t, now, repo.resetAt, "管理员重置周/月额度应传递精确时刻，日窗口由仓储单独对齐")
		require.NotEqual(t, timezone.StartOfDay(now), repo.resetAt)
		require.True(t, repo.resetDaily)
		require.True(t, repo.resetWeekly)
		require.True(t, repo.resetMonth)
	})
}
