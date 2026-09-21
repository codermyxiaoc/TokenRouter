package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"entgo.io/ent/dialect"
	dbent "github.com/TokenFlux/TokenRouter/ent"
	dbuser "github.com/TokenFlux/TokenRouter/ent/user"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

// MaxBulkSubscriptions 限制单次事务的订阅数量，避免长时间占用用户锁。
const MaxBulkSubscriptions = 100

// BulkSubscriptionResult 只返回确认提交的订阅，整批失败时不返回部分成功。
type BulkSubscriptionResult struct {
	SubscriptionIDs []int64 `json:"subscription_ids"`
	UpdatedCount    int     `json:"updated_count"`
}

// BulkExtendSubscriptions 在原到期时间上增加天数；已过期订阅从当前时间起算。
// @project-doc docs/domains/payments_and_entitlements.md#subscription_admin_bulk
func (s *SubscriptionService) BulkExtendSubscriptions(ctx context.Context, ids []int64, days int) (*BulkSubscriptionResult, error) {
	if days < 1 || days > MaxValidityDays {
		return nil, infraerrors.BadRequest("SUBSCRIPTION_BULK_DAYS_INVALID", "extension days must be between 1 and 36500")
	}
	return s.mutateSubscriptionsBulk(ctx, ids, subscriptionBulkExtend, func(txCtx context.Context, sub *UserSubscription) error {
		if sub.ExpiresAt.After(time.Now()) {
			return nil
		}
		chain, err := s.userSubRepo.ListByUserIDAndPlanID(txCtx, sub.UserID, sub.PlanID)
		if err != nil {
			return err
		}
		// 不批量恢复已有后继记录的历史订阅，避免单条顺延规则重新激活未选中的历史权益。
		for _, later := range chain {
			if later.ID != sub.ID && later.Status != SubscriptionStatusRevoked && !later.StartsAt.Before(sub.ExpiresAt) {
				return infraerrors.Conflict("SUBSCRIPTION_BULK_EXPIRED_HAS_SUCCESSOR", "expired subscription has later subscriptions; extend the latest subscription instead")
			}
		}
		return nil
	}, func(txCtx context.Context, id int64) error {
		_, err := s.ExtendSubscription(txCtx, id, days)
		return err
	})
}

// BulkResetSubscriptionQuota 复用单条重置规则，不调整有效期、计划或额度上限。
func (s *SubscriptionService) BulkResetSubscriptionQuota(ctx context.Context, ids []int64, daily, weekly, monthly bool) (*BulkSubscriptionResult, error) {
	if !daily && !weekly && !monthly {
		return nil, ErrInvalidInput
	}
	return s.mutateSubscriptionsBulk(ctx, ids, subscriptionBulkReset, nil, func(txCtx context.Context, id int64) error {
		_, err := s.AdminResetQuota(txCtx, id, daily, weekly, monthly)
		return err
	})
}

// BulkRevokeSubscriptions 在整批事务内撤销订阅，沿用单条撤销的后继时间链平移规则。
func (s *SubscriptionService) BulkRevokeSubscriptions(ctx context.Context, ids []int64) (*BulkSubscriptionResult, error) {
	return s.mutateSubscriptionsBulk(ctx, ids, subscriptionBulkRevoke, nil, s.RevokeSubscription)
}

// BulkRestoreSubscriptions 只恢复已撤销记录，保留原有效期、已用额度和时间链冲突检查。
func (s *SubscriptionService) BulkRestoreSubscriptions(ctx context.Context, ids []int64) (*BulkSubscriptionResult, error) {
	return s.mutateSubscriptionsBulk(ctx, ids, subscriptionBulkRestore, nil, func(txCtx context.Context, id int64) error {
		// 整批入口已按用户 ID 加锁，直接复用锁内恢复方法避免重新安排锁顺序。
		_, err := s.restoreSubscriptionUnlocked(txCtx, id)
		return err
	})
}

// subscriptionBulkAction 将资格校验与动作绑定，防止延长或重置误恢复已撤销记录。
type subscriptionBulkAction uint8

const (
	subscriptionBulkExtend subscriptionBulkAction = iota
	subscriptionBulkReset
	subscriptionBulkRevoke
	subscriptionBulkRestore
)

func validateSubscriptionBulkAction(sub *UserSubscription, action subscriptionBulkAction) error {
	switch action {
	case subscriptionBulkRestore:
		if sub.DeletedAt == nil {
			return ErrSubscriptionNotRevoked
		}
	case subscriptionBulkRevoke:
		// 与管理员单条撤销一致，待生效、过期和暂停记录也允许撤销。
		if sub.DeletedAt != nil {
			return ErrSubscriptionNotActive
		}
	default:
		if sub.Status != SubscriptionStatusActive && sub.Status != SubscriptionStatusPending && sub.Status != SubscriptionStatusExpired {
			return ErrSubscriptionNotActive
		}
	}
	return nil
}

