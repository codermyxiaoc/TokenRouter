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

// focusWindowFields 统一选择额度窗口，确保日、周、月执行完全相同的生命周期断言。
func focusWindowFields(sub *UserSubscription, kind string) (**time.Time, **float64, *float64) {
	switch kind {
	case "daily":
		return &sub.DailyWindowStart, &sub.DailyLimitUSD, &sub.DailyUsageUSD
	case "weekly":
		return &sub.WeeklyWindowStart, &sub.WeeklyLimitUSD, &sub.WeeklyUsageUSD
	default:
		return &sub.MonthlyWindowStart, &sub.MonthlyLimitUSD, &sub.MonthlyUsageUSD
	}
}

func focusProgressWindow(progress *SubscriptionProgress, kind string) *UsageWindowProgress {
	switch kind {
	case "daily":
		return progress.Daily
	case "weekly":
		return progress.Weekly
	default:
		return progress.Monthly
	}
}

// 使用虚拟时钟真正走过未来的日、周、月边界，不仅检查延期之后窗口是否非空。
func TestSubscriptionFocus_ExtensionRestoresFullWindowLifecycle(t *testing.T) {
	for _, window := range []struct {
		kind string
		days int
	}{{"daily", 1}, {"weekly", 7}, {"monthly", 30}} {
		for _, operation := range []string{"extend", "set_validity"} {
			for _, stale := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stale_%v", window.kind, operation, stale), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						ctx := context.Background()
						// 统一从当地当天中午开始，排除运行机器当前时间对临期场景的影响。
						time.Sleep(time.Until(timezone.StartOfDay(time.Now()).AddDate(0, 0, 1).Add(12 * time.Hour)))
						now := time.Now()
						repo := newExtensionWindowRepo()
						sub := &UserSubscription{ID: 1, UserID: 1, PlanID: 1, Status: SubscriptionStatusActive,
							StartsAt: now.AddDate(0, 0, -90), ExpiresAt: timezone.StartOfDay(now).Add(20 * time.Hour)}
						anchor, limit, used := focusWindowFields(sub, window.kind)
						amount := 10.0
						*limit, *used = &amount, 3
						if stale {
							old := timezone.StartOfDay(now).AddDate(0, 0, -window.days)
							*anchor = &old
						}
						repo.seed(sub)
						client := newPaymentConfigServiceTestClient(t)
						svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, client, nil)
						before, err := svc.EnsureWindowMaintenance(ctx, sub)
						require.NoError(t, err)
						_, _, beforeUsage := focusWindowFields(before, window.kind)
						require.Equal(t, 3.0, *beforeUsage, "临期且没有外层额度保护时不能额外赠送新窗口")
						beforeProgress, err := svc.GetSubscriptionProgress(ctx, sub.ID)
						require.NoError(t, err)
						if !stale {
							require.Nil(t, focusProgressWindow(beforeProgress, window.kind), "尚未启动的迁移窗口不应伪造重置时间")
						}

						var extended *UserSubscription
						// 保留至少两个完整窗口，确保首个重置不是恰好落在订阅到期时。
						extensionDays := window.days*2 + 2
						if operation == "extend" {
							extended, err = svc.ExtendSubscription(ctx, sub.ID, extensionDays)
						} else {
							extended, err = svc.SetSubscriptionValidityDays(ctx, sub.ID, extensionDays)
						}
						require.NoError(t, err)
						currentAnchor, _, currentUsage := focusWindowFields(extended, window.kind)
						require.NotNil(t, *currentAnchor)
						require.True(t, (*currentAnchor).Equal(timezone.StartOfDay(now)))
						if stale {
							require.Zero(t, *currentUsage, "已到期旧窗口延期后立即推进并清零")
						} else {
							require.Equal(t, 3.0, *currentUsage, "NULL 窗口首次激活必须保留真实旧用量")
						}
						progress, err := svc.GetSubscriptionProgress(ctx, sub.ID)
						require.NoError(t, err)
						visible := focusProgressWindow(progress, window.kind)
						require.NotNil(t, visible)
						resetAt := timezone.StartOfDay(now).AddDate(0, 0, window.days)
						require.True(t, resetAt.Equal(visible.ResetsAt), "管理界面使用的进度接口应立即给出正确重置时刻")
						require.Greater(t, visible.ResetsInSeconds, int64(0))

						// 模拟延期后继续产生消费，再推进到显示的重置时间前一纳秒。
						_, _, persistedUsage := focusWindowFields(repo.byID[sub.ID], window.kind)
						*persistedUsage = 6.25
						time.Sleep(time.Until(resetAt) - time.Nanosecond)
						fresh, err := repo.GetByID(ctx, sub.ID)
						require.NoError(t, err)
						fresh, err = svc.EnsureWindowMaintenance(ctx, fresh)
						require.NoError(t, err)
						_, _, notDueUsage := focusWindowFields(fresh, window.kind)
						require.Equal(t, 6.25, *notDueUsage, "到点前不能提前清空消费")
						time.Sleep(time.Nanosecond)
						fresh, err = svc.EnsureWindowMaintenance(ctx, fresh)
						require.NoError(t, err)
						afterAnchor, _, afterUsage := focusWindowFields(fresh, window.kind)
						require.Zero(t, *afterUsage, "到点必须真正清零，而不仅改变显示")
						require.True(t, (*afterAnchor).Equal(resetAt))
						afterProgress, err := svc.GetSubscriptionProgress(ctx, sub.ID)
						require.NoError(t, err)
						require.True(t, focusProgressWindow(afterProgress, window.kind).ResetsAt.After(resetAt))

						// 维护重复执行不得抹掉新窗口中的新消费。
						_, _, persistedUsage = focusWindowFields(repo.byID[sub.ID], window.kind)
						*persistedUsage = 1.5
						fresh, err = repo.GetByID(ctx, sub.ID)
						require.NoError(t, err)
						fresh, err = svc.EnsureWindowMaintenance(ctx, fresh)
						require.NoError(t, err)
						_, _, repeatedUsage := focusWindowFields(fresh, window.kind)
						require.Equal(t, 1.5, *repeatedUsage)
						time.Sleep(time.Until(fresh.ExpiresAt))
						_, err = svc.ValidateAndCheckLimits(fresh, nil)
						require.ErrorIs(t, err, ErrSubscriptionExpired, "到期时刻本身不能继续消费或再获一个新窗口")
					})
				})
			}
		}
	}
}

