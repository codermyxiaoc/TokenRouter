//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
	"github.com/TokenFlux/TokenRouter/ent/usersubscription"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// bulkSubscriptionTestRepo 使用真实事务验证回滚，未用到的仓储方法继续拒绝意外调用。
type bulkSubscriptionTestRepo struct {
	userSubRepoNoop
	client *dbent.Client
	failID int64
}

func (r *bulkSubscriptionTestRepo) db(ctx context.Context) *dbent.Client {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return r.client
}

func bulkSubscriptionFromEnt(sub *dbent.UserSubscription) *UserSubscription {
	return &UserSubscription{ID: sub.ID, UserID: sub.UserID, PlanID: sub.PlanID, StartsAt: sub.StartsAt, ExpiresAt: sub.ExpiresAt, Status: sub.Status, DeletedAt: sub.DeletedAt,
		DailyUsageUSD: sub.DailyUsageUsd, WeeklyUsageUSD: sub.WeeklyUsageUsd, MonthlyUsageUSD: sub.MonthlyUsageUsd}
}

func (r *bulkSubscriptionTestRepo) GetByIDIncludeDeleted(ctx context.Context, id int64) (*UserSubscription, error) {
	return r.GetByID(mixins.SkipSoftDelete(ctx), id)
}

func (r *bulkSubscriptionTestRepo) Delete(ctx context.Context, id int64) error {
	if id == r.failID {
		return errors.New("private database detail")
	}
	return r.db(ctx).UserSubscription.UpdateOneID(id).SetDeletedAt(time.Now()).Exec(ctx)
}

func (r *bulkSubscriptionTestRepo) Restore(ctx context.Context, id int64, status string) (*UserSubscription, error) {
	if id == r.failID {
		return nil, errors.New("private database detail")
	}
	if err := r.db(ctx).UserSubscription.UpdateOneID(id).ClearDeletedAt().SetStatus(status).Exec(mixins.SkipSoftDelete(ctx)); err != nil {
		return nil, err
	}
	return r.GetByID(ctx, id)
}

func (r *bulkSubscriptionTestRepo) GetByID(ctx context.Context, id int64) (*UserSubscription, error) {
	sub, err := r.db(ctx).UserSubscription.Get(ctx, id)
	if dbent.IsNotFound(err) {
		return nil, ErrSubscriptionNotFound
	}
	if err != nil {
		return nil, err
	}
	return bulkSubscriptionFromEnt(sub), nil
}

func (r *bulkSubscriptionTestRepo) ListByUserIDAndPlanID(ctx context.Context, userID, planID int64) ([]UserSubscription, error) {
	rows, err := r.db(ctx).UserSubscription.Query().Where(usersubscription.UserIDEQ(userID), usersubscription.PlanIDEQ(planID)).All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]UserSubscription, 0, len(rows))
	for _, row := range rows {
		result = append(result, *bulkSubscriptionFromEnt(row))
	}
	return result, nil
}

func (r *bulkSubscriptionTestRepo) ExtendExpiry(ctx context.Context, id int64, expiry time.Time) error {
	if id == r.failID {
		return errors.New("private database detail")
	}
	return r.db(ctx).UserSubscription.UpdateOneID(id).SetExpiresAt(expiry).Exec(ctx)
}

func (r *bulkSubscriptionTestRepo) UpdateStatus(ctx context.Context, id int64, status string) error {
	return r.db(ctx).UserSubscription.UpdateOneID(id).SetStatus(status).Exec(ctx)
}

func (r *bulkSubscriptionTestRepo) Update(ctx context.Context, sub *UserSubscription) error {
	return r.db(ctx).UserSubscription.UpdateOneID(sub.ID).SetStartsAt(sub.StartsAt).SetExpiresAt(sub.ExpiresAt).SetStatus(sub.Status).Exec(ctx)
}

func (r *bulkSubscriptionTestRepo) ResetUsageWindows(ctx context.Context, id int64, daily, weekly, monthly bool, start time.Time) error {
	if id == r.failID {
		return errors.New("private database detail")
	}
	update := r.db(ctx).UserSubscription.UpdateOneID(id)
	if daily {
		update.SetDailyUsageUsd(0).SetDailyWindowStart(start)
	}
	if weekly {
		update.SetWeeklyUsageUsd(0).SetWeeklyWindowStart(start)
	}
	if monthly {
		update.SetMonthlyUsageUsd(0).SetMonthlyWindowStart(start)
	}
	return update.Exec(ctx)
}

