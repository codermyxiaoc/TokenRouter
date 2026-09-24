//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

// 使用虚拟时钟固定在项目时区中午，避免临期用例依赖测试执行时间。
func subscriptionResetCountAtNoon(t *testing.T, check func(*testing.T, time.Time)) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Until(timezone.StartOfDay(time.Now()).AddDate(0, 0, 1).Add(12 * time.Hour)))
		check(t, time.Now())
	})
}

// 统一取得同一个周期的窗口、额度、用量和次数，避免表驱动断言漏掉某一维度。
func subscriptionResetCountFields(sub *UserSubscription, kind string) (**time.Time, **float64, *float64, *int64) {
	switch kind {
	case "daily":
		return &sub.DailyWindowStart, &sub.DailyLimitUSD, &sub.DailyUsageUSD, &sub.DailyResetCount
	case "weekly":
		return &sub.WeeklyWindowStart, &sub.WeeklyLimitUSD, &sub.WeeklyUsageUSD, &sub.WeeklyResetCount
	default:
		return &sub.MonthlyWindowStart, &sub.MonthlyLimitUSD, &sub.MonthlyUsageUSD, &sub.MonthlyResetCount
	}
}

// 旧快照没有计数水位时只保留已有次数，不能凭过期锚点推算升级前历史。
func TestSubscriptionResetCount_LegacyBaselineDueProjection(t *testing.T) {
	for _, window := range []struct {
		kind string
		days int
	}{{"daily", 1}, {"weekly", 7}, {"monthly", 30}} {
		for _, used := range []float64{0, 6.25} {
			for _, periods := range []int{1, 3} {
				t.Run(fmt.Sprintf("%s/usage_%g/periods_%d", window.kind, used, periods), func(t *testing.T) {
					subscriptionResetCountAtNoon(t, func(t *testing.T, now time.Time) {
						source := UserSubscription{
							ID: 1, StartsAt: now.AddDate(0, 0, -180), ExpiresAt: now.AddDate(0, 0, 90),
							DailyResetCount: 7, WeeklyResetCount: 11, MonthlyResetCount: 13,
						}
						anchor, limit, usage, count := subscriptionResetCountFields(&source, window.kind)
						old := timezone.StartOfDay(now).AddDate(0, 0, -window.days*periods)
						originalWindow := old
						amount := 10.0
						*anchor, *limit, *usage, *count = &old, &amount, used, int64(1)<<33
						original := source
						projected := []UserSubscription{source}

						normalizeExpiredWindows(projected)

						expected := source
						expectedAnchor, _, expectedUsage, _ := subscriptionResetCountFields(&expected, window.kind)
						*expectedAnchor, *expectedUsage = nil, 0
						require.Equal(t, expected, projected[0], "到期用量可临时归零，但缺少计数水位时不能推算历史次数")
						require.Equal(t, original, source, "投影不能修改仓储读取的源快照")
						require.Equal(t, originalWindow, **anchor, "共享的窗口时间指针不能被投影修改")

						// 重复处理同一返回值和重新读取同一持久化快照都不能继续虚增次数。
						normalizeExpiredWindows(projected)
						require.Equal(t, expected, projected[0])
						reread := []UserSubscription{source}
						normalizeExpiredWindows(reread)
						require.Equal(t, projected, reread)
						require.Equal(t, original, source)
					})
				})
			}
		}
	}
}

func TestSubscriptionResetCount_UnactivatedAndCurrentWindows(t *testing.T) {
	for _, kind := range []string{"daily", "weekly", "monthly"} {
		for _, state := range []string{"unactivated", "unactivated_with_usage", "current"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				subscriptionResetCountAtNoon(t, func(t *testing.T, now time.Time) {
					source := UserSubscription{ID: 2, StartsAt: now.AddDate(0, 0, -90), ExpiresAt: now.AddDate(0, 0, 90)}
					anchor, limit, usage, count := subscriptionResetCountFields(&source, kind)
					amount := 10.0
					*limit = &amount
					if state == "unactivated_with_usage" {
						*usage = 3.5
					}
					if state == "current" {
						start := timezone.StartOfDay(now)
						*anchor, *usage, *count = &start, 3.5, 8
					}
					projected := []UserSubscription{source}
					normalizeExpiredWindows(projected)
					require.Equal(t, source, projected[0], "首次激活前和当前未到期窗口都不得计次或清除旧用量")
				})
			})
		}
	}
}