// 完整窗口边界以当前窗口零点计算；刚好容纳一个窗口只允许使用至到期，不赠送下一窗口。
func TestSubscriptionFocus_FullWindowThresholdAndTailProtection(t *testing.T) {
	base := timezone.StartOfDay(time.Now())
	for _, tc := range []struct {
		kind string
		days int
	}{{"daily", 1}, {"weekly", 7}, {"monthly", 30}} {
		for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
			t.Run(fmt.Sprintf("%s/%s", tc.kind, delta), func(t *testing.T) {
				sub := &UserSubscription{StartsAt: base.AddDate(0, 0, -90), ExpiresAt: base.AddDate(0, 0, tc.days).Add(delta)}
				_, limit, _ := focusWindowFields(sub, tc.kind)
				amount := 10.0
				*limit = &amount
				activation := sub.WindowActivationAt(base)
				var enabled bool
				switch tc.kind {
				case "daily":
					enabled = activation.Daily
				case "weekly":
					enabled = activation.Weekly
				default:
					enabled = activation.Monthly
				}
				require.Equal(t, delta >= 0, enabled)
			})
		}
	}
	for _, outer := range []float64{-1, 0, 50} {
		t.Run(fmt.Sprintf("outer_limit_%g", outer), func(t *testing.T) {
			limit := 10.0
			sub := &UserSubscription{StartsAt: base.AddDate(0, 0, -90), ExpiresAt: base.Add(12 * time.Hour), DailyLimitUSD: &limit, WeeklyLimitUSD: &outer}
			require.Equal(t, outer > 0, sub.WindowActivationAt(base).Daily)
			sub.DailyLimitUSD, sub.WeeklyLimitUSD, sub.MonthlyLimitUSD = nil, &limit, &outer
			require.Equal(t, outer > 0, sub.WindowActivationAt(base).Weekly)
		})
	}
}