func newBulkSubscriptionFixture(t *testing.T) (*SubscriptionService, *bulkSubscriptionTestRepo, []int64, time.Time) {
	t.Helper()
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().SetEmail("bulk@example.com").SetPasswordHash("test").Save(ctx)
	require.NoError(t, err)
	plan, err := client.SubscriptionPlan.Create().SetName("bulk").SetPrice(1).Save(ctx)
	require.NoError(t, err)
	base := time.Now().UTC().Truncate(time.Second).AddDate(0, 0, 10)
	ids := make([]int64, 0, 2)
	for i := 0; i < 2; i++ {
		status := SubscriptionStatusActive
		if i == 1 {
			status = SubscriptionStatusPending
		}
		sub, err := client.UserSubscription.Create().SetUserID(user.ID).SetPlanID(plan.ID).
			SetStartsAt(base.AddDate(0, 0, (i-1)*20)).SetExpiresAt(base.AddDate(0, 0, i*20)).SetStatus(status).
			SetDailyUsageUsd(3).SetWeeklyUsageUsd(4).SetMonthlyUsageUsd(5).Save(ctx)
		require.NoError(t, err)
		ids = append(ids, sub.ID)
	}
	repo := &bulkSubscriptionTestRepo{client: client}
	return NewSubscriptionService(groupRepoNoop{}, repo, nil, client, nil), repo, ids, base
}

func TestBulkSubscriptionsExtendDeduplicatesAndPreservesChain(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		name := "forward"
		if reverse {
			name = "reverse"
		}
		t.Run(name, func(t *testing.T) {
			svc, repo, ids, base := newBulkSubscriptionFixture(t)
			selected := []int64{ids[0], ids[1], ids[0]}
			if reverse {
				selected = []int64{ids[1], ids[0], ids[1]}
			}
			result, err := svc.BulkExtendSubscriptions(context.Background(), selected, 3)
			require.NoError(t, err)
			require.Equal(t, 2, result.UpdatedCount)
			first, _ := repo.GetByID(context.Background(), ids[0])
			second, _ := repo.GetByID(context.Background(), ids[1])
			require.True(t, base.AddDate(0, 0, 3).Equal(first.ExpiresAt))
			require.True(t, first.ExpiresAt.Equal(second.StartsAt))
			require.True(t, base.AddDate(0, 0, 26).Equal(second.ExpiresAt))
		})
	}
}

func TestBulkSubscriptionsRollbackAndSafeError(t *testing.T) {
	for _, operation := range []string{"extend", "reset"} {
		t.Run(operation, func(t *testing.T) {
			svc, repo, ids, base := newBulkSubscriptionFixture(t)
			repo.failID = ids[1]
			var err error
			if operation == "extend" {
				_, err = svc.BulkExtendSubscriptions(context.Background(), ids, 3)
			} else {
				_, err = svc.BulkResetSubscriptionQuota(context.Background(), ids, true, true, true)
			}
			require.Error(t, err)
			_, status := infraerrors.ToHTTP(err)
			require.Equal(t, "2", status.Metadata["subscription_id"])
			require.NotContains(t, status.Message, "private database")
			first, _ := repo.GetByID(context.Background(), ids[0])
			require.True(t, base.Equal(first.ExpiresAt))
			require.Equal(t, float64(3), first.DailyUsageUSD)
			second, _ := repo.GetByID(context.Background(), ids[1])
			require.True(t, base.Equal(second.StartsAt))
		})
	}
}

func TestBulkSubscriptionsResetOnlySelectedWindows(t *testing.T) {
	svc, repo, ids, base := newBulkSubscriptionFixture(t)
	result, err := svc.BulkResetSubscriptionQuota(context.Background(), ids, true, false, true)
	require.NoError(t, err)
	require.Equal(t, 2, result.UpdatedCount)
	for _, id := range ids {
		sub, _ := repo.GetByID(context.Background(), id)
		require.Zero(t, sub.DailyUsageUSD)
		require.Equal(t, float64(4), sub.WeeklyUsageUSD)
		require.Zero(t, sub.MonthlyUsageUSD)
	}
	first, _ := repo.GetByID(context.Background(), ids[0])
	require.True(t, first.ExpiresAt.Equal(base))
}

