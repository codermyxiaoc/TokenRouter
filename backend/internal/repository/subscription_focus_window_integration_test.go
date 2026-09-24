//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// focusPersistedWindow 用相同断言验证三个窗口，避免只验证日窗口漏掉周、月逻辑。
func focusPersistedWindow(sub *service.UserSubscription, kind string) (*time.Time, float64) {
	switch kind {
	case "daily":
		return sub.DailyWindowStart, sub.DailyUsageUSD
	case "weekly":
		return sub.WeeklyWindowStart, sub.WeeklyUsageUSD
	default:
		return sub.MonthlyWindowStart, sub.MonthlyUsageUSD
	}
}

func focusPersistedProgress(progress *service.SubscriptionProgress, kind string) *service.UsageWindowProgress {
	switch kind {
	case "daily":
		return progress.Daily
	case "weekly":
		return progress.Weekly
	default:
		return progress.Monthly
	}
}

// 在独立 PostgreSQL 中检查临期激活、管理延期、真实窗口推进及并发旧快照保护。
func TestSubscriptionFocus_PostgresExtensionResetLifecycle(t *testing.T) {
	for _, window := range []struct {
		kind string
		days int
	}{{"daily", 1}, {"weekly", 7}, {"monthly", 30}} {
		for _, operation := range []string{"extend", "set_validity", "bulk_extend"} {
			for _, stale := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stale_%v", window.kind, operation, stale), func(t *testing.T) {
					ctx := context.Background()
					client := testEntClient(t)
					user := mustCreateUser(t, client, &service.User{Email: "focus-" + uuid.NewString() + "@example.invalid"})
					plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "窗口专项", Price: 10, ValidityDays: 90, ValidityUnit: "day"})
					now := time.Now().Truncate(time.Microsecond)
					today := timezone.StartOfDay(now)
					// 即使剩余时长不足一个周期，只要订阅有效就应允许激活或推进已到点的窗口。
					expiry := now.Add(time.Hour)
					sub := &service.UserSubscription{UserID: user.ID, PlanID: plan.ID, StartsAt: today.AddDate(0, 0, -90), ExpiresAt: expiry}
					switch window.kind {
					case "daily":
						sub.DailyLimitUSD, sub.DailyUsageUSD = float64Ptr(10), 3
					case "weekly":
						sub.WeeklyLimitUSD, sub.WeeklyUsageUSD = float64Ptr(10), 3
					default:
						sub.MonthlyLimitUSD, sub.MonthlyUsageUSD = float64Ptr(10), 3
					}
					currentWindow := now.Add(-time.Hour)
					if window.kind == "daily" {
						currentWindow = today
					}
					oldWindow := currentWindow.AddDate(0, 0, -window.days*2)
					if stale {
						switch window.kind {
						case "daily":
							sub.DailyWindowStart = &oldWindow
						case "weekly":
							sub.WeeklyWindowStart = &oldWindow
						default:
							sub.MonthlyWindowStart = &oldWindow
						}
					}
					mustCreateSubscription(t, client, sub)
					repo := NewUserSubscriptionRepository(client)
					svc := service.NewSubscriptionService(nil, repo, nil, client, nil)
					activationStarted := time.Now()
					before, err := svc.EnsureWindowMaintenance(ctx, sub)
					require.NoError(t, err)
					activationFinished := time.Now()
					beforeAnchor, beforeUsed := focusPersistedWindow(before, window.kind)
					require.NotNil(t, beforeAnchor)
					if stale {
						require.Zero(t, beforeUsed, "已到点窗口无需等到有效期容纳下一完整周期才推进")
						require.True(t, currentWindow.Equal(*beforeAnchor), "旧窗口应按整数周期推进，不能改为请求当日午夜")
					} else {
						require.Equal(t, 3.0, beforeUsed, "首次激活不能抹掉迁移记录的历史用量")
						if window.kind == "daily" {
							require.True(t, today.Equal(*beforeAnchor))
						} else {
							require.False(t, beforeAnchor.Before(activationStarted.Add(-time.Microsecond)))
							require.False(t, beforeAnchor.After(activationFinished.Add(time.Microsecond)), "首次周、月窗口应锚定实际激活时刻")
						}
					}

					var extended *service.UserSubscription
					extensionDays := window.days*2 + 2
					switch operation {
					case "extend":
						extended, err = svc.ExtendSubscription(ctx, sub.ID, extensionDays)
					case "set_validity":
						extended, err = svc.SetSubscriptionValidityDays(ctx, sub.ID, extensionDays)
					default:
						var result *service.BulkSubscriptionResult
						result, err = svc.BulkExtendSubscriptions(ctx, []int64{sub.ID, sub.ID}, extensionDays)
						require.NoError(t, err)
						require.Equal(t, 1, result.UpdatedCount, "重复选中同一个订阅只能延长一次")
						extended, err = repo.GetByID(ctx, sub.ID)
					}
					require.NoError(t, err)
					if operation == "set_validity" {
						require.WithinDuration(t, now.AddDate(0, 0, extensionDays), extended.ExpiresAt, 5*time.Second)
					} else {
						require.True(t, expiry.AddDate(0, 0, extensionDays).Equal(extended.ExpiresAt))
					}
					anchor, used := focusPersistedWindow(extended, window.kind)
					require.NotNil(t, anchor)
					require.True(t, beforeAnchor.Equal(*anchor), "延期不能重新锚定已经激活的窗口")
					if stale {
						require.Zero(t, used)
					} else {
						require.Equal(t, 3.0, used, "首次激活不会抹掉历史累计费用")
					}
					progress, err := svc.GetSubscriptionProgress(ctx, sub.ID)
					require.NoError(t, err)
					visible := focusPersistedProgress(progress, window.kind)
					require.NotNil(t, visible)
					require.True(t, anchor.AddDate(0, 0, window.days).Equal(visible.ResetsAt))
					require.Greater(t, visible.ResetsInSeconds, int64(0))

					// 仅移动本测试记录的窗口锚点，等价于走过完整周期，真实执行生产维护和条件 UPDATE。
					_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("UPDATE user_subscriptions SET %s_window_start=$1, %s_usage_usd=$2 WHERE id=$3", window.kind, window.kind), oldWindow, 6.25, sub.ID)
					require.NoError(t, err)
					oldSnapshot, err := repo.GetByID(ctx, sub.ID)
					require.NoError(t, err)
					reset, err := svc.EnsureWindowMaintenance(ctx, oldSnapshot)
					require.NoError(t, err)
					resetAnchor, resetUsed := focusPersistedWindow(reset, window.kind)
					require.Zero(t, resetUsed, "到期窗口必须在 PostgreSQL 内实际清零")
					require.True(t, resetAnchor.Equal(currentWindow))
					require.True(t, reset.ExpiresAt.Equal(extended.ExpiresAt), "窗口重置不能改变订阅有效期")

					// 新窗口已扣费后，60 个持有相同旧窗口的在途请求不能再清空这笔新消费。
					_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("UPDATE user_subscriptions SET %s_usage_usd=$1 WHERE id=$2", window.kind), 1.5, sub.ID)
					require.NoError(t, err)
					var wg sync.WaitGroup
					start := make(chan struct{})
					errors := make(chan error, 60)
					for range 60 {
						wg.Add(1)
						go func() {
							defer wg.Done()
							<-start
							copy := *oldSnapshot
							// EnsureWindowMaintenance 会改写入参，重新还原为并发请求最初读取的旧锚点。
							switch window.kind {
							case "daily":
								copy.DailyWindowStart = &oldWindow
							case "weekly":
								copy.WeeklyWindowStart = &oldWindow
							default:
								copy.MonthlyWindowStart = &oldWindow
							}
							_, err := svc.EnsureWindowMaintenance(ctx, &copy)
							errors <- err
						}()
					}
					close(start)
					wg.Wait()
					close(errors)
					for err := range errors {
						require.NoError(t, err)
					}
					final, err := repo.GetByID(ctx, sub.ID)
					require.NoError(t, err)
					_, finalUsed := focusPersistedWindow(final, window.kind)
					require.Equal(t, 1.5, finalUsed, "并发旧快照不能重复重置新周期中的费用")
				})
			}
		}
	}
}

