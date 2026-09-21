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

// 在独立 PostgreSQL 中依次检查临期禁用、管理延期、立即显示、真实持久化清零及并发旧快照保护。
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
					// 原到期时间在当天结束之前，日窗口也不再具备完整新周期。
					expiry := today.AddDate(0, 0, 1).Add(-time.Microsecond)
					sub := &service.UserSubscription{UserID: user.ID, PlanID: plan.ID, StartsAt: today.AddDate(0, 0, -90), ExpiresAt: expiry}
					switch window.kind {
					case "daily":
						sub.DailyLimitUSD, sub.DailyUsageUSD = float64Ptr(10), 3
					case "weekly":
						sub.WeeklyLimitUSD, sub.WeeklyUsageUSD = float64Ptr(10), 3
					default:
						sub.MonthlyLimitUSD, sub.MonthlyUsageUSD = float64Ptr(10), 3
					}
					oldWindow := today.AddDate(0, 0, -window.days)
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
					before, err := svc.EnsureWindowMaintenance(ctx, sub)
					require.NoError(t, err)
					beforeAnchor, beforeUsed := focusPersistedWindow(before, window.kind)
					require.Equal(t, 3.0, beforeUsed)
					if !stale {
						require.Nil(t, beforeAnchor, "迁移遗留 NULL 窗口在原有效期内确实不能启动")
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
					require.True(t, today.Equal(*anchor))
					if stale {
						require.Zero(t, used)
					} else {
						require.Equal(t, 3.0, used, "首次激活不会抹掉历史累计费用")
					}
					progress, err := svc.GetSubscriptionProgress(ctx, sub.ID)
					require.NoError(t, err)
					visible := focusPersistedProgress(progress, window.kind)
					require.NotNil(t, visible)
					require.True(t, today.AddDate(0, 0, window.days).Equal(visible.ResetsAt))
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
					require.True(t, resetAnchor.Equal(today))
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
				_, err := other.AdminResetQuota(ctx, sub.ID, kind == "daily", kind == "weekly", kind == "monthly")
				require.NoError(t, err)
				require.NoError(t, other.RecordUsage(ctx, sub.ID, 4.25))
				close(paused.resume)
				require.NoError(t, <-finished)
				final, err := repo.GetByID(ctx, sub.ID)
				require.NoError(t, err)
				anchor, used := focusPersistedWindow(final, kind)
				require.True(t, anchor.Equal(timezone.StartOfDay(now)))
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

// 追加一天跨过完整窗口阈值时，以 PostgreSQL 微秒精度核对“不足、恰好、超过”三个边界。
func TestSubscriptionFocus_PostgresExtensionFullWindowBoundary(t *testing.T) {
	for _, window := range []struct {
		kind string
		days int
	}{{"weekly", 7}, {"monthly", 30}} {
		for _, operation := range []string{"extend", "bulk_extend"} {
			for _, delta := range []time.Duration{-time.Microsecond, 0, time.Microsecond} {
				for _, stale := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/stale_%v", window.kind, operation, delta, stale), func(t *testing.T) {
						ctx := context.Background()
						client := testEntClient(t)
						user := mustCreateUser(t, client, &service.User{Email: "boundary-" + uuid.NewString() + "@example.invalid"})
						plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "完整窗口边界", Price: 10})
						today := timezone.StartOfDay(time.Now())
						expires := today.AddDate(0, 0, window.days-1).Add(delta)
						old := today.AddDate(0, 0, -window.days)
						sub := &service.UserSubscription{UserID: user.ID, PlanID: plan.ID, StartsAt: today.AddDate(0, 0, -90), ExpiresAt: expires, Status: service.SubscriptionStatusActive}
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
						require.True(t, got.ExpiresAt.Equal(today.AddDate(0, 0, window.days).Add(delta)))
						anchor, used := focusPersistedWindow(got, window.kind)
						if delta < 0 {
							require.Equal(t, 3.25, used, "延期仍不足一个完整周期时不能提前赠送额度")
							if stale {
								require.True(t, anchor.Equal(old))
							} else {
								require.Nil(t, anchor)
							}
						} else {
							require.NotNil(t, anchor)
							require.True(t, anchor.Equal(today), "恰好容纳完整周期时也必须启动")
							if stale {
								require.Zero(t, used)
							} else {
								require.Equal(t, 3.25, used, "首次激活保留历史消费")
							}
						}
						progress, err := svc.GetSubscriptionProgress(ctx, sub.ID)
						require.NoError(t, err)
						visible := focusPersistedProgress(progress, window.kind)
						if !stale && delta < 0 {
							require.Nil(t, visible)
						} else {
							require.NotNil(t, visible)
							require.True(t, visible.ResetsAt.Equal(got.ExpiresAt), "仅一个完整窗口时不能显示额外第二个周期的刷新")
						}
					})
				}
			}
		}
	}
}

// 单条“设置目标有效期”先延到仍不足一周期，再延到充足周期，验证与追加入口相同的恢复规则。
func TestSubscriptionFocus_PostgresSetValidityInsufficientThenRecover(t *testing.T) {
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
				today := timezone.StartOfDay(time.Now())
				old := today.AddDate(0, 0, -window.days)
				sub := &service.UserSubscription{UserID: user.ID, PlanID: plan.ID, StartsAt: today.AddDate(0, 0, -90), ExpiresAt: today.AddDate(0, 0, 1).Add(-time.Microsecond), Status: service.SubscriptionStatusActive}
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
				got, err := svc.SetSubscriptionValidityDays(ctx, sub.ID, window.days-1)
				require.NoError(t, err)
				anchor, used := focusPersistedWindow(got, window.kind)
				require.Equal(t, 3.25, used)
				if stale {
					require.True(t, anchor.Equal(old))
				} else {
					require.Nil(t, anchor)
				}
				got, err = svc.SetSubscriptionValidityDays(ctx, sub.ID, window.days*2+1)
				require.NoError(t, err)
				anchor, used = focusPersistedWindow(got, window.kind)
				require.NotNil(t, anchor)
				require.True(t, anchor.Equal(today))
				if stale {
					require.Zero(t, used)
				} else {
					require.Equal(t, 3.25, used)
				}
				progress, err := svc.GetSubscriptionProgress(ctx, sub.ID)
				require.NoError(t, err)
				visible := focusPersistedProgress(progress, window.kind)
				require.NotNil(t, visible)
				require.True(t, visible.ResetsAt.Equal(today.AddDate(0, 0, window.days)))
			})
		}
	}
}