func TestBulkSubscriptionsExpiredHistoryRejectsRegardlessOfSelectionOrder(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		name := "forward"
		if reverse {
			name = "reverse"
		}
		t.Run(name, func(t *testing.T) {
			svc, repo, ids, _ := newBulkSubscriptionFixture(t)
			ctx := context.Background()
			now := time.Now().UTC()
			for i, id := range ids {
				require.NoError(t, repo.client.UserSubscription.UpdateOneID(id).
					SetStartsAt(now.AddDate(0, 0, -40+i*10)).SetExpiresAt(now.AddDate(0, 0, -30+i*10)).SetStatus(SubscriptionStatusExpired).Exec(ctx))
			}
			selected := []int64{ids[0], ids[1]}
			if reverse {
				selected = []int64{ids[1], ids[0]}
			}
			_, err := svc.BulkExtendSubscriptions(ctx, selected, 3)
			require.Equal(t, "SUBSCRIPTION_BULK_EXPIRED_HAS_SUCCESSOR", infraerrors.Reason(err))
			first, _ := repo.GetByID(ctx, ids[0])
			second, _ := repo.GetByID(ctx, ids[1])
			require.True(t, now.AddDate(0, 0, -30).Equal(first.ExpiresAt))
			require.True(t, first.ExpiresAt.Equal(second.StartsAt))
			require.True(t, now.AddDate(0, 0, -20).Equal(second.ExpiresAt))
			// 链末尾的过期订阅可正常续上，历史记录保持原样。
			_, err = svc.BulkExtendSubscriptions(ctx, []int64{ids[1]}, 3)
			require.NoError(t, err)
			second, _ = repo.GetByID(ctx, ids[1])
			require.WithinDuration(t, now.AddDate(0, 0, 3), second.ExpiresAt, time.Second)
		})
	}
}