// 同套餐排队链在批量延期后仍无重叠，未来套餐不得提前启动或消耗额度。
func TestSubscriptionFocus_PostgresBulkExtensionPreservesPendingWindows(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: "chain-" + uuid.NewString() + "@example.invalid"})
	plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "时间链专项", Price: 10, ValidityDays: 30, ValidityUnit: "day"})
	now := time.Now().Truncate(time.Microsecond)
	expires := now.Add(time.Hour)
	active := mustCreateSubscription(t, client, &service.UserSubscription{UserID: user.ID, PlanID: plan.ID,
		StartsAt: now.AddDate(0, 0, -30), ExpiresAt: expires, MonthlyLimitUSD: float64Ptr(10), MonthlyUsageUSD: 2.5})
	pending := mustCreateSubscription(t, client, &service.UserSubscription{UserID: user.ID, PlanID: plan.ID,
		StartsAt: expires, ExpiresAt: expires.AddDate(0, 0, 30), Status: service.SubscriptionStatusPending, MonthlyLimitUSD: float64Ptr(10)})
	repo := NewUserSubscriptionRepository(client)
	svc := service.NewSubscriptionService(nil, repo, nil, client, nil)
	result, err := svc.BulkExtendSubscriptions(ctx, []int64{pending.ID, active.ID, pending.ID}, 35)
	require.NoError(t, err)
	require.Equal(t, 2, result.UpdatedCount)
	current, err := repo.GetByID(ctx, active.ID)
	require.NoError(t, err)
	next, err := repo.GetByID(ctx, pending.ID)
	require.NoError(t, err)
	require.True(t, current.ExpiresAt.Equal(expires.AddDate(0, 0, 35)))
	require.True(t, next.StartsAt.Equal(current.ExpiresAt))
	require.True(t, next.ExpiresAt.Equal(expires.AddDate(0, 0, 100)))
	require.Equal(t, service.SubscriptionStatusPending, next.Status)
	require.Nil(t, next.MonthlyWindowStart)
	require.NotNil(t, current.MonthlyWindowStart)
	require.Equal(t, 2.5, current.MonthlyUsageUSD)
}

