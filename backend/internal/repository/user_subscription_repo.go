package repository

import (
	"context"
	"errors"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
	"github.com/TokenFlux/TokenRouter/ent/subscriptionplan"
	dbuser "github.com/TokenFlux/TokenRouter/ent/user"
	"github.com/TokenFlux/TokenRouter/ent/usersubscription"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/lib/pq"
)

type userSubscriptionRepository struct {
	client *dbent.Client
}

func NewUserSubscriptionRepository(client *dbent.Client) service.UserSubscriptionRepository {
	return &userSubscriptionRepository{client: client}
}

func (r *userSubscriptionRepository) Create(ctx context.Context, sub *service.UserSubscription) error {
	if sub == nil {
		return service.ErrSubscriptionNilInput
	}

	client := clientFromContext(ctx, r.client)
	builder := client.UserSubscription.Create().
		SetUserID(sub.UserID).
		SetPlanID(sub.PlanID).
		SetExpiresAt(sub.ExpiresAt).
		SetNillableDailyWindowStart(sub.DailyWindowStart).
		SetNillableWeeklyWindowStart(sub.WeeklyWindowStart).
		SetNillableMonthlyWindowStart(sub.MonthlyWindowStart).
		SetNillableDailyLimitUsd(sub.DailyLimitUSD).
		SetNillableWeeklyLimitUsd(sub.WeeklyLimitUSD).
		SetNillableMonthlyLimitUsd(sub.MonthlyLimitUSD).
		SetDailyUsageUsd(sub.DailyUsageUSD).
		SetWeeklyUsageUsd(sub.WeeklyUsageUSD).
		SetMonthlyUsageUsd(sub.MonthlyUsageUSD).
		SetNillableAssignedBy(sub.AssignedBy).
		SetNillableSourceOrderID(sub.SourceOrderID)

	if sub.StartsAt.IsZero() {
		builder.SetStartsAt(time.Now())
	} else {
		builder.SetStartsAt(sub.StartsAt)
	}
	if sub.Status != "" {
		builder.SetStatus(sub.Status)
	}
	if !sub.AssignedAt.IsZero() {
		builder.SetAssignedAt(sub.AssignedAt)
	}
	builder.SetNotes(sub.Notes)

	created, err := builder.Save(ctx)
	if err == nil {
		applyUserSubscriptionEntityToService(sub, created)
	}
	return translatePersistenceError(err, nil, service.ErrSubscriptionAlreadyExists)
}

func (r *userSubscriptionRepository) GetByID(ctx context.Context, id int64) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	m, err := client.UserSubscription.Query().
		Where(usersubscription.IDEQ(id)).
		WithUser().
		WithPlan().
		WithAssignedByUser().
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	return userSubscriptionEntityToService(m), nil
}

// GetByIDIncludeDeleted 绕过软删除过滤查询订阅，供恢复撤销订阅时读取原始记录。
func (r *userSubscriptionRepository) GetByIDIncludeDeleted(ctx context.Context, id int64) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	queryCtx := mixins.SkipSoftDelete(ctx)
	m, err := client.UserSubscription.Query().
		Where(usersubscription.IDEQ(id)).
		WithUser().
		WithPlan().
		WithAssignedByUser().
		Only(queryCtx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	return userSubscriptionEntityToServicePreserveStatus(m), nil
}

func (r *userSubscriptionRepository) GetLatestByUserIDAndPlanID(ctx context.Context, userID, planID int64) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	m, err := client.UserSubscription.Query().
		Where(
			usersubscription.UserIDEQ(userID),
			usersubscription.PlanIDEQ(planID),
		).
		WithPlan().
		Order(
			dbent.Desc(usersubscription.FieldExpiresAt),
			dbent.Desc(usersubscription.FieldCreatedAt),
		).
		First(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	return userSubscriptionEntityToService(m), nil
}

