package repository

import (
	"context"
	"errors"
	"time"

	"entgo.io/ent/dialect/sql"
	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
	"github.com/TokenFlux/TokenRouter/ent/usersubscription"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

const subscriptionResetCountBatchSize = 200

// withSubscriptionCountTransaction 复用业务事务；单条管理操作则自行建立短事务。
func (r *userSubscriptionRepository) withSubscriptionCountTransaction(ctx context.Context, apply func(context.Context, *dbent.Client) error) error {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return apply(ctx, tx.Client())
	}
	tx, err := r.client.Tx(ctx)
	if errors.Is(err, dbent.ErrTxStarted) {
		// 仓储也可直接由 tx.Client() 构造，此时由外层调用者提交，不再开启嵌套事务。
		return apply(ctx, r.client)
	}
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := apply(dbent.NewTxContext(ctx, tx), tx.Client()); err != nil {
		return err
	}
	return tx.Commit()
}

// mutateWithResetCountSettlement 锁定数据库最新状态并结清旧时间表，再执行窗口或有效期变更。
// 不能拿调用方的旧快照计算次数，否则计费、后台扫描和管理操作并发时会重复或漏记。
func (r *userSubscriptionRepository) mutateWithResetCountSettlement(ctx context.Context, id int64, includeDeleted bool, apply func(context.Context, *dbent.Client, *dbent.UserSubscription) error) error {
	return r.withSubscriptionCountTransaction(ctx, func(txCtx context.Context, client *dbent.Client) error {
		if includeDeleted {
			txCtx = mixins.SkipSoftDelete(txCtx)
		}
		row, err := client.UserSubscription.Query().Where(usersubscription.IDEQ(id)).ForUpdate().Only(txCtx)
		if err != nil {
			return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
		}
		// 先在锁内计算，再由业务变更的同一条 UPDATE 写入，避免同事务二次更新触发额外外键锁。
		accrueSubscriptionResetCounts(row, time.Now())
		return apply(txCtx, client, row)
	})
}

// accrueSubscriptionResetCounts 仅更新已锁定的内存快照，不独立写入数据库。
func accrueSubscriptionResetCounts(row *dbent.UserSubscription, now time.Time) bool {
	sub := userSubscriptionEntityToServicePreserveStatus(row)
	if !sub.AccrueScheduledResetCounts(now) {
		return false
	}
	row.DailyResetCount, row.WeeklyResetCount, row.MonthlyResetCount = sub.DailyResetCount, sub.WeeklyResetCount, sub.MonthlyResetCount
	row.ResetCountedAt = sub.ResetCountedAt
	return true
}

// setSubscriptionResetCountMutation 必须使用锁内新快照，不能使用调用方传入的历史计数。
func setSubscriptionResetCountMutation(mutation *dbent.UserSubscriptionMutation, row *dbent.UserSubscription) {
	mutation.SetDailyResetCount(row.DailyResetCount)
	mutation.SetWeeklyResetCount(row.WeeklyResetCount)
	mutation.SetMonthlyResetCount(row.MonthlyResetCount)
	mutation.SetResetCountedAt(row.ResetCountedAt)
}

// settleSubscriptionResetCounts 只写计数及水位，保留实际额度、窗口与业务更新时间。
// 调用方必须已持有该订阅行锁，不能用于普通列表的无锁读取。
func settleSubscriptionResetCounts(ctx context.Context, client *dbent.Client, row *dbent.UserSubscription, now time.Time) error {
	if !accrueSubscriptionResetCounts(row, now) {
		return nil
	}
	update := client.UserSubscription.UpdateOneID(row.ID).SetUpdatedAt(row.UpdatedAt)
	setSubscriptionResetCountMutation(update.Mutation(), row)
	_, err := update.Save(ctx)
	return err
}

// RefreshScheduledResetCounts 让没有网关请求的订阅也按时间表累计，每批独立提交。
// 固定本轮上界并使用行锁跳过繁忙记录，多实例扫描不会重复累计；下轮继续补处理跳过的行。
func (r *userSubscriptionRepository) RefreshScheduledResetCounts(ctx context.Context, now time.Time) error {
	for {
		processed := 0
		err := r.withSubscriptionCountTransaction(ctx, func(txCtx context.Context, client *dbent.Client) error {
			rows, err := client.UserSubscription.Query().Where(
				usersubscription.ResetCountedAtLT(now),
				usersubscription.StartsAtLTE(now),
				func(s *sql.Selector) {
					// 已过期记录仍须结清到期前尚未统计的周期，结清后不再反复扫描。
					s.Where(sql.ColumnsLT(s.C(usersubscription.FieldResetCountedAt), s.C(usersubscription.FieldExpiresAt)))
				},
			).Order(dbent.Asc(usersubscription.FieldResetCountedAt), dbent.Asc(usersubscription.FieldID)).
				Limit(subscriptionResetCountBatchSize).
				ForUpdate(sql.WithLockAction(sql.SkipLocked)).All(txCtx)
			if err != nil {
				return err
			}
			processed = len(rows)
			for _, row := range rows {
				if err := settleSubscriptionResetCounts(txCtx, client, row, now); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if processed < subscriptionResetCountBatchSize {
			return nil
		}
	}
}
