//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/stretchr/testify/require"
)

// extensionWindowRepo 记录延期后窗口维护的持久化结果。
type extensionWindowRepo struct {
	*subscriptionUserSubRepoStub
}

func (r *extensionWindowRepo) ExtendExpiry(_ context.Context, id int64, expiresAt time.Time) error {
	sub := r.byID[id]
	if sub == nil {
		return ErrSubscriptionNotFound
	}
	sub.ExpiresAt = expiresAt
	return nil
}

func (r *extensionWindowRepo) UpdateStatus(_ context.Context, id int64, status string) error {
	sub := r.byID[id]
	if sub == nil {
		return ErrSubscriptionNotFound
	}
	sub.Status = status
	return nil
}

func (r *extensionWindowRepo) ActivateWindows(_ context.Context, id int64, start time.Time, activation SubscriptionWindowActivation) error {
	sub := r.byID[id]
	if sub == nil {
		return ErrSubscriptionNotFound
	}
	if activation.Daily {
		sub.DailyWindowStart = &start
	}
	if activation.Weekly {
		sub.WeeklyWindowStart = &start
	}
	if activation.Monthly {
		sub.MonthlyWindowStart = &start
	}
	return nil
}

func (r *extensionWindowRepo) ResetDailyUsage(_ context.Context, id int64, _ *time.Time, start time.Time) error {
	sub := r.byID[id]
	if sub == nil {
		return ErrSubscriptionNotFound
	}
	sub.DailyUsageUSD = 0
	sub.DailyWindowStart = &start
	return nil
}

func (r *extensionWindowRepo) ResetWeeklyUsage(_ context.Context, id int64, _ *time.Time, start time.Time) error {
	sub := r.byID[id]
	if sub == nil {
		return ErrSubscriptionNotFound
	}
	sub.WeeklyUsageUSD = 0
	sub.WeeklyWindowStart = &start
	return nil
}

func (r *extensionWindowRepo) ResetMonthlyUsage(_ context.Context, id int64, _ *time.Time, start time.Time) error {
	sub := r.byID[id]
	if sub == nil {
		return ErrSubscriptionNotFound
	}
	sub.MonthlyUsageUSD = 0
	sub.MonthlyWindowStart = &start
	return nil
}

func newExtensionWindowRepo() *extensionWindowRepo {
	return &extensionWindowRepo{subscriptionUserSubRepoStub: newSubscriptionUserSubRepoStub()}
}

func TestExtendSubscriptionActivatesQuotaWindowAfterExpiryExtension(t *testing.T) {
	repo := newExtensionWindowRepo()
	limit := 10.0
	now := time.Now()
	repo.seed(&UserSubscription{
		ID:            1,
		UserID:        1,
		PlanID:        1,
		StartsAt:      now.Add(-24 * time.Hour),
		ExpiresAt:     now.Add(12 * time.Hour),
		Status:        SubscriptionStatusActive,
		DailyLimitUSD: &limit,
		DailyUsageUSD: 3,
	})

	client := newPaymentConfigServiceTestClient(t)
	tx, err := client.Tx(context.Background())
	require.NoError(t, err)
	txCtx := dbent.NewTxContext(context.Background(), tx)
	t.Cleanup(func() { _ = tx.Rollback() })

	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)
	_, err = svc.ExtendSubscription(txCtx, 1, 2)
	require.NoError(t, err)

	updated, err := repo.GetByID(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, updated.DailyWindowStart, "延长后应立即激活此前因临期而未启动的日窗口")
	require.Equal(t, 3.0, updated.DailyUsageUSD, "激活窗口不应隐式清除原有用量")
}

func TestExtendSubscriptionResetsExpiredQuotaWindowAfterExtension(t *testing.T) {
	repo := newExtensionWindowRepo()
	limit := 10.0
	now := time.Now()
	oldWindowStart := startOfDay(now.AddDate(0, 0, -1))
	repo.seed(&UserSubscription{
		ID:               1,
		UserID:           1,
		PlanID:           1,
		StartsAt:         now.Add(-48 * time.Hour),
		ExpiresAt:        now.Add(12 * time.Hour),
		Status:           SubscriptionStatusActive,
		DailyLimitUSD:    &limit,
		DailyUsageUSD:    3,
		DailyWindowStart: &oldWindowStart,
	})

	client := newPaymentConfigServiceTestClient(t)
	tx, err := client.Tx(context.Background())
	require.NoError(t, err)
	txCtx := dbent.NewTxContext(context.Background(), tx)
	t.Cleanup(func() { _ = tx.Rollback() })

	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)
	_, err = svc.ExtendSubscription(txCtx, 1, 2)
	require.NoError(t, err)

	updated, err := repo.GetByID(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, updated.DailyWindowStart)
	require.True(t, updated.DailyWindowStart.Equal(startOfDay(now)))
	require.Zero(t, updated.DailyUsageUSD, "延长后已具备新窗口时应推进过期窗口并清零该窗口用量")
}

func TestSetSubscriptionValidityDaysActivatesQuotaWindowBeforeReturning(t *testing.T) {
	repo := newExtensionWindowRepo()
	limit := 10.0
	now := time.Now()
	repo.seed(&UserSubscription{
		ID:            1,
		UserID:        1,
		PlanID:        1,
		StartsAt:      now.Add(-24 * time.Hour),
		ExpiresAt:     now.Add(12 * time.Hour),
		Status:        SubscriptionStatusActive,
		DailyLimitUSD: &limit,
	})

	client := newPaymentConfigServiceTestClient(t)
	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, client, nil)
	_, err := svc.SetSubscriptionValidityDays(context.Background(), 1, 2)
	require.NoError(t, err)

	updated, err := repo.GetByID(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, updated.DailyWindowStart, "管理员延长接口返回前应完成窗口激活")
}
