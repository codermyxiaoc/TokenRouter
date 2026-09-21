//go:build integration

package repository

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 在真实 PostgreSQL 中保留软删除、用户锁与事务行为，验证撤销恢复不会重新发放时长或额度。
func TestSubscriptionBulkStatusPostgresPreservesEntitlements(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUserSubscriptionRepository(client)
	svc := service.NewSubscriptionService(nil, repo, nil, client, nil)
	plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "批量状态", Price: 10})
	now := time.Now().Truncate(time.Microsecond)
	var selected []int64
	original := make(map[int64]*service.UserSubscription)
	for _, status := range []string{service.SubscriptionStatusActive, service.SubscriptionStatusPending, service.SubscriptionStatusExpired, service.SubscriptionStatusSuspended} {
		user := mustCreateUser(t, client, &service.User{Email: "bulk-status-" + uuid.NewString() + "@example.invalid", Balance: 37.125})
		starts, expires := now.Add(-time.Hour), now.AddDate(0, 0, 30)
		if status == service.SubscriptionStatusPending {
			starts, expires = now.Add(time.Hour), now.AddDate(0, 0, 31)
		}
		if status == service.SubscriptionStatusExpired {
			starts, expires = now.Add(-2*time.Hour), now.Add(-time.Hour)
		}
		window := now.Add(-time.Hour)
		sub := mustCreateSubscription(t, client, &service.UserSubscription{UserID: user.ID, PlanID: plan.ID, StartsAt: starts, ExpiresAt: expires, Status: status,
			DailyLimitUSD: float64Ptr(10), WeeklyLimitUSD: float64Ptr(20), MonthlyLimitUSD: float64Ptr(30),
			DailyWindowStart: &window, WeeklyWindowStart: &window, MonthlyWindowStart: &window, DailyUsageUSD: 1.25, WeeklyUsageUSD: 2.5, MonthlyUsageUSD: 3.75})
		original[sub.ID] = sub
		selected = append([]int64{sub.ID}, selected...)
	}
	result, err := svc.BulkRevokeSubscriptions(ctx, append(selected, selected[0]))
	require.NoError(t, err)
	require.Equal(t, 4, result.UpdatedCount)
	require.Equal(t, selected, result.SubscriptionIDs)
	for _, id := range selected {
		_, err := repo.GetByID(ctx, id)
		require.ErrorIs(t, err, service.ErrSubscriptionNotFound)
	}
	result, err = svc.BulkRestoreSubscriptions(ctx, selected)
	require.NoError(t, err)
	require.Equal(t, 4, result.UpdatedCount)
	for _, id := range selected {
		got, err := repo.GetByID(ctx, id)
		require.NoError(t, err)
		before := original[id]
		require.Nil(t, got.DeletedAt)
		require.Equal(t, before.Status, got.Status)
		require.True(t, before.StartsAt.Equal(got.StartsAt))
		require.True(t, before.ExpiresAt.Equal(got.ExpiresAt))
		require.Equal(t, before.UserID, got.UserID)
		require.Equal(t, before.PlanID, got.PlanID)
		require.Equal(t, before.DailyLimitUSD, got.DailyLimitUSD)
		require.Equal(t, before.WeeklyLimitUSD, got.WeeklyLimitUSD)
		require.Equal(t, before.MonthlyLimitUSD, got.MonthlyLimitUSD)
		require.Equal(t, before.DailyUsageUSD, got.DailyUsageUSD)
		require.Equal(t, before.WeeklyUsageUSD, got.WeeklyUsageUSD)
		require.Equal(t, before.MonthlyUsageUSD, got.MonthlyUsageUSD)
		require.True(t, before.DailyWindowStart.Equal(*got.DailyWindowStart))
		require.True(t, before.WeeklyWindowStart.Equal(*got.WeeklyWindowStart))
		require.True(t, before.MonthlyWindowStart.Equal(*got.MonthlyWindowStart))
		// 撤销和恢复不涉及退款或重新扣费，用户余额必须逐笔保持原值。
		owner, err := client.User.Get(ctx, got.UserID)
		require.NoError(t, err)
		require.Equal(t, 37.125, owner.Balance)
	}
}

// 第二项数据库失败必须回滚第一项的撤销、后继时间链和恢复写入。
type bulkStatusFailingRepository struct {
	service.UserSubscriptionRepository
	failID int64
}

func (r bulkStatusFailingRepository) Delete(ctx context.Context, id int64) error {
	if id == r.failID {
		return errors.New("private database failure")
	}
	return r.UserSubscriptionRepository.Delete(ctx, id)
}

func (r bulkStatusFailingRepository) Restore(ctx context.Context, id int64, status string) (*service.UserSubscription, error) {
	if id == r.failID {
		return nil, errors.New("private database failure")
	}
	return r.UserSubscriptionRepository.Restore(ctx, id, status)
}

