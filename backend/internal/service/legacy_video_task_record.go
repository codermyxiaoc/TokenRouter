package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrLegacyVideoTaskNotFound  = errors.New("旧视频任务不存在")
	ErrLegacyVideoTaskConflict  = errors.New("旧视频任务归属冲突")
	ErrLegacyVideoTaskLeaseLost = errors.New("旧视频任务执行租约已失效")
)

// LegacyVideoTask 保存旧 Grok/Seedance 任务的恢复与结算依据，不保存请求正文、凭据或媒体地址。
// BillingUserID 为零仅表示历史任务付款归属尚未恢复，不能据此假定由行为用户付款。
type LegacyVideoTask struct {
	// 仅可信画布上下文写入，创建仓储用它与原任务原子关联；不改变普通网关任务。
	CreativeExecutionID string
	ID                  int64
	Source              string
	TaskID              string
	UserID              int64
	BillingUserID       int64
	APIKeyID            int64
	GroupID             int64
	AccountID           int64
	Model               string
	RequestID           string
	Pending             *GrokVideoPendingBilling
	Status              string
	BillingStatus       string
	SettlementResult    json.RawMessage
	CreatedAt           time.Time
	UpdatedAt           time.Time
	NextPollAt          time.Time
	LeaseToken          string
	LeaseUntil          *time.Time
	Attempts            int
	LastError           string
	CompletedAt         *time.Time
	UpstreamDeletedAt   *time.Time
}

// LegacyVideoTaskDeletionRepository 原子补齐历史任务并记录删除事实，不能清除已保存的结算证据。
type LegacyVideoTaskDeletionRepository interface {
	MarkUpstreamDeleted(context.Context, *LegacyVideoTask) error
}

// LegacyVideoTaskRepository 使用数据库租约协调多实例；账务幂等仍由持久扣费账本保证。
// Save 只新增任务或补齐缺失快照；Finish 仅允许有效租约推进状态，不能更换原任务归属。
type LegacyVideoTaskRepository interface {
	Save(context.Context, *LegacyVideoTask) error
	Get(context.Context, string, string, int64, int64) (*LegacyVideoTask, error)
	ClaimDue(context.Context, time.Time, int, time.Duration) ([]*LegacyVideoTask, error)
	Checkpoint(context.Context, *LegacyVideoTask) error
	Finish(context.Context, *LegacyVideoTask) error
	Backfill(context.Context, int) (int, error)
	HasBilling(context.Context, string, int64) (bool, error)
	HasUsage(context.Context, string, int64) (bool, error)
}
