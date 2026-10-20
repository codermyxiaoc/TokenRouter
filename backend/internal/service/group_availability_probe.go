package service

import (
	"context"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

const (
	GroupAvailabilityProbeStatusSuccess = "success"
	GroupAvailabilityProbeStatusFailed  = "failed"
)

// ErrGroupAvailabilityProbeLeaseLost 防止已过期的探测覆盖新一轮观测或清除其租约。
var ErrGroupAvailabilityProbeLeaseLost = infraerrors.Conflict("GROUP_AVAILABILITY_PROBE_LEASE_LOST", "This probe lease has expired; a newer probe may already be running")

// GroupAvailabilityProbeDueGroup 是等待主动探测的分组快照。
type GroupAvailabilityProbeDueGroup struct {
	GroupID int64
	Name    string
	// Platform 用于选择对应平台的账号调度器。
	Platform string
	Config   GroupAvailabilityProbeConfig
	// LeaseToken 标识本次领取，不能使用实例 ID 代替每轮唯一标识。
	LeaseToken string
}

// GroupAvailabilityProbeResult 是单次主动探测结果。
type GroupAvailabilityProbeResult struct {
	LeaseToken   string    `json:"-"`
	GroupID      int64     `json:"group_id"`
	AccountID    *int64    `json:"account_id,omitempty"`
	ModelID      string    `json:"model_id"`
	Protocol     string    `json:"protocol"`
	Status       string    `json:"status"`
	Success      bool      `json:"success"`
	LatencyMs    int64     `json:"latency_ms"`
	ErrorMessage string    `json:"error_message,omitempty"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
}

// GroupAvailabilityProbeLeaseDuration 按实际执行次数预留租约，崩溃后不必等待全局最大重试窗口。
func GroupAvailabilityProbeLeaseDuration(cfg GroupAvailabilityProbeConfig, manual bool) time.Duration {
	normalized, err := normalizeGroupAvailabilityProbeConfig(cfg)
	if err != nil || !normalized.Enabled {
		return time.Minute
	}
	attempts := 1
	if !manual {
		attempts += *normalized.MaxRetries
	}
	// 一分钟余量覆盖领取事务、账号选择结束后的收尾和独立结果保存。
	return time.Duration(attempts*normalized.TimeoutSeconds)*time.Second + time.Minute
}

// GroupAvailabilityBucket 是模型广场条形组件的时间桶聚合。
type GroupAvailabilityBucket struct {
	Date             string
	SuccessCount     int64
	TotalCount       int64
	AvailabilityRate *float64
}

// GroupAvailabilitySummary 是分组主动可用性摘要。
type GroupAvailabilitySummary struct {
	WindowDays       int
	BucketMinutes    int
	SuccessCount     int64
	TotalCount       int64
	AvailabilityRate *float64
	LastStatus       string
	LastCheckedAt    *time.Time
	Days             []GroupAvailabilityBucket
}

// GroupAvailabilityProbeRepository 定义分组主动可用性探测的数据访问接口。
type GroupAvailabilityProbeRepository interface {
	ClaimDue(ctx context.Context, now time.Time, lockUntil time.Time, lockedBy string, limit int) ([]GroupAvailabilityProbeDueGroup, error)
	// ClaimGroup 在同一事务中保存探测配置并领取租约，忽略下次定时时间。
	ClaimGroup(ctx context.Context, groupID int64, config GroupAvailabilityProbeConfig, now time.Time, lockUntil time.Time, lockedBy string) (*GroupAvailabilityProbeDueGroup, error)
	SaveResultAndScheduleNext(ctx context.Context, result *GroupAvailabilityProbeResult, nextRunAt time.Time) error
	GetSummaryByGroupIDs(ctx context.Context, groupIDs []int64, days int, bucketMinutes int, timezone string, now time.Time) (map[int64]*GroupAvailabilitySummary, error)
	CleanupOldResults(ctx context.Context, before time.Time) error
}
