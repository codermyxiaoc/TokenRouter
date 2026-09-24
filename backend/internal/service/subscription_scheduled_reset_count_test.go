//go:build unit

package service

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

// 固定计划起点，日历日使用项目时区，避免测试依赖运行机器的当前日期。
func scheduledCountStart() time.Time {
	return time.Date(2026, time.September, 1, 0, 0, 0, 0, timezone.Location())
}

type scheduledCountPeriod struct {
	name string
	days int
}

var scheduledCountPeriods = []scheduledCountPeriod{{"daily", 1}, {"weekly", 7}, {"monthly", 30}}

// 每次只配置被验证的额度，确保各窗口的计划独立累计。
func scheduledCountSubscription(period string) UserSubscription {
	start, limit := scheduledCountStart(), 10.0
	sub := UserSubscription{
		ID: 91, UserID: 7, PlanID: 3, Status: SubscriptionStatusActive,
		StartsAt: start.Add(12 * time.Hour), ExpiresAt: start.AddDate(0, 0, 180),
		ResetCountedAt: start.Add(12 * time.Hour),
	}
	_, quota, _, _ := scheduledCountFields(&sub, period)
	*quota = &limit
	return sub
}

func scheduledCountFields(sub *UserSubscription, period string) (**time.Time, **float64, *float64, *int64) {
	switch period {
	case "daily":
		return &sub.DailyWindowStart, &sub.DailyLimitUSD, &sub.DailyUsageUSD, &sub.DailyResetCount
	case "weekly":
		return &sub.WeeklyWindowStart, &sub.WeeklyLimitUSD, &sub.WeeklyUsageUSD, &sub.WeeklyResetCount
	default:
		return &sub.MonthlyWindowStart, &sub.MonthlyLimitUSD, &sub.MonthlyUsageUSD, &sub.MonthlyResetCount
	}
}

func TestScheduledSubscriptionResetCount_UnusedAndCatchUp(t *testing.T) {
	for _, period := range scheduledCountPeriods {
		for _, activated := range []bool{false, true} {
			name := period.name + "/never_used"
			if activated {
				name = period.name + "/active_zero_usage"
			}
			t.Run(name, func(t *testing.T) {
				sub := scheduledCountSubscription(period.name)
				window, _, used, count := scheduledCountFields(&sub, period.name)
				anchor := scheduledCountStart()
				if activated {
					*window = &anchor
				}
				*count = int64(1)<<33 + 2
				now := anchor.AddDate(0, 0, 3*period.days)
				if period.name != "daily" {
					// 周/月尚未激活或保留初始旧零点时，均以精确生效时刻为计数基准。
					now = sub.StartsAt.AddDate(0, 0, 3*period.days)
				}
				before := sub

				require.True(t, sub.AccrueScheduledResetCounts(now))
				require.Equal(t, int64(1)<<33+5, *count, "三个符合规则的到点窗口必须累计三次，与是否产生用量无关")
				require.Zero(t, *used)
				require.Equal(t, now, sub.ResetCountedAt)
				expected := before
				_, _, _, expectedCount := scheduledCountFields(&expected, period.name)
				*expectedCount += 3
				expected.ResetCountedAt = now
				require.Equal(t, expected, sub, "计数维护不能改变用量、实际窗口、状态或有效期")
				require.False(t, sub.AccrueScheduledResetCounts(now), "同一水位重复维护不能多计")
				require.True(t, sub.AccrueScheduledResetCounts(now.Add(time.Minute)))
				require.Equal(t, int64(1)<<33+5, *count)
			})
		}
	}
}

func TestScheduledSubscriptionResetCount_ExactBoundaries(t *testing.T) {
	for _, period := range scheduledCountPeriods {
		t.Run(period.name, func(t *testing.T) {
			sub := scheduledCountSubscription(period.name)
			_, _, _, count := scheduledCountFields(&sub, period.name)
			due := scheduledCountStart().AddDate(0, 0, period.days)
			if period.name != "daily" {
				due = sub.StartsAt.AddDate(0, 0, period.days)
			}
			require.True(t, sub.AccrueScheduledResetCounts(due.Add(-time.Nanosecond)))
			require.Zero(t, *count)
			require.True(t, sub.AccrueScheduledResetCounts(due))
			require.Equal(t, int64(1), *count)
			require.True(t, sub.AccrueScheduledResetCounts(due.Add(time.Nanosecond)))
			require.Equal(t, int64(1), *count)
			last := sub
			require.False(t, sub.AccrueScheduledResetCounts(due.Add(-time.Hour)))
			require.Equal(t, last, sub, "时间回退不能回退水位或计数")
		})
	}
}