func TestSubscriptionResetCount_TailResetsWithoutOuterLimit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		kind       string
		days       int
		outerKind  string
		outerLimit float64
		wantReset  bool
	}{
		{"daily_without_outer", "daily", 1, "", 0, true},
		{"daily_zero_outer", "daily", 1, "weekly", 0, true},
		{"daily_negative_outer", "daily", 1, "weekly", -1, true},
		{"daily_weekly_protection", "daily", 1, "weekly", 50, true},
		{"daily_monthly_protection", "daily", 1, "monthly", 50, true},
		{"weekly_without_outer", "weekly", 7, "", 0, true},
		{"weekly_zero_outer", "weekly", 7, "monthly", 0, true},
		{"weekly_negative_outer", "weekly", 7, "monthly", -1, true},
		{"weekly_monthly_protection", "weekly", 7, "monthly", 50, true},
		{"monthly_without_full_window", "monthly", 30, "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subscriptionResetCountAtNoon(t, func(t *testing.T, now time.Time) {
				source := UserSubscription{
					ID: 3, StartsAt: now.AddDate(0, 0, -90), ExpiresAt: timezone.StartOfDay(now).Add(20 * time.Hour),
					DailyResetCount: 3, WeeklyResetCount: 5, MonthlyResetCount: 7,
				}
				anchor, limit, used, _ := subscriptionResetCountFields(&source, tc.kind)
				old := timezone.StartOfDay(now).AddDate(0, 0, -tc.days)
				amount := 10.0
				*anchor, *limit, *used = &old, &amount, 4.25
				if tc.outerKind != "" {
					_, outerLimit, _, _ := subscriptionResetCountFields(&source, tc.outerKind)
					*outerLimit = &tc.outerLimit
				}
				original := source
				projected := []UserSubscription{source}
				normalizeExpiredWindows(projected)

				expected := source
				if tc.wantReset {
					expectedAnchor, _, expectedUsage, _ := subscriptionResetCountFields(&expected, tc.kind)
					*expectedAnchor, *expectedUsage = nil, 0
				}
				require.Equal(t, expected, projected[0], "临期可用量正常刷新，无水位的旧快照保留已有次数")
				require.Equal(t, original, source)
			})
		})
	}
}

func TestSubscriptionResetCount_OneTimeDailyAndExpired(t *testing.T) {
	for _, state := range []string{"one_time_daily", "one_time_daily_with_outer", "expired"} {
		t.Run(state, func(t *testing.T) {
			subscriptionResetCountAtNoon(t, func(t *testing.T, now time.Time) {
				old := timezone.StartOfDay(now).AddDate(0, 0, -1)
				limit := 10.0
				source := UserSubscription{
					ID: 4, StartsAt: now.Add(-18 * time.Hour), ExpiresAt: now.Add(6 * time.Hour),
					DailyWindowStart: &old, DailyLimitUSD: &limit, DailyUsageUSD: 4.5, DailyResetCount: 6,
				}
				if state == "one_time_daily_with_outer" {
					source.WeeklyLimitUSD, source.MonthlyLimitUSD = &limit, &limit
				}
				if state == "expired" {
					old = timezone.StartOfDay(now).AddDate(0, 0, -90)
					source.StartsAt, source.ExpiresAt = now.AddDate(0, 0, -100), now.Add(-time.Second)
					source.WeeklyWindowStart, source.MonthlyWindowStart = &old, &old
					source.WeeklyLimitUSD, source.MonthlyLimitUSD = &limit, &limit
					source.WeeklyUsageUSD, source.MonthlyUsageUSD = 7.5, 8.5
					source.WeeklyResetCount, source.MonthlyResetCount = 10, 12
				}
				projected := []UserSubscription{source}
				normalizeExpiredWindows(projected)
				require.Equal(t, source, projected[0], "一次性日额度及已过期订阅不能因次数展示而获得额外重置")
			})
		})
	}
}

func TestSubscriptionResetCount_AdminResetPreservesCounts(t *testing.T) {
	for mask := 1; mask < 8; mask++ {
		t.Run(fmt.Sprintf("selected_%03b", mask), func(t *testing.T) {
			subscriptionResetCountAtNoon(t, func(t *testing.T, now time.Time) {
				old := timezone.StartOfDay(now).AddDate(0, 0, -31)
				source := UserSubscription{
					ID: 5, StartsAt: now.AddDate(0, 0, -90), ExpiresAt: now.AddDate(0, 0, 90),
					DailyWindowStart: &old, WeeklyWindowStart: &old, MonthlyWindowStart: &old,
					DailyUsageUSD: 2.5, WeeklyUsageUSD: 5.5, MonthlyUsageUSD: 8.5,
					DailyResetCount: int64(1)<<33 + 3, WeeklyResetCount: 7, MonthlyResetCount: 11,
				}
				stored := source
				stub := &resetQuotaUserSubRepoStub{sub: &stored}
				svc := newResetQuotaSvc(stub)
				daily, weekly, monthly := mask&1 != 0, mask&2 != 0, mask&4 != 0
				// 先查看已到期窗口，再执行手动重置，界面次数不能出现先增后减。
				beforeReset := []UserSubscription{source}
				normalizeExpiredWindows(beforeReset)

				result, err := svc.AdminResetQuota(context.Background(), source.ID, daily, weekly, monthly)

				require.NoError(t, err)
				require.Equal(t, daily, stub.resetDailyCalled)
				require.Equal(t, weekly, stub.resetWeeklyCalled)
				require.Equal(t, monthly, stub.resetMonthlyCalled)
				expected := source
				for i, kind := range []string{"daily", "weekly", "monthly"} {
					if mask&(1<<i) != 0 {
						anchor, _, used, _ := subscriptionResetCountFields(&expected, kind)
						start := now
						if kind == "daily" {
							start = startOfDay(now)
						}
						*anchor, *used = &start, 0
					}
				}
				require.Equal(t, expected, *result, "手动重置只改变选中的窗口和用量，三项累计次数全部保留")
				require.Equal(t, expected, stored)
				require.Equal(t, beforeReset[0].DailyResetCount, result.DailyResetCount)
				require.Equal(t, beforeReset[0].WeeklyResetCount, result.WeeklyResetCount)
				require.Equal(t, beforeReset[0].MonthlyResetCount, result.MonthlyResetCount)
				// 重复手动操作也不应清零或增加自动重置次数。
				repeated, err := svc.AdminResetQuota(context.Background(), source.ID, daily, weekly, monthly)
				require.NoError(t, err)
				require.Equal(t, result, repeated)
			})
		})
	}
}
