package service

import (
	"context"
	"errors"
	"time"
)

// ErrUserPlatformQuotaNotFound service 层 sentinel：quota 记录不存在。
// adapter 将 repository.ErrUserPlatformQuotaNotFound 包装为此错误，
// handler 只需引用 service 包，无需直接依赖 repository 包。
var ErrUserPlatformQuotaNotFound = errors.New("user platform quota not found")

// ErrUserPlatformQuotaFKViolation 保留旧快照适配器的外键错误契约。
// 当前仓储快照只 UPDATE 既有行，缺失用户不再触发插入外键错误。
var ErrUserPlatformQuotaFKViolation = errors.New("user platform quota snapshot FK violation")

// UserPlatformQuotaSnapshot 是 service 层 flusher 向 DB 写入快照时使用的传输结构。
// 字段语义与 repository.UserPlatformQuotaSnapshot 完全对应，由 adapter 负责转换。
type UserPlatformQuotaSnapshot struct {
	UserID             int64
	Platform           string
	DailyUsageUSD      float64
	WeeklyUsageUSD     float64
	MonthlyUsageUSD    float64
	DailyWindowStart   time.Time
	WeeklyWindowStart  time.Time
	MonthlyWindowStart time.Time
}

// UserPlatformQuotaRecord service 层传输结构体（与 repository 层解耦）。
type UserPlatformQuotaRecord struct {
	UserID          int64
	Platform        string
	DailyLimitUSD   *float64
	WeeklyLimitUSD  *float64
	MonthlyLimitUSD *float64
	DailyUsageUSD   float64
	WeeklyUsageUSD  float64
	MonthlyUsageUSD float64
	// 窗口起始时间（可选，用于未来 reset 校验）
	DailyWindowStart   *time.Time
	WeeklyWindowStart  *time.Time
	MonthlyWindowStart *time.Time
}

// UserPlatformQuotaRepository 定义 service 层所需的 user × platform quota 数据访问端口。
// repository 包的 userPlatformQuotaRepository 实现此接口。
type UserPlatformQuotaRepository interface {
	// GetByUserPlatform 查询单条配额记录，未找到时返回 (nil, nil)。
	GetByUserPlatform(ctx context.Context, userID int64, platform string) (*UserPlatformQuotaRecord, error)
	// BulkInsertInitial 幂等批量插入初始配额记录（ON CONFLICT DO NOTHING）。
	BulkInsertInitial(ctx context.Context, records []UserPlatformQuotaRecord) error
	// IncrementUsageWithReset 原子地累加用量，若窗口已过期则先重置再累加。
	IncrementUsageWithReset(ctx context.Context, userID int64, platform string, cost float64, now time.Time) error
	// ListByUser 查询用户的所有平台配额记录。
	ListByUser(ctx context.Context, userID int64) ([]UserPlatformQuotaRecord, error)
	// UpsertForUser 全量替换限额；省略或清空平台时取消限制，保留既有用量与窗口。
	// 全空且没有既有行的平台不建行，避免制造无意义记录。
	UpsertForUser(ctx context.Context, userID int64, records []UserPlatformQuotaRecord) error
	// ResetExpiredWindow 重置指定窗口（"daily"|"weekly"|"monthly"）的用量与起始时间。
	// 未命中活跃记录时返回（service-side wrapper of repository.ErrUserPlatformQuotaNotFound）。
	ResetExpiredWindow(ctx context.Context, userID int64, platform string, window string, newStart time.Time) error
	// BatchSnapshotUsage 绝对值更新既有活跃行，缺失或软删除行不创建；兼容保留既有错误 sentinel。
	BatchSnapshotUsage(ctx context.Context, snapshots []UserPlatformQuotaSnapshot, now time.Time) error
}