// focusPausedExpiryRepository 只暂停延期写入，用于稳定复现读取旧快照后另一事务先完成重置与扣费的交错。
type focusPausedExpiryRepository struct {
	service.UserSubscriptionRepository
	entered chan struct{}
	resume  chan struct{}
}

func (r *focusPausedExpiryRepository) ExtendExpiry(ctx context.Context, id int64, expiry time.Time) error {
	close(r.entered)
	select {
	case <-r.resume:
		return r.UserSubscriptionRepository.ExtendExpiry(ctx, id, expiry)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// 延期不能用刚读取的过时窗口覆盖另一个事务已经重置并重新扣费后的用量。
func TestSubscriptionFocus_PostgresExtensionDoesNotEraseConcurrentConsumption(t *testing.T) {
	for _, kind := range []string{"daily", "weekly", "monthly"} {
		for _, operation := range []string{"set_validity", "bulk_extend"} {
			t.Run(kind+"/"+operation, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				client := testEntClient(t)
				user := mustCreateUser(t, client, &service.User{Email: "interleave-" + uuid.NewString() + "@example.invalid"})
				plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "延期扣费交错", Price: 10, ValidityDays: 90, ValidityUnit: "day"})
				now := time.Now().Truncate(time.Microsecond)
				old := timezone.StartOfDay(now).AddDate(0, 0, -35)
				sub := &service.UserSubscription{UserID: user.ID, PlanID: plan.ID, StartsAt: now.AddDate(0, 0, -90), ExpiresAt: now.Add(time.Hour)}
				switch kind {
				case "daily":
					sub.DailyLimitUSD, sub.DailyWindowStart, sub.DailyUsageUSD = float64Ptr(10), &old, 3
				case "weekly":
					sub.WeeklyLimitUSD, sub.WeeklyWindowStart, sub.WeeklyUsageUSD = float64Ptr(10), &old, 3
				default:
					sub.MonthlyLimitUSD, sub.MonthlyWindowStart, sub.MonthlyUsageUSD = float64Ptr(10), &old, 3
				}
				mustCreateSubscription(t, client, sub)
				repo := NewUserSubscriptionRepository(client)
				paused := &focusPausedExpiryRepository{UserSubscriptionRepository: repo, entered: make(chan struct{}), resume: make(chan struct{})}
				svc := service.NewSubscriptionService(nil, paused, nil, client, nil)
				finished := make(chan error, 1)
				go func() {
					var err error
					if operation == "set_validity" {
						_, err = svc.SetSubscriptionValidityDays(ctx, sub.ID, 65)
					} else {
						_, err = svc.BulkExtendSubscriptions(ctx, []int64{sub.ID}, 65)
					}
					finished <- err
				}()
				select {
				case <-paused.entered:
				case <-ctx.Done():
					t.Fatal("延期未进入受控事务交错点")
				}
				other := service.NewSubscriptionService(nil, repo, nil, client, nil)
				resetStarted := time.Now()
				reset, err := other.AdminResetQuota(ctx, sub.ID, kind == "daily", kind == "weekly", kind == "monthly")
				require.NoError(t, err)
				resetAnchor, _ := focusPersistedWindow(reset, kind)
				require.NotNil(t, resetAnchor)
				if kind == "daily" {
					require.True(t, resetAnchor.Equal(timezone.StartOfDay(resetStarted)))
				} else {
					require.False(t, resetAnchor.Before(resetStarted.Add(-time.Microsecond)))
					require.False(t, resetAnchor.After(time.Now().Add(time.Microsecond)), "手动周、月重置应使用实际操作时刻")
				}
				require.NoError(t, other.RecordUsage(ctx, sub.ID, 4.25))
				close(paused.resume)
				require.NoError(t, <-finished)
				final, err := repo.GetByID(ctx, sub.ID)
				require.NoError(t, err)
				anchor, used := focusPersistedWindow(final, kind)
				require.True(t, anchor.Equal(*resetAnchor), "延期必须保留并发手动重置已经写入的精确锚点")
				require.Equal(t, 4.25, used, "延期时旧锚点 CAS 不匹配，必须保留新窗口已经实际产生的消费")
			})
		}
	}
}