func (r *userSubscriptionRepository) Update(ctx context.Context, sub *service.UserSubscription) error {
	if sub == nil {
		return service.ErrSubscriptionNilInput
	}

	return r.mutateWithResetCountSettlement(ctx, sub.ID, false, func(txCtx context.Context, client *dbent.Client, row *dbent.UserSubscription) error {
		// 通用编辑的计数及水位来自锁内新快照，避免旧输入覆盖已结清的时间表。
		builder := client.UserSubscription.UpdateOneID(sub.ID).
			SetUserID(sub.UserID).
			SetPlanID(sub.PlanID).
			SetStartsAt(sub.StartsAt).
			SetExpiresAt(sub.ExpiresAt).
			SetStatus(sub.Status).
			SetNillableDailyWindowStart(sub.DailyWindowStart).
			SetNillableWeeklyWindowStart(sub.WeeklyWindowStart).
			SetNillableMonthlyWindowStart(sub.MonthlyWindowStart).
			SetNillableDailyLimitUsd(sub.DailyLimitUSD).
			SetNillableWeeklyLimitUsd(sub.WeeklyLimitUSD).
			SetNillableMonthlyLimitUsd(sub.MonthlyLimitUSD).
			SetDailyUsageUsd(sub.DailyUsageUSD).
			SetWeeklyUsageUsd(sub.WeeklyUsageUSD).
			SetMonthlyUsageUsd(sub.MonthlyUsageUSD).
			SetNillableAssignedBy(sub.AssignedBy).
			SetAssignedAt(sub.AssignedAt).
			SetNillableSourceOrderID(sub.SourceOrderID).
			SetNotes(sub.Notes)
		setSubscriptionResetCountMutation(builder.Mutation(), row)

		updated, err := builder.Save(txCtx)
		if err == nil {
			applyUserSubscriptionEntityToService(sub, updated)
			return nil
		}
		return translatePersistenceError(err, service.ErrSubscriptionNotFound, service.ErrSubscriptionAlreadyExists)
	})
}

func (r *userSubscriptionRepository) Delete(ctx context.Context, id int64) error {
	err := r.mutateWithResetCountSettlement(ctx, id, false, func(txCtx context.Context, client *dbent.Client, row *dbent.UserSubscription) error {
		// 复用删除钩子将计数与软删除合并为同一条更新，并保留内部显式物理删除的语义。
		mutation := client.UserSubscription.Update().Where(usersubscription.IDEQ(id)).Mutation()
		setSubscriptionResetCountMutation(mutation, row)
		mutation.SetOp(dbent.OpDelete)
		_, err := client.Mutate(txCtx, mutation)
		return err
	})
	// 保持原批量删除的幂等语义：不存在或已经软删除的记录视为完成。
	if errors.Is(err, service.ErrSubscriptionNotFound) {
		return nil
	}
	return err
}

// Restore 清除订阅软删除标记，并按当前时间窗口写回恢复后的状态。
func (r *userSubscriptionRepository) Restore(ctx context.Context, subscriptionID int64, restoredStatus string) (*service.UserSubscription, error) {
	err := r.mutateWithResetCountSettlement(ctx, subscriptionID, true, func(txCtx context.Context, client *dbent.Client, row *dbent.UserSubscription) error {
		update := client.UserSubscription.UpdateOneID(subscriptionID).
			SetStatus(restoredStatus).
			ClearDeletedAt().
			SetUpdatedAt(time.Now())
		setSubscriptionResetCountMutation(update.Mutation(), row)
		_, err := update.Save(txCtx)
		return err
	})
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, service.ErrSubscriptionAlreadyExists)
	}
	return r.GetByID(ctx, subscriptionID)
}