func TestSubscriptionBulkStatusPostgresRollback(t *testing.T) {
	for _, action := range []string{"revoke", "restore"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			repo := NewUserSubscriptionRepository(client)
			plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "批量回滚", Price: 10})
			now := time.Now().Truncate(time.Microsecond)
			var selected []int64
			for i := 0; i < 2; i++ {
				user := mustCreateUser(t, client, &service.User{Email: "bulk-rollback-" + uuid.NewString() + "@example.invalid"})
				sub := mustCreateSubscription(t, client, &service.UserSubscription{UserID: user.ID, PlanID: plan.ID, StartsAt: now.Add(-time.Hour), ExpiresAt: now.AddDate(0, 0, 30), Status: service.SubscriptionStatusActive})
				selected = append(selected, sub.ID)
			}
			if action == "restore" {
				for _, id := range selected {
					require.NoError(t, repo.Delete(ctx, id))
				}
			}
			first, err := repo.GetByIDIncludeDeleted(ctx, selected[0])
			require.NoError(t, err)
			var pending *service.UserSubscription
			if action == "revoke" {
				pending = mustCreateSubscription(t, client, &service.UserSubscription{UserID: first.UserID, PlanID: plan.ID, StartsAt: first.ExpiresAt, ExpiresAt: first.ExpiresAt.AddDate(0, 0, 30), Status: service.SubscriptionStatusPending})
			}
			svc := service.NewSubscriptionService(nil, bulkStatusFailingRepository{repo, selected[1]}, nil, client, nil)
			if action == "revoke" {
				_, err = svc.BulkRevokeSubscriptions(ctx, selected)
			} else {
				_, err = svc.BulkRestoreSubscriptions(ctx, selected)
			}
			require.Error(t, err)
			_, appErr := infraerrors.ToHTTP(err)
			require.Equal(t, strconv.FormatInt(selected[1], 10), appErr.Metadata["subscription_id"])
			require.NotContains(t, appErr.Message, "private database")
			got, err := repo.GetByIDIncludeDeleted(ctx, selected[0])
			require.NoError(t, err)
			require.Equal(t, first.DeletedAt, got.DeletedAt)
			if pending != nil {
				got, err = repo.GetByID(ctx, pending.ID)
				require.NoError(t, err)
				require.True(t, pending.StartsAt.Equal(got.StartsAt))
				require.True(t, pending.ExpiresAt.Equal(got.ExpiresAt))
			}
		})
	}
}

// 第二项恢复与已经接续生效的套餐冲突时，第一项恢复也必须回滚，不能改变原有消费链。
func TestSubscriptionBulkStatusPostgresRestoreConflictRollsBackEarlierUser(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUserSubscriptionRepository(client)
	svc := service.NewSubscriptionService(nil, repo, nil, client, nil)
	plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "恢复冲突", Price: 10})
	now := time.Now().Truncate(time.Microsecond)
	var selected []int64
	var pending *service.UserSubscription
	for i := 0; i < 2; i++ {
		user := mustCreateUser(t, client, &service.User{Email: "bulk-conflict-" + uuid.NewString() + "@example.invalid"})
		sub := mustCreateSubscription(t, client, &service.UserSubscription{UserID: user.ID, PlanID: plan.ID, StartsAt: now.Add(-time.Hour), ExpiresAt: now.AddDate(0, 0, 15), Status: service.SubscriptionStatusActive})
		selected = append(selected, sub.ID)
		if i == 1 {
			pending = mustCreateSubscription(t, client, &service.UserSubscription{UserID: user.ID, PlanID: plan.ID, StartsAt: sub.ExpiresAt, ExpiresAt: sub.ExpiresAt.AddDate(0, 0, 30), Status: service.SubscriptionStatusPending})
		}
	}
	_, err := svc.BulkRevokeSubscriptions(ctx, selected)
	require.NoError(t, err)
	afterRevoke, err := repo.GetByID(ctx, pending.ID)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now(), afterRevoke.StartsAt, time.Second)
	_, err = svc.BulkRestoreSubscriptions(ctx, selected)
	require.ErrorIs(t, err, service.ErrSubscriptionRestoreConflict)
	_, status := infraerrors.ToHTTP(err)
	require.Equal(t, strconv.FormatInt(selected[1], 10), status.Metadata["subscription_id"])
	for _, id := range selected {
		got, err := repo.GetByIDIncludeDeleted(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, got.DeletedAt)
	}
	afterConflict, err := repo.GetByID(ctx, pending.ID)
	require.NoError(t, err)
	require.True(t, afterRevoke.StartsAt.Equal(afterConflict.StartsAt))
	require.True(t, afterRevoke.ExpiresAt.Equal(afterConflict.ExpiresAt))
}