func TestScheduledSubscriptionResetCount_StrictExpiryBoundary(t *testing.T) {
	for _, period := range scheduledCountPeriods {
		for _, exactFull := range []bool{false, true} {
			name := period.name + "/at_expiry"
			if exactFull {
				name = period.name + "/exactly_one_remaining_window"
			}
			t.Run(name, func(t *testing.T) {
				sub := scheduledCountSubscription(period.name)
				sub.StartsAt, sub.ResetCountedAt = scheduledCountStart(), scheduledCountStart()
				sub.Status = SubscriptionStatusExpired
				due := scheduledCountStart().AddDate(0, 0, period.days)
				sub.ExpiresAt = due
				want := int64(0)
				if exactFull {
					sub.ExpiresAt = due.AddDate(0, 0, period.days)
					want = 1
				}
				_, _, _, count := scheduledCountFields(&sub, period.name)
				require.True(t, sub.AccrueScheduledResetCounts(sub.ExpiresAt.AddDate(0, 0, 90)))
				require.Equal(t, want, *count, "可结算已过期订阅，但到期时刻本身不能再发一个窗口")
			})
		}
	}
}

func TestScheduledSubscriptionResetCount_TailIndependentOfOuterLimits(t *testing.T) {
	for _, tc := range []struct {
		name, period, outer string
		outerLimit          float64
		want                int64
	}{
		{"daily_without_outer", "daily", "", 0, 1},
		{"daily_zero_weekly", "daily", "weekly", 0, 1},
		{"daily_negative_weekly", "daily", "weekly", -1, 1},
		{"daily_finite_weekly", "daily", "weekly", 10, 1},
		{"daily_finite_monthly", "daily", "monthly", 10, 1},
		{"weekly_without_outer", "weekly", "", 0, 1},
		{"weekly_zero_monthly", "weekly", "monthly", 0, 1},
		{"weekly_negative_monthly", "weekly", "monthly", -1, 1},
		{"weekly_finite_monthly", "weekly", "monthly", 10, 1},
		{"monthly_has_no_outer", "monthly", "daily", 10, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub := scheduledCountSubscription(tc.period)
			periodDays := map[string]int{"daily": 1, "weekly": 7, "monthly": 30}[tc.period]
			due := scheduledCountStart().AddDate(0, 0, periodDays)
			sub.StartsAt, sub.ResetCountedAt = scheduledCountStart(), scheduledCountStart()
			sub.ExpiresAt = due.Add(6 * time.Hour)
			if tc.outer != "" {
				_, limit, _, _ := scheduledCountFields(&sub, tc.outer)
				*limit = &tc.outerLimit
			}
			sub.AccrueScheduledResetCounts(sub.ExpiresAt.Add(time.Hour))
			_, _, _, count := scheduledCountFields(&sub, tc.period)
			require.Equal(t, tc.want, *count, "到期前已到达的尾段重置点不再受外层额度限制")
		})
	}
}

func TestScheduledSubscriptionResetCount_HistoricalNonMidnightAnchor(t *testing.T) {
	for _, period := range scheduledCountPeriods[1:] {
		for _, remainingTooShort := range []bool{false, true} {
			name := period.name + "/exact_full_window"
			if remainingTooShort {
				name = period.name + "/insufficient_tail"
			}
			t.Run(name, func(t *testing.T) {
				sub := scheduledCountSubscription(period.name)
				sub.StartsAt, sub.ResetCountedAt = scheduledCountStart(), scheduledCountStart()
				anchor := scheduledCountStart().Add(8 * time.Hour)
				window, _, used, count := scheduledCountFields(&sub, period.name)
				*window, *used = &anchor, 3.25
				due := anchor.AddDate(0, 0, period.days)
				sub.ExpiresAt = timezone.StartOfDay(due).AddDate(0, 0, period.days)
				if remainingTooShort {
					sub.ExpiresAt = sub.ExpiresAt.Add(-time.Nanosecond)
				}

				// 原始 08:00 锚点必须等到 08:00，与额度维护保持相同节奏。
				sub.AccrueScheduledResetCounts(timezone.StartOfDay(due))
				require.Zero(t, *count)
				sub.AccrueScheduledResetCounts(due.Add(-time.Nanosecond))
				require.Zero(t, *count)
				actualResetAllowed := sub.NeedsWeeklyResetAt(due)
				if period.name == "monthly" {
					actualResetAllowed = sub.NeedsMonthlyResetAt(due)
				}
				require.True(t, actualResetAllowed, "剩余时间不足完整周期仍应按时刷新")
				sub.AccrueScheduledResetCounts(due)
				require.Equal(t, int64(1), *count, "尾段计数资格必须与实际额度维护一致")
				require.Equal(t, anchor, **window, "计数不能把历史实际窗口重锚到零点")
				require.Equal(t, 3.25, *used)
				require.False(t, sub.AccrueScheduledResetCounts(due))
			})
		}
	}
}