// 过期记录延期后，数据库状态、进度状态和网关准入必须一起恢复，不能仅修改 expires_at。
func TestSubscriptionFocus_PostgresExpiredExtensionRestoresEligibility(t *testing.T) {
	for _, operation := range []string{"extend", "set_validity", "bulk_extend"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			user := mustCreateUser(t, client, &service.User{Email: "expired-focus-" + uuid.NewString() + "@example.invalid"})
			plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "过期恢复专项", Price: 10, ValidityDays: 90, ValidityUnit: "day"})
			now := time.Now().Truncate(time.Microsecond)
			old := timezone.StartOfDay(now).AddDate(0, 0, -35)
			sub := mustCreateSubscription(t, client, &service.UserSubscription{UserID: user.ID, PlanID: plan.ID,
				StartsAt: now.AddDate(0, 0, -90), ExpiresAt: now.Add(-time.Hour), Status: service.SubscriptionStatusExpired,
				DailyWindowStart: &old, WeeklyWindowStart: &old, MonthlyWindowStart: &old,
				DailyLimitUSD: float64Ptr(10), WeeklyLimitUSD: float64Ptr(20), MonthlyLimitUSD: float64Ptr(30),
				DailyUsageUSD: 10, WeeklyUsageUSD: 20, MonthlyUsageUSD: 30})
			repo := NewUserSubscriptionRepository(client)
			svc := service.NewSubscriptionService(nil, repo, nil, client, nil)
			_, err := svc.ValidateAndCheckLimits(sub, nil)
			require.ErrorIs(t, err, service.ErrSubscriptionExpired)
			switch operation {
			case "extend":
				_, err = svc.ExtendSubscription(ctx, sub.ID, 65)
			case "set_validity":
				_, err = svc.SetSubscriptionValidityDays(ctx, sub.ID, 65)
			default:
				_, err = svc.BulkExtendSubscriptions(ctx, []int64{sub.ID}, 65)
			}
			require.NoError(t, err)
			final, err := repo.GetByID(ctx, sub.ID)
			require.NoError(t, err)
			require.Equal(t, service.SubscriptionStatusActive, final.Status)
			require.WithinDuration(t, now.AddDate(0, 0, 65), final.ExpiresAt, 5*time.Second)
			require.Zero(t, final.DailyUsageUSD)
			require.Zero(t, final.WeeklyUsageUSD)
			require.Zero(t, final.MonthlyUsageUSD)
			maintenance, err := svc.ValidateAndCheckLimits(final, nil)
			require.NoError(t, err)
			require.False(t, maintenance, "延期返回前已完成维护，下一次请求无需弥补窗口")
			progress, err := svc.GetSubscriptionProgress(ctx, sub.ID)
			require.NoError(t, err)
			require.Equal(t, service.SubscriptionStatusActive, progress.Status)
			for _, window := range []*service.UsageWindowProgress{progress.Daily, progress.Weekly, progress.Monthly} {
				require.NotNil(t, window)
				require.Greater(t, window.ResetsInSeconds, int64(0))
			}
		})
	}
}

