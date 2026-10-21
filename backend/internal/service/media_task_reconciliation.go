package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
)

var ErrMediaTaskSettlementConflict = infraerrors.New(http.StatusConflict, "MEDIA_TASK_SETTLEMENT_CONFLICT", "任务账务状态已变化或没有可处理的预留，请刷新后核对")

// MediaTaskBillingReview 区分真实资金预留和缺少使用记录，不能把空费用当作冻结金额。
type MediaTaskBillingReview struct {
	Status                 string     `json:"status"`
	HeldBalanceAmount      float64    `json:"held_balance_amount"`
	HeldSubscriptionAmount float64    `json:"held_subscription_amount"`
	OriginalReservedAmount float64    `json:"original_reserved_amount"`
	ActualAmount           *float64   `json:"actual_amount,omitempty"`
	ExpiresAt              *time.Time `json:"expires_at,omitempty"`
	ResolvedAt             *time.Time `json:"resolved_at,omitempty"`
	Action                 string     `json:"action,omitempty"`
	Reason                 string     `json:"reason,omitempty"`
	ActorID                *int64     `json:"actor_id,omitempty"`
	CanCharge              bool       `json:"can_charge"`
	CanRelease             bool       `json:"can_release"`
}

type MediaTaskSettlementCommand struct {
	TaskID  int64
	ActorID int64
	Action  string
	Reason  string
	// 自动处理必须在资金事务中复核失败时间和本轮配置，不能依赖扫描快照。
	FailedBefore *time.Time
}

type MediaTaskSettlementResult struct {
	UserID      int64
	ActorUserID int64
	APIKeyID    int64
}

// 账务操作是独立可选能力，普通任务列表及预览不能隐式触发资金修改。
type MediaTaskSettlementRepository interface {
	GetMediaTaskBillingReview(context.Context, *MediaTask) (*MediaTaskBillingReview, error)
	ResolveMediaTaskBilling(context.Context, MediaTaskSettlementCommand) (*MediaTaskSettlementResult, error)
	ListExpiredMediaTaskBilling(context.Context, time.Time, int) ([]int64, error)
}

type mediaTaskTimeoutSettings interface {
	GetMediaTaskReconciliationTimeout(context.Context) (time.Duration, error)
}

func mediaTaskFailed(status string) bool {
	return status == "failed" || status == "cancelled" || status == "expired"
}

// enrichBillingReview 只读真实账本；读取失败时关闭动作，不能回落成可以退款的状态。
func (s *MediaTaskService) enrichBillingReview(ctx context.Context, task *MediaTask, actor MediaTaskActor, timeout time.Duration) {
	repo, ok := s.repo.(MediaTaskSettlementRepository)
	if !ok {
		return
	}
	review, err := repo.GetMediaTaskBillingReview(ctx, task)
	if err != nil || review == nil {
		review = &MediaTaskBillingReview{Status: "unavailable"}
	}
	if review.Status == "pending" && mediaTaskFailed(task.Status) && timeout > 0 {
		base := task.CompletedAt
		if base == nil {
			base = &task.UpdatedAt
		}
		deadline := base.Add(timeout)
		review.ExpiresAt = &deadline
	}
	if !actor.IsAdmin {
		review.CanCharge, review.CanRelease = false, false
		review.Reason, review.ActorID = "", nil
	}
	if review.ActualAmount != nil {
		task.ActualCost = review.ActualAmount
	}
	if review.Status == "expired" {
		task.Status = "expired"
	}
	task.BillingReview = review
}

func (s *MediaTaskService) reconciliationTimeout(ctx context.Context) (time.Duration, error) {
	if s.reconciliationSettings == nil {
		return time.Hour, nil
	}
	return s.reconciliationSettings.GetMediaTaskReconciliationTimeout(ctx)
}