func TestScheduledSubscriptionResetCount_AnchorBeforeCurrentTerm(t *testing.T) {
	for _, period := range scheduledCountPeriods[1:] {
		t.Run(period.name, func(t *testing.T) {
			sub := scheduledCountSubscription(period.name)
			anchor := scheduledCountStart().AddDate(0, 0, -2).Add(8 * time.Hour)
			window, _, _, count := scheduledCountFields(&sub, period.name)
			*window = &anchor
			due := anchor.AddDate(0, 0, period.days)
			sub.AccrueScheduledResetCounts(due.Add(-time.Nanosecond))
			require.Zero(t, *count, "继承的实际锚点与额度维护保持一致，只统计本期开始后的到点周期")
			sub.AccrueScheduledResetCounts(due)
			require.Equal(t, int64(1), *count)
			require.Equal(t, anchor, **window)
		})
	}
}

func TestScheduledSubscriptionResetCount_OneTimeDaily(t *testing.T) {
	for _, hours := range []int{6, 24} {
		t.Run((time.Duration(hours) * time.Hour).String(), func(t *testing.T) {
			sub := scheduledCountSubscription("daily")
			sub.ExpiresAt = sub.StartsAt.Add(time.Duration(hours) * time.Hour)
			outer := 100.0
			sub.WeeklyLimitUSD, sub.MonthlyLimitUSD = &outer, &outer
			sub.AccrueScheduledResetCounts(sub.ExpiresAt.AddDate(0, 0, 60))
			require.Zero(t, sub.DailyResetCount, "短日卡即使跨零点且存在外层额度，也不能新增刷新次数")
		})
	}
}

func TestScheduledSubscriptionResetCount_UnconfiguredLimits(t *testing.T) {
	for _, quota := range []*float64{nil, scheduledCountFloat(0), scheduledCountFloat(-1)} {
		sub := scheduledCountSubscription("daily")
		sub.DailyLimitUSD, sub.WeeklyLimitUSD, sub.MonthlyLimitUSD = quota, quota, quota
		sub.DailyResetCount, sub.WeeklyResetCount, sub.MonthlyResetCount = 3, 4, 5
		sub.AccrueScheduledResetCounts(sub.StartsAt.AddDate(0, 0, 90))
		require.Equal(t, int64(3), sub.DailyResetCount)
		require.Equal(t, int64(4), sub.WeeklyResetCount)
		require.Equal(t, int64(5), sub.MonthlyResetCount)
	}
}

func scheduledCountFloat(value float64) *float64 { return &value }

func TestScheduledSubscriptionResetCount_FutureAndInactive(t *testing.T) {
	for _, state := range []string{"future", "suspended", "revoked", "deleted"} {
		t.Run(state, func(t *testing.T) {
			sub := scheduledCountSubscription("daily")
			now := scheduledCountStart().AddDate(0, 0, 3)
			switch state {
			case "future":
				sub.Status, sub.StartsAt = SubscriptionStatusPending, now.AddDate(0, 0, 1)
			case "suspended":
				sub.Status = SubscriptionStatusSuspended
			case "revoked":
				sub.Status = SubscriptionStatusRevoked
			case "deleted":
				sub.DeletedAt = &now
			}
			require.True(t, sub.AccrueScheduledResetCounts(now))
			require.Zero(t, sub.DailyResetCount)
			require.Equal(t, now, sub.ResetCountedAt)
			if state != "future" {
				// 恢复后只能统计恢复水位以后的重置点，不补算停用期间。
				sub.Status, sub.DeletedAt = SubscriptionStatusActive, nil
				sub.AccrueScheduledResetCounts(now.AddDate(0, 0, 1))
				require.Equal(t, int64(1), sub.DailyResetCount)
			}
		})
	}
}