// 下一次重置早于到期时间就可以显示，以 PostgreSQL 微秒精度核对相等边界和精确周、月锚点。
func TestSubscriptionFocus_PostgresExtensionNextResetBoundary(t *testing.T) {
	for _, window := range []struct {
		kind string
		days int
	}{{"weekly", 7}, {"monthly", 30}} {
		for _, operation := range []string{"extend", "bulk_extend"} {
			for _, delta := range []time.Duration{-time.Microsecond, 0, time.Microsecond} {
				for _, preciseAnchor := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/precise_%v", window.kind, operation, delta, preciseAnchor), func(t *testing.T) {
						ctx := context.Background()
						client := testEntClient(t)
						user := mustCreateUser(t, client, &service.User{Email: "boundary-" + uuid.NewString() + "@example.invalid"})
						plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "下一重置边界", Price: 10})
						now := time.Now().Truncate(time.Microsecond)
						today := timezone.StartOfDay(now)
						current := today
						if preciseAnchor {
							current = now.Add(-time.Hour)
						}
						nextReset := current.AddDate(0, 0, window.days)
						expires := nextReset.AddDate(0, 0, -1).Add(delta)
						sub := &service.UserSubscription{UserID: user.ID, PlanID: plan.ID, StartsAt: today.AddDate(0, 0, -90), ExpiresAt: expires, Status: service.SubscriptionStatusActive}
						if window.kind == "weekly" {
							sub.WeeklyLimitUSD, sub.WeeklyWindowStart, sub.WeeklyUsageUSD = float64Ptr(10), &current, 3.25
						} else {
							sub.MonthlyLimitUSD, sub.MonthlyWindowStart, sub.MonthlyUsageUSD = float64Ptr(10), &current, 3.25
						}
						mustCreateSubscription(t, client, sub)
						repo := NewUserSubscriptionRepository(client)
						svc := service.NewSubscriptionService(nil, repo, nil, client, nil)
						_, err := svc.EnsureWindowMaintenance(ctx, sub)
						require.NoError(t, err)
						if operation == "extend" {
							_, err = svc.ExtendSubscription(ctx, sub.ID, 1)
						} else {
							_, err = svc.BulkExtendSubscriptions(ctx, []int64{sub.ID}, 1)
						}
						require.NoError(t, err)
						got, err := repo.GetByID(ctx, sub.ID)
						require.NoError(t, err)
						require.True(t, got.ExpiresAt.Equal(nextReset.Add(delta)))
						anchor, used := focusPersistedWindow(got, window.kind)
						require.NotNil(t, anchor)
						require.True(t, anchor.Equal(current), "尚未到点时，延期不能提前重置窗口")
						require.Equal(t, 3.25, used, "延期只调整未来重置资格，保留当前窗口消费")
						progress, err := svc.GetSubscriptionProgress(ctx, sub.ID)
						require.NoError(t, err)
						visible := focusPersistedProgress(progress, window.kind)
						require.NotNil(t, visible)
						// 上游进度接口返回原始下一周期；页面再依据到期时间决定显示重置还是结束。
						require.True(t, visible.ResetsAt.Equal(nextReset))
					})
				}
			}
		}
	}
}