// SettleBilling 只接受管理员对已失败任务的最终决定，金额和付款人完全由原账本确定。
// @project-doc docs/domains/media_tasks.md#media_task_reconciliation
func (s *MediaTaskService) SettleBilling(ctx context.Context, actor MediaTaskActor, id int64, action, reason string) (*MediaTask, error) {
	if !actor.IsAdmin || actor.UserID <= 0 {
		return nil, infraerrors.Forbidden("ADMIN_REQUIRED", "需要管理员权限")
	}
	reason = strings.TrimSpace(reason)
	if id <= 0 || (action != "charge" && action != "release") || reason == "" || utf8.RuneCountInString(reason) > 500 {
		return nil, ErrMediaTaskInvalid
	}
	repo, ok := s.repo.(MediaTaskSettlementRepository)
	if !ok {
		return nil, ErrMediaTaskSettlementConflict
	}
	result, err := repo.ResolveMediaTaskBilling(ctx, MediaTaskSettlementCommand{TaskID: id, ActorID: actor.UserID, Action: action, Reason: reason})
	if err != nil {
		return nil, err
	}
	s.invalidateSettlement(result)
	return s.Get(ctx, actor, id)
}

// 事务提交后即使浏览器断开也刷新缓存；重放同一决策时再次失效，修复上次失效中断。
func (s *MediaTaskService) invalidateSettlement(result *MediaTaskSettlementResult) {
	if result == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if s.settlementCache != nil {
		if err := s.settlementCache.InvalidateUserBalance(ctx, result.UserID); err != nil {
			logger.L().Warn("media_task.balance_cache_invalidation_failed")
		}
		if err := s.settlementCache.InvalidateAPIKeyRateLimit(ctx, result.APIKeyID); err != nil {
			logger.L().Warn("media_task.key_cache_invalidation_failed")
		}
	}
	if s.settlementKeys != nil {
		s.settlementKeys.InvalidateAuthCacheByUserID(ctx, result.UserID)
		if result.ActorUserID > 0 && result.ActorUserID != result.UserID {
			s.settlementKeys.InvalidateAuthCacheByUserID(ctx, result.ActorUserID)
		}
	}
}

// ReconcileFailedBilling 限量处理失败任务；配置不可读时整轮跳过，不缩短用户资金处理期限。
func (s *MediaTaskService) ReconcileFailedBilling(ctx context.Context) error {
	timeout, err := s.reconciliationTimeout(ctx)
	if err != nil {
		return err
	}
	repo, ok := s.repo.(MediaTaskSettlementRepository)
	if !ok || timeout <= 0 {
		return nil
	}
	cutoff := time.Now().UTC().Add(-timeout)
	ids, err := repo.ListExpiredMediaTaskBilling(ctx, cutoff, 100)
	if err != nil {
		return err
	}
	var failures error
	for _, id := range ids {
		if ctx.Err() != nil {
			return errors.Join(failures, ctx.Err())
		}
		result, err := repo.ResolveMediaTaskBilling(ctx, MediaTaskSettlementCommand{TaskID: id, Action: "expire", Reason: "失败任务超过账务处理期限，自动释放预留", FailedBefore: &cutoff})
		if err != nil {
			if delayed, ok := repo.(interface {
				DeferMediaTaskBillingReview(context.Context, int64) error
			}); ok {
				failures = errors.Join(failures, delayed.DeferMediaTaskBillingReview(ctx, id))
			}
			if !errors.Is(err, ErrMediaTaskSettlementConflict) {
				failures = errors.Join(failures, err)
			}
			continue
		}
		s.invalidateSettlement(result)
	}
	return failures
}

// 独立维护循环不创建生成任务；取消后先结束账务事务，再允许底层数据库关闭。
type MediaTaskReconciliationRuntime struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func ProvideMediaTaskServiceWithReconciliation(repo MediaTaskRepository, preview MediaTaskPreviewCache, upstream *VideoUpstreamService, accounts AccountRepository, settings *SettingService, cache *BillingCacheService, keys *APIKeyService) *MediaTaskService {
	s := ProvideMediaTaskServiceWithVideoContent(repo, preview, upstream, accounts)
	s.reconciliationSettings, s.settlementCache, s.settlementKeys = settings, cache, keys
	return s
}

func ProvideMediaTaskReconciliationRuntime(s *MediaTaskService) *MediaTaskReconciliationRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	r := &MediaTaskReconciliationRuntime{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			work, stop := context.WithTimeout(ctx, 45*time.Second)
			if err := s.ReconcileFailedBilling(work); err != nil && ctx.Err() == nil {
				logger.L().Warn("media_task.reconciliation_failed")
			}
			stop()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return r
}

func (r *MediaTaskReconciliationRuntime) Stop() {
	if r != nil {
		r.once.Do(r.cancel)
		<-r.done
	}
}