func (r *userSubscriptionRepository) ListByUserID(ctx context.Context, userID int64) ([]service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	subs, err := client.UserSubscription.Query().
		Where(usersubscription.UserIDEQ(userID)).
		WithPlan().
		Order(
			dbent.Desc(usersubscription.FieldExpiresAt),
			dbent.Desc(usersubscription.FieldCreatedAt),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return userSubscriptionEntitiesToService(subs), nil
}

func (r *userSubscriptionRepository) ListByUserIDAndPlanID(ctx context.Context, userID, planID int64) ([]service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	subs, err := client.UserSubscription.Query().
		Where(
			usersubscription.UserIDEQ(userID),
			usersubscription.PlanIDEQ(planID),
		).
		WithPlan().
		Order(
			dbent.Asc(usersubscription.FieldStartsAt),
			dbent.Asc(usersubscription.FieldCreatedAt),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return userSubscriptionEntitiesToService(subs), nil
}

func (r *userSubscriptionRepository) ListActiveByUserID(ctx context.Context, userID int64) ([]service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	now := time.Now()
	subs, err := client.UserSubscription.Query().
		Where(
			usersubscription.UserIDEQ(userID),
			usersubscription.StartsAtLTE(now),
			usersubscription.ExpiresAtGT(now),
			usersubscription.StatusIn(service.SubscriptionStatusActive, service.SubscriptionStatusPending),
		).
		WithPlan().
		Order(
			dbent.Asc(usersubscription.FieldExpiresAt),
			dbent.Asc(usersubscription.FieldStartsAt),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return userSubscriptionEntitiesToService(subs), nil
}

func (r *userSubscriptionRepository) FilterByGroup(ctx context.Context, subs []service.UserSubscription, groupID int64) ([]service.UserSubscription, error) {
	if groupID <= 0 || len(subs) == 0 {
		return subs, nil
	}
	planIDs := make([]int64, 0, len(subs))
	for i := range subs {
		planIDs = append(planIDs, subs[i].PlanID)
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
		SELECT sp.id
		FROM subscription_plans sp
		WHERE sp.id = ANY($1)
			AND (
				NOT EXISTS (
					SELECT 1
					FROM subscription_plan_groups spg
					WHERE spg.plan_id = sp.id
				)
				OR EXISTS (
					SELECT 1
					FROM subscription_plan_groups spg
					WHERE spg.plan_id = sp.id
						AND spg.group_id = $2
				)
			)
	`, pq.Array(planIDs), groupID)
	if err != nil {
		return nil, err
	}

	allowed := make(map[int64]struct{}, len(planIDs))
	for rows.Next() {
		var planID int64
		if err := rows.Scan(&planID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		allowed[planID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	out := make([]service.UserSubscription, 0, len(subs))
	for i := range subs {
		if _, ok := allowed[subs[i].PlanID]; ok {
			out = append(out, subs[i])
		}
	}
	return out, nil
}

func (r *userSubscriptionRepository) ListByPlanID(ctx context.Context, planID int64, params pagination.PaginationParams) ([]service.UserSubscription, *pagination.PaginationResult, error) {
	client := clientFromContext(ctx, r.client)
	q := client.UserSubscription.Query().Where(usersubscription.PlanIDEQ(planID))

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	subs, err := q.
		WithUser().
		WithPlan().
		Order(dbent.Desc(usersubscription.FieldCreatedAt)).
		Offset(params.Offset()).
		Limit(params.Limit()).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	return userSubscriptionEntitiesToService(subs), paginationResultFromTotal(int64(total), params), nil
}

func (r *userSubscriptionRepository) List(ctx context.Context, params pagination.PaginationParams, userID, planID *int64, status, _platform, sortBy, sortOrder string) ([]service.UserSubscription, *pagination.PaginationResult, error) {
	client := clientFromContext(ctx, r.client)
	queryCtx := ctx
	if status == "" || status == service.SubscriptionStatusRevoked {
		// 管理端列表需要看到软删除订阅，以便撤销后仍能展示历史记录。
		queryCtx = mixins.SkipSoftDelete(ctx)
	}
	q := client.UserSubscription.Query()
	if userID != nil {
		q = q.Where(usersubscription.UserIDEQ(*userID))
	}
	if planID != nil {
		q = q.Where(usersubscription.PlanIDEQ(*planID))
	}

	now := time.Now()
	switch status {
	case service.SubscriptionStatusActive:
		q = q.Where(
			usersubscription.StartsAtLTE(now),
			usersubscription.ExpiresAtGT(now),
			usersubscription.StatusIn(service.SubscriptionStatusActive, service.SubscriptionStatusPending),
		)
	case service.SubscriptionStatusPending:
		q = q.Where(
			usersubscription.StatusEQ(service.SubscriptionStatusPending),
			usersubscription.StartsAtGT(now),
			usersubscription.ExpiresAtGT(now),
		)
	case service.SubscriptionStatusExpired:
		q = q.Where(
			usersubscription.Or(
				usersubscription.StatusEQ(service.SubscriptionStatusExpired),
				usersubscription.ExpiresAtLTE(now),
			),
		)
	case service.SubscriptionStatusRevoked:
		q = q.Where(usersubscription.DeletedAtNotNil())
	case "":
	default:
		q = q.Where(usersubscription.StatusEQ(status))
	}

	total, err := q.Clone().Count(queryCtx)
	if err != nil {
		return nil, nil, err
	}

	if status != "" && status != service.SubscriptionStatusRevoked {
		q = q.WithUser().WithPlan().WithAssignedByUser()
	}

	var field string
	switch sortBy {
	case "expires_at":
		field = usersubscription.FieldExpiresAt
	case "starts_at":
		field = usersubscription.FieldStartsAt
	case "status":
		field = usersubscription.FieldStatus
	default:
		field = usersubscription.FieldCreatedAt
	}

	if sortOrder == "asc" && sortBy != "" {
		q = q.Order(dbent.Asc(field))
	} else {
		q = q.Order(dbent.Desc(field))
	}

	subs, err := q.
		Offset(params.Offset()).
		Limit(params.Limit()).
		All(queryCtx)
	if err != nil {
		return nil, nil, err
	}

	result := userSubscriptionEntitiesToService(subs)
	if status == "" || status == service.SubscriptionStatusRevoked {
		if err := r.attachUserSubscriptionRelations(ctx, result); err != nil {
			return nil, nil, err
		}
	}

	return result, paginationResultFromTotal(int64(total), params), nil
}

func (r *userSubscriptionRepository) ListBySourceOrderID(ctx context.Context, sourceOrderID int64) ([]service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	subs, err := client.UserSubscription.Query().
		Where(usersubscription.SourceOrderIDEQ(sourceOrderID)).
		WithPlan().
		Order(
			dbent.Asc(usersubscription.FieldStartsAt),
			dbent.Asc(usersubscription.FieldCreatedAt),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return userSubscriptionEntitiesToService(subs), nil
}

func (r *userSubscriptionRepository) ExtendExpiry(ctx context.Context, subscriptionID int64, newExpiresAt time.Time) error {
	return r.mutateWithResetCountSettlement(ctx, subscriptionID, false, func(txCtx context.Context, client *dbent.Client, row *dbent.UserSubscription) error {
		update := client.UserSubscription.UpdateOneID(subscriptionID).SetExpiresAt(newExpiresAt)
		setSubscriptionResetCountMutation(update.Mutation(), row)
		_, err := update.Save(txCtx)
		return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	})
}

func (r *userSubscriptionRepository) UpdateStatus(ctx context.Context, subscriptionID int64, status string) error {
	return r.mutateWithResetCountSettlement(ctx, subscriptionID, false, func(txCtx context.Context, client *dbent.Client, row *dbent.UserSubscription) error {
		update := client.UserSubscription.UpdateOneID(subscriptionID).SetStatus(status)
		setSubscriptionResetCountMutation(update.Mutation(), row)
		_, err := update.Save(txCtx)
		return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	})
}

func (r *userSubscriptionRepository) UpdateNotes(ctx context.Context, subscriptionID int64, notes string) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(subscriptionID).
		SetNotes(notes).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) ActivateWindows(ctx context.Context, id int64, start time.Time, activation service.SubscriptionWindowActivation) error {
	if !activation.Any() {
		return nil
	}
	return r.mutateWithResetCountSettlement(ctx, id, false, func(txCtx context.Context, client *dbent.Client, row *dbent.UserSubscription) error {
		update := client.UserSubscription.UpdateOneID(id)
		setSubscriptionResetCountMutation(update.Mutation(), row)
		// 只激活锁内仍为空的窗口，避免扫描旧快照覆盖其他请求已经建立的锚点。
		if activation.Daily && row.DailyWindowStart == nil {
			update.SetDailyWindowStart(timezone.StartOfDay(start))
		}
		if activation.Weekly && row.WeeklyWindowStart == nil {
			update.SetWeeklyWindowStart(start)
		}
		if activation.Monthly && row.MonthlyWindowStart == nil {
			update.SetMonthlyWindowStart(start)
		}
		_, err := update.Save(txCtx)
		return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	})
}

func (r *userSubscriptionRepository) ResetUsageWindows(ctx context.Context, id int64, resetDaily, resetWeekly, resetMonthly bool, newWindowStart time.Time) error {
	return r.mutateWithResetCountSettlement(ctx, id, false, func(txCtx context.Context, client *dbent.Client, row *dbent.UserSubscription) error {
		update := client.UserSubscription.UpdateOneID(id)
		setSubscriptionResetCountMutation(update.Mutation(), row)
		if resetDaily {
			// 日额度按配置时区零点刷新，周/月保留实际手动重置时刻。
			update.SetDailyUsageUsd(0).SetDailyWindowStart(timezone.StartOfDay(newWindowStart))
		}
		if resetWeekly {
			update.SetWeeklyUsageUsd(0).SetWeeklyWindowStart(newWindowStart)
		}
		if resetMonthly {
			update.SetMonthlyUsageUsd(0).SetMonthlyWindowStart(newWindowStart)
		}
		_, err := update.Save(txCtx)
		return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	})
}

func (r *userSubscriptionRepository) ResetDailyUsage(ctx context.Context, id int64, expectedWindowStart *time.Time, newWindowStart time.Time) error {
	return r.mutateWithResetCountSettlement(ctx, id, false, func(txCtx context.Context, client *dbent.Client, row *dbent.UserSubscription) error {
		update := client.UserSubscription.UpdateOneID(id)
		setSubscriptionResetCountMutation(update.Mutation(), row)
		// 在行锁内比较旧锚点；过时请求只结清计数，不覆盖新窗口已经产生的消费。
		if subscriptionWindowStartMatches(row.DailyWindowStart, expectedWindowStart) {
			update.SetDailyUsageUsd(0).SetDailyWindowStart(newWindowStart)
		} else {
			update.SetUpdatedAt(row.UpdatedAt)
		}
		_, err := update.Save(txCtx)
		return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	})
}

func (r *userSubscriptionRepository) ResetWeeklyUsage(ctx context.Context, id int64, expectedWindowStart *time.Time, newWindowStart time.Time) error {
	return r.mutateWithResetCountSettlement(ctx, id, false, func(txCtx context.Context, client *dbent.Client, row *dbent.UserSubscription) error {
		update := client.UserSubscription.UpdateOneID(id)
		setSubscriptionResetCountMutation(update.Mutation(), row)
		if subscriptionWindowStartMatches(row.WeeklyWindowStart, expectedWindowStart) {
			update.SetWeeklyUsageUsd(0).SetWeeklyWindowStart(newWindowStart)
		} else {
			update.SetUpdatedAt(row.UpdatedAt)
		}
		_, err := update.Save(txCtx)
		return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	})
}

func (r *userSubscriptionRepository) ResetMonthlyUsage(ctx context.Context, id int64, expectedWindowStart *time.Time, newWindowStart time.Time) error {
	return r.mutateWithResetCountSettlement(ctx, id, false, func(txCtx context.Context, client *dbent.Client, row *dbent.UserSubscription) error {
		update := client.UserSubscription.UpdateOneID(id)
		setSubscriptionResetCountMutation(update.Mutation(), row)
		if subscriptionWindowStartMatches(row.MonthlyWindowStart, expectedWindowStart) {
			update.SetMonthlyUsageUsd(0).SetMonthlyWindowStart(newWindowStart)
		} else {
			update.SetUpdatedAt(row.UpdatedAt)
		}
		_, err := update.Save(txCtx)
		return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	})
}

// subscriptionWindowStartMatches 保留原条件重置的空值语义，调用方必须持有订阅行锁。
func subscriptionWindowStartMatches(current, expected *time.Time) bool {
	if current == nil || expected == nil {
		return current == nil && expected == nil
	}
	return current.Equal(*expected)
}

func (r *userSubscriptionRepository) IncrementUsage(ctx context.Context, id int64, costUSD float64) error {
	const updateSQL = `
		UPDATE user_subscriptions
		SET
			daily_usage_usd = daily_usage_usd + $1,
			weekly_usage_usd = weekly_usage_usd + $1,
			monthly_usage_usd = monthly_usage_usd + $1,
			updated_at = NOW()
		WHERE id = $2
			AND deleted_at IS NULL
	`

	client := clientFromContext(ctx, r.client)
	result, err := client.ExecContext(ctx, updateSQL, costUSD, id)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	return service.ErrSubscriptionNotFound
}

func (r *userSubscriptionRepository) BatchUpdateExpiredStatus(ctx context.Context) (int64, error) {
	client := clientFromContext(ctx, r.client)
	n, err := client.UserSubscription.Update().
		Where(
			usersubscription.StatusIn(service.SubscriptionStatusActive, service.SubscriptionStatusPending),
			usersubscription.ExpiresAtLTE(time.Now()),
		).
		SetStatus(service.SubscriptionStatusExpired).
		Save(ctx)
	return int64(n), err
}

func (r *userSubscriptionRepository) ListExpired(ctx context.Context) ([]service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	subs, err := client.UserSubscription.Query().
		Where(usersubscription.ExpiresAtLTE(time.Now())).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return userSubscriptionEntitiesToService(subs), nil
}

func (r *userSubscriptionRepository) CountByGroupID(_ context.Context, _ int64) (int64, error) {
	return 0, nil
}

func (r *userSubscriptionRepository) CountActiveByGroupID(_ context.Context, _ int64) (int64, error) {
	return 0, nil
}

func (r *userSubscriptionRepository) DeleteByGroupID(_ context.Context, _ int64) (int64, error) {
	return 0, nil
}

func (r *userSubscriptionRepository) attachUserSubscriptionRelations(ctx context.Context, subs []service.UserSubscription) error {
	if len(subs) == 0 {
		return nil
	}

	userIDs := make([]int64, 0, len(subs))
	planIDs := make([]int64, 0, len(subs))
	assignedByIDs := make([]int64, 0, len(subs))
	for i := range subs {
		userIDs = append(userIDs, subs[i].UserID)
		planIDs = append(planIDs, subs[i].PlanID)
		if subs[i].AssignedBy != nil {
			assignedByIDs = append(assignedByIDs, *subs[i].AssignedBy)
		}
	}

	client := clientFromContext(ctx, r.client)
	users, err := client.User.Query().Where(dbuser.IDIn(uniquePositiveInt64s(userIDs)...)).All(ctx)
	if err != nil {
		return err
	}
	userByID := make(map[int64]*service.User, len(users))
	for _, u := range users {
		userByID[u.ID] = userEntityToService(u)
	}

	plans, err := client.SubscriptionPlan.Query().Where(subscriptionplan.IDIn(uniquePositiveInt64s(planIDs)...)).All(ctx)
	if err != nil {
		return err
	}
	planByID := make(map[int64]*service.SubscriptionPlan, len(plans))
	for _, plan := range plans {
		planByID[plan.ID] = subscriptionPlanEntityToService(plan)
	}

	assignedByID := map[int64]*service.User{}
	if len(assignedByIDs) > 0 {
		assignedUsers, err := client.User.Query().Where(dbuser.IDIn(uniquePositiveInt64s(assignedByIDs)...)).All(ctx)
		if err != nil {
			return err
		}
		assignedByID = make(map[int64]*service.User, len(assignedUsers))
		for _, u := range assignedUsers {
			assignedByID[u.ID] = userEntityToService(u)
		}
	}

	for i := range subs {
		subs[i].User = userByID[subs[i].UserID]
		subs[i].Plan = planByID[subs[i].PlanID]
		if subs[i].AssignedBy != nil {
			subs[i].AssignedByUser = assignedByID[*subs[i].AssignedBy]
		}
	}
	return nil
}

func userSubscriptionEntityToService(m *dbent.UserSubscription) *service.UserSubscription {
	return userSubscriptionEntityToServiceWithStatusMapping(m, true)
}

// userSubscriptionEntityToServicePreserveStatus 保留软删除记录的持久化状态，避免恢复逻辑误把 revoked 写回数据库。
func userSubscriptionEntityToServicePreserveStatus(m *dbent.UserSubscription) *service.UserSubscription {
	return userSubscriptionEntityToServiceWithStatusMapping(m, false)
}

func userSubscriptionEntityToServiceWithStatusMapping(m *dbent.UserSubscription, mapDeletedToRevoked bool) *service.UserSubscription {
	if m == nil {
		return nil
	}
	status := m.Status
	if mapDeletedToRevoked && m.DeletedAt != nil {
		status = service.SubscriptionStatusRevoked
	}
	out := &service.UserSubscription{
		ID:                 m.ID,
		UserID:             m.UserID,
		PlanID:             m.PlanID,
		StartsAt:           m.StartsAt,
		ExpiresAt:          m.ExpiresAt,
		Status:             status,
		DailyWindowStart:   m.DailyWindowStart,
		WeeklyWindowStart:  m.WeeklyWindowStart,
		MonthlyWindowStart: m.MonthlyWindowStart,
		DailyLimitUSD:      m.DailyLimitUsd,
		WeeklyLimitUSD:     m.WeeklyLimitUsd,
		MonthlyLimitUSD:    m.MonthlyLimitUsd,
		DailyUsageUSD:      m.DailyUsageUsd,
		WeeklyUsageUSD:     m.WeeklyUsageUsd,
		MonthlyUsageUSD:    m.MonthlyUsageUsd,
		DailyResetCount:    m.DailyResetCount,
		WeeklyResetCount:   m.WeeklyResetCount,
		MonthlyResetCount:  m.MonthlyResetCount,
		ResetCountedAt:     m.ResetCountedAt,
		AssignedBy:         m.AssignedBy,
		AssignedAt:         m.AssignedAt,
		SourceOrderID:      m.SourceOrderID,
		Notes:              derefString(m.Notes),
		CreatedAt:          m.CreatedAt,
		UpdatedAt:          m.UpdatedAt,
		DeletedAt:          m.DeletedAt,
	}
	if m.Edges.User != nil {
		out.User = userEntityToService(m.Edges.User)
	}
	if m.Edges.Plan != nil {
		out.Plan = subscriptionPlanEntityToService(m.Edges.Plan)
	}
	if m.Edges.AssignedByUser != nil {
		out.AssignedByUser = userEntityToService(m.Edges.AssignedByUser)
	}
	return out
}

func userSubscriptionEntitiesToService(models []*dbent.UserSubscription) []service.UserSubscription {
	out := make([]service.UserSubscription, 0, len(models))
	for i := range models {
		if s := userSubscriptionEntityToService(models[i]); s != nil {
			out = append(out, *s)
		}
	}
	return out
}

func applyUserSubscriptionEntityToService(dst *service.UserSubscription, src *dbent.UserSubscription) {
	if dst == nil || src == nil {
		return
	}
	dst.ID = src.ID
	dst.CreatedAt = src.CreatedAt
	dst.UpdatedAt = src.UpdatedAt
	// 创建及普通编辑回传数据库权威次数，不保留调用方传入的旧计数快照。
	dst.DailyResetCount = src.DailyResetCount
	dst.WeeklyResetCount = src.WeeklyResetCount
	dst.MonthlyResetCount = src.MonthlyResetCount
	dst.ResetCountedAt = src.ResetCountedAt
}

func subscriptionPlanEntityToService(plan *dbent.SubscriptionPlan) *service.SubscriptionPlan {
	if plan == nil {
		return nil
	}
	return &service.SubscriptionPlan{
		ID:                   plan.ID,
		Name:                 plan.Name,
		Description:          plan.Description,
		Price:                plan.Price,
		OriginalPrice:        plan.OriginalPrice,
		Currency:             plan.Currency,
		ValidityDays:         plan.ValidityDays,
		ValidityUnit:         plan.ValidityUnit,
		GroupIDs:             append([]int64(nil), plan.GroupIds...),
		GroupRateMultipliers: cloneInt64Float64Map(plan.GroupRateMultipliers),
		DailyLimitUSD:        plan.DailyLimitUsd,
		WeeklyLimitUSD:       plan.WeeklyLimitUsd,
		MonthlyLimitUSD:      plan.MonthlyLimitUsd,
		Features:             plan.Features,
		ProductName:          plan.ProductName,
		ForSale:              plan.ForSale,
		SortOrder:            plan.SortOrder,
		CreatedAt:            plan.CreatedAt,
		UpdatedAt:            plan.UpdatedAt,
	}
}

func cloneInt64Float64Map(in map[int64]float64) map[int64]float64 {
	if len(in) == 0 {
		return map[int64]float64{}
	}
	out := make(map[int64]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

var _ service.UserSubscriptionRepository = (*userSubscriptionRepository)(nil)