// normalizeBulkSubscriptionIDs 先校验再去重，保持请求顺序且每个订阅只处理一次。
func normalizeBulkSubscriptionIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 || len(ids) > MaxBulkSubscriptions {
		return nil, infraerrors.BadRequest("SUBSCRIPTION_BULK_IDS_INVALID", "select between 1 and 100 subscriptions")
	}
	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, infraerrors.BadRequest("SUBSCRIPTION_BULK_IDS_INVALID", "subscription IDs must be positive")
		}
		if !seen[id] {
			unique = append(unique, id)
			seen[id] = true
		}
	}
	return unique, nil
}

// mutateSubscriptionsBulk 串行复用单条规则，避免同用户同套餐的时间链并发覆盖。
func (s *SubscriptionService) mutateSubscriptionsBulk(ctx context.Context, ids []int64, action subscriptionBulkAction, validate func(context.Context, *UserSubscription) error, mutate func(context.Context, int64) error) (*BulkSubscriptionResult, error) {
	ids, err := normalizeBulkSubscriptionIDs(ids)
	if err != nil {
		return nil, err
	}
	load := s.userSubRepo.GetByID
	if action == subscriptionBulkRestore {
		// 恢复必须绕过软删除过滤，但其他动作仍不可见已撤销记录。
		load = s.userSubRepo.GetByIDIncludeDeleted
	}
	err = s.withSubscriptionMutationTx(ctx, func(txCtx context.Context) error {
		tx := dbent.TxFromContext(txCtx)
		users := make(map[int64]bool)
		for _, id := range ids {
			sub, err := load(txCtx, id)
			if err != nil {
				return subscriptionBulkItemError(id, err)
			}
			users[sub.UserID] = true
		}
		userIDs := make([]int64, 0, len(users))
		for userID := range users {
			userIDs = append(userIDs, userID)
		}
		sort.Slice(userIDs, func(i, j int) bool { return userIDs[i] < userIDs[j] })
		for _, userID := range userIDs {
			query := tx.User.Query().Where(dbuser.IDEQ(userID))
			// 与余额支付的测试数据库兼容；生产 PostgreSQL 必须持有用户行锁。
			if s.entClient.Driver().Dialect() != dialect.SQLite {
				query.ForUpdate()
			}
			if _, err := query.Only(txCtx); err != nil {
				return fmt.Errorf("lock subscription user %d: %w", userID, err)
			}
		}
		// 锁后复核所有记录，按各动作资格校验，不能借延长或重置绕过撤销状态。
		ordered := make([]*UserSubscription, 0, len(ids))
		for _, id := range ids {
			sub, err := load(txCtx, id)
			if err != nil {
				return subscriptionBulkItemError(id, err)
			}
			if err := validateSubscriptionBulkAction(sub, action); err != nil {
				return subscriptionBulkItemError(id, err)
			}
			if validate != nil {
				if err := validate(txCtx, sub); err != nil {
					return subscriptionBulkItemError(id, err)
				}
			}
			ordered = append(ordered, sub)
		}
		// 先延长时间链前面的记录，使过期订阅的起算点不受勾选顺序影响。
		sort.Slice(ordered, func(i, j int) bool {
			left, right := ordered[i], ordered[j]
			if left.UserID != right.UserID {
				return left.UserID < right.UserID
			}
			if left.PlanID != right.PlanID {
				return left.PlanID < right.PlanID
			}
			if !left.StartsAt.Equal(right.StartsAt) {
				return left.StartsAt.Before(right.StartsAt)
			}
			return left.ID < right.ID
		})
		for _, sub := range ordered {
			if err := mutate(txCtx, sub.ID); err != nil {
				return subscriptionBulkItemError(sub.ID, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &BulkSubscriptionResult{SubscriptionIDs: ids, UpdatedCount: len(ids)}, nil
}

// subscriptionBulkItemError 仅返回安全业务原因和定位 ID，内部数据库错误仍留在服务端。
func subscriptionBulkItemError(id int64, err error) error {
	statusCode, status := infraerrors.ToHTTP(err)
	return infraerrors.New(statusCode, status.Reason, fmt.Sprintf("subscription #%d: %s", id, status.Message)).
		WithMetadata(map[string]string{"subscription_id": strconv.FormatInt(id, 10)}).WithCause(err)
}