// 单条设置目标有效期在不足完整周期时即可恢复窗口，再次延期必须保留锚点及中间产生的消费。
func TestSubscriptionFocus_PostgresSetValidityPartialPeriodThenExtend(t *testing.T) {
	for _, window := range []struct {
		kind string
		days int
	}{{"weekly", 7}, {"monthly", 30}} {
		for _, stale := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stale_%v", window.kind, stale), func(t *testing.T) {
				ctx := context.Background()
				client := testEntClient(t)
				user := mustCreateUser(t, client, &service.User{Email: "target-window-" + uuid.NewString() + "@example.invalid"})
				plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "目标有效期边界", Price: 10})
				now := time.Now().Truncate(time.Microsecond)
				current := now.Add(-time.Hour)
				old := current.AddDate(0, 0, -window.days)
				sub := &service.UserSubscription{UserID: user.ID, PlanID: plan.ID, StartsAt: now.AddDate(0, 0, -90), ExpiresAt: now.Add(time.Hour), Status: service.SubscriptionStatusActive}
				if window.kind == "weekly" {
					sub.WeeklyLimitUSD, sub.WeeklyUsageUSD = float64Ptr(10), 3.25
					if stale {
						sub.WeeklyWindowStart = &old
					}
				} else {
					sub.MonthlyLimitUSD, sub.MonthlyUsageUSD = float64Ptr(10), 3.25
					if stale {
						sub.MonthlyWindowStart = &old
					}
				}
				mustCreateSubscription(t, client, sub)
				repo := NewUserSubscriptionRepository(client)
				svc := service.NewSubscriptionService(nil, repo, nil, client, nil)
				activationStarted := time.Now()
				got, err := svc.SetSubscriptionValidityDays(ctx, sub.ID, window.days-1)
				require.NoError(t, err)
				anchor, used := focusPersistedWindow(got, window.kind)
				require.NotNil(t, anchor)
				if stale {
					require.True(t, anchor.Equal(current), "周、月窗口保持旧锚点的整数周期节奏")
					require.Zero(t, used, "下一窗口起点在有效期内即可推进，无需容纳完整周期")
				} else {
					require.False(t, anchor.Before(activationStarted.Add(-time.Microsecond)))
					require.False(t, anchor.After(time.Now().Add(time.Microsecond)))
					require.Equal(t, 3.25, used, "首次激活保留历史消费")
				}
				firstAnchor := *anchor
				firstUsed := used
				progress, err := svc.GetSubscriptionProgress(ctx, sub.ID)
				require.NoError(t, err)
				visible := focusPersistedProgress(progress, window.kind)
				require.NotNil(t, visible)
				require.True(t, visible.ResetsAt.Equal(firstAnchor.AddDate(0, 0, window.days)), "进度接口保留上游的原始下一重置时间")
				require.NoError(t, svc.RecordUsage(ctx, sub.ID, 1.5))
				got, err = svc.SetSubscriptionValidityDays(ctx, sub.ID, window.days*2+1)
				require.NoError(t, err)
				anchor, used = focusPersistedWindow(got, window.kind)
				require.NotNil(t, anchor)
				require.True(t, anchor.Equal(firstAnchor))
				require.Equal(t, firstUsed+1.5, used, "再次延期不能抹掉首次恢复后新产生的消费")
				progress, err = svc.GetSubscriptionProgress(ctx, sub.ID)
				require.NoError(t, err)
				visible = focusPersistedProgress(progress, window.kind)
				require.NotNil(t, visible)
				require.True(t, visible.ResetsAt.Equal(firstAnchor.AddDate(0, 0, window.days)))
			})
		}
	}
}