func TestScheduledSubscriptionResetCount_ZeroWatermarkOnlyEstablishesBaseline(t *testing.T) {
	sub := scheduledCountSubscription("daily")
	sub.ResetCountedAt = time.Time{}
	sub.DailyResetCount = 12
	now := scheduledCountStart().AddDate(0, 0, 20)
	require.True(t, sub.AccrueScheduledResetCounts(now))
	require.Equal(t, int64(12), sub.DailyResetCount)
	require.Equal(t, now, sub.ResetCountedAt)
	sub.AccrueScheduledResetCounts(now.AddDate(0, 0, 1))
	require.Equal(t, int64(13), sub.DailyResetCount)
	before := sub
	require.False(t, sub.AccrueScheduledResetCounts(time.Time{}))
	require.Equal(t, before, sub)
	var missing *UserSubscription
	require.False(t, missing.AccrueScheduledResetCounts(now))
}

func TestScheduledSubscriptionResetCount_ExtensionDoesNotBackfillInvalidPast(t *testing.T) {
	for _, period := range scheduledCountPeriods {
		t.Run(period.name, func(t *testing.T) {
			sub := scheduledCountSubscription(period.name)
			sub.StartsAt, sub.ResetCountedAt = scheduledCountStart(), scheduledCountStart()
			due := scheduledCountStart().AddDate(0, 0, period.days)
			sub.ExpiresAt = due
			now := due.Add(6 * time.Hour)
			_, _, _, count := scheduledCountFields(&sub, period.name)
			// 管理员变更必须先用旧有效期结算到操作时间，再写入新有效期。
			sub.AccrueScheduledResetCounts(now)
			require.Zero(t, *count)
			sub.ExpiresAt = due.AddDate(0, 0, 4*period.days)
			sub.AccrueScheduledResetCounts(now.Add(time.Nanosecond))
			require.Zero(t, *count, "延期不能把旧有效期到期后已过去的时点追补成有效次数")
			sub.AccrueScheduledResetCounts(due.AddDate(0, 0, period.days))
			require.Equal(t, int64(1), *count, "延期后新到达的合法重置点恢复计次")
		})
	}
}

func TestScheduledSubscriptionResetCount_ChangedAnchorsKeepPriorCounts(t *testing.T) {
	for _, period := range scheduledCountPeriods {
		t.Run(period.name, func(t *testing.T) {
			sub := scheduledCountSubscription(period.name)
			operationAt := scheduledCountStart().AddDate(0, 0, 2*period.days).Add(12 * time.Hour)
			sub.AccrueScheduledResetCounts(operationAt)
			window, _, _, count := scheduledCountFields(&sub, period.name)
			require.Equal(t, int64(2), *count)
			anchor := timezone.StartOfDay(operationAt)
			*window = &anchor
			sub.AccrueScheduledResetCounts(operationAt.Add(time.Second))
			require.Equal(t, int64(2), *count, "首次实际激活或手动重置窗口本身不额外计次")
			sub.AccrueScheduledResetCounts(anchor.AddDate(0, 0, period.days))
			require.Equal(t, int64(3), *count)
		})
	}
}

func TestScheduledSubscriptionResetCount_ListProjectionIsRepeatable(t *testing.T) {
	for _, period := range scheduledCountPeriods {
		t.Run(period.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				now := time.Now()
				anchor := timezone.StartOfDay(now).AddDate(0, 0, -3*period.days)
				source := scheduledCountSubscription(period.name)
				source.StartsAt, source.ResetCountedAt, source.ExpiresAt = anchor, anchor, now.AddDate(0, 0, 90)
				window, _, _, count := scheduledCountFields(&source, period.name)
				*window, *count = &anchor, 9
				original := source
				repo := newSubscriptionUserSubRepoStub()
				repo.byID[source.ID] = &source
				svc := &SubscriptionService{userSubRepo: repo}

				first, err := svc.ListUserSubscriptions(context.Background(), source.UserID)
				require.NoError(t, err)
				require.Len(t, first, 1)
				_, _, _, projectedCount := scheduledCountFields(&first[0], period.name)
				require.Equal(t, int64(12), *projectedCount)
				require.Equal(t, original, source, "返回计数投影不能写回仓储源快照或共享的时间指针")

				second, err := svc.ListUserSubscriptions(context.Background(), source.UserID)
				require.NoError(t, err)
				require.Equal(t, first, second)
				normalizeExpiredWindows(second)
				require.Equal(t, first, second, "重复读取或再次归一化同一响应不能重复计次")
				active, err := svc.ListActiveUserSubscriptions(context.Background(), source.UserID)
				require.NoError(t, err)
				require.Equal(t, first, active)
				require.Equal(t, original, source)
			})
		})
	}
}