func TestBulkSubscriptionsLocksUserBeforeMutation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	repo := newSubscriptionUserSubRepoStub()
	repo.seed(&UserSubscription{ID: 1, UserID: 42, PlanID: 7, Status: SubscriptionStatusActive})
	injected := errors.New("stop at lock")
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT .*FROM "users".*FOR UPDATE`).WithArgs(int64(42)).WillReturnError(injected)
	mock.ExpectRollback()
	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, client, nil)
	_, err = svc.BulkExtendSubscriptions(context.Background(), []int64{1}, 2)
	require.ErrorIs(t, err, injected)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBulkSubscriptionsRejectInvalidSelectionBeforeMutation(t *testing.T) {
	svc := &SubscriptionService{}
	for _, ids := range [][]int64{nil, {0}, {-1}, make([]int64, 101)} {
		_, err := svc.BulkExtendSubscriptions(context.Background(), ids, 1)
		require.Error(t, err)
		_, err = svc.BulkRevokeSubscriptions(context.Background(), ids)
		require.Error(t, err)
		_, err = svc.BulkRestoreSubscriptions(context.Background(), ids)
		require.Error(t, err)
	}
	for _, days := range []int{0, -1, 36501} {
		_, err := svc.BulkExtendSubscriptions(context.Background(), []int64{1}, days)
		require.Error(t, err)
	}
	_, err := svc.BulkResetSubscriptionQuota(context.Background(), []int64{1}, false, false, false)
	require.ErrorIs(t, err, ErrInvalidInput)

	svc, repo, ids, base := newBulkSubscriptionFixture(t)
	require.NoError(t, repo.UpdateStatus(context.Background(), ids[1], SubscriptionStatusSuspended))
	_, err = svc.BulkExtendSubscriptions(context.Background(), ids, 1)
	require.ErrorIs(t, err, ErrSubscriptionNotActive)
	first, _ := repo.GetByID(context.Background(), ids[0])
	require.True(t, first.ExpiresAt.Equal(base))
}

// 撤销当前订阅必须沿用后继提前生效，恢复存在重叠时必须整批拒绝而非额外延期。
func TestBulkSubscriptionsRevokePreservesChainAndRestoreRejectsOverlap(t *testing.T) {
	svc, repo, ids, _ := newBulkSubscriptionFixture(t)
	ctx := context.Background()
	before, err := repo.GetByID(ctx, ids[1])
	require.NoError(t, err)
	result, err := svc.BulkRevokeSubscriptions(ctx, []int64{ids[0], ids[0]})
	require.NoError(t, err)
	require.Equal(t, []int64{ids[0]}, result.SubscriptionIDs)
	require.Equal(t, 1, result.UpdatedCount)
	first, err := repo.GetByIDIncludeDeleted(ctx, ids[0])
	require.NoError(t, err)
	require.NotNil(t, first.DeletedAt)
	next, err := repo.GetByID(ctx, ids[1])
	require.NoError(t, err)
	require.WithinDuration(t, time.Now(), next.StartsAt, time.Second)
	require.Equal(t, before.ExpiresAt.Sub(before.StartsAt), next.ExpiresAt.Sub(next.StartsAt))
	_, err = svc.BulkRestoreSubscriptions(ctx, []int64{ids[0]})
	require.ErrorIs(t, err, ErrSubscriptionRestoreConflict)
	first, err = repo.GetByIDIncludeDeleted(ctx, ids[0])
	require.NoError(t, err)
	require.NotNil(t, first.DeletedAt)
}

// 恢复必须根据原窗口计算 active、pending、expired，暂停记录仍保留暂停且不清空消费。
func TestBulkSubscriptionsRestorePreservesOriginalEntitlements(t *testing.T) {
	for _, status := range []string{SubscriptionStatusActive, SubscriptionStatusPending, SubscriptionStatusExpired, SubscriptionStatusSuspended} {
		t.Run(status, func(t *testing.T) {
			svc, repo, ids, _ := newBulkSubscriptionFixture(t)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Second)
			starts, expires := now.Add(-time.Hour), now.Add(time.Hour)
			if status == SubscriptionStatusPending {
				starts, expires = now.Add(time.Hour), now.Add(2*time.Hour)
			}
			if status == SubscriptionStatusExpired {
				starts, expires = now.Add(-2*time.Hour), now.Add(-time.Hour)
			}
			require.NoError(t, repo.client.UserSubscription.UpdateOneID(ids[0]).SetStartsAt(starts).SetExpiresAt(expires).SetStatus(status).SetDeletedAt(now).Exec(ctx))
			result, err := svc.BulkRestoreSubscriptions(ctx, []int64{ids[0], ids[0]})
			require.NoError(t, err)
			require.Equal(t, 1, result.UpdatedCount)
			got, err := repo.GetByID(ctx, ids[0])
			require.NoError(t, err)
			require.Nil(t, got.DeletedAt)
			require.Equal(t, status, got.Status)
			require.True(t, starts.Equal(got.StartsAt))
			require.True(t, expires.Equal(got.ExpiresAt))
			require.Equal(t, 3.0, got.DailyUsageUSD)
			require.Equal(t, 4.0, got.WeeklyUsageUSD)
			require.Equal(t, 5.0, got.MonthlyUsageUSD)
		})
	}
}

// 状态资格在修改前整批复核，不能先撤销有效项再因第二项无效而留下部分成功。
func TestBulkSubscriptionsStatusMutationRejectsMixedEligibility(t *testing.T) {
	svc, repo, ids, _ := newBulkSubscriptionFixture(t)
	ctx := context.Background()
	require.NoError(t, repo.Delete(ctx, ids[1]))
	_, err := svc.BulkRevokeSubscriptions(ctx, ids)
	require.Error(t, err)
	got, err := repo.GetByID(ctx, ids[0])
	require.NoError(t, err)
	require.Nil(t, got.DeletedAt)
	require.NoError(t, repo.client.UserSubscription.UpdateOneID(ids[1]).ClearDeletedAt().Exec(mixins.SkipSoftDelete(ctx)))
	require.NoError(t, repo.Delete(ctx, ids[0]))
	_, err = svc.BulkRestoreSubscriptions(ctx, ids)
	require.ErrorIs(t, err, ErrSubscriptionNotRevoked)
	got, err = repo.GetByIDIncludeDeleted(ctx, ids[0])
	require.NoError(t, err)
	require.NotNil(t, got.DeletedAt)
}
