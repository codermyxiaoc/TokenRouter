package service

import (
	"context"
	"time"
)

// UsageAnalyticsAggregationState 记录多维用量聚合的实时水位和历史覆盖进度。
type UsageAnalyticsAggregationState struct {
	LiveWatermark        time.Time
	CoverageStart        *time.Time
	BackfillCursor       *time.Time
	SourceOldestAt       *time.Time
	ManualBackfillStart  *time.Time
	ManualBackfillCursor *time.Time
	Phase                string
	LastRunAt            *time.Time
	LastSuccessAt        *time.Time
	LastErrorAt          *time.Time
	LastError            string
	LastDurationMS       int64
}

// UsageAnalyticsAggregationRepository 定义多维用量预聚合所需的仓储能力。
type UsageAnalyticsAggregationRepository interface {
	AggregateUsageAnalyticsRange(ctx context.Context, start, end time.Time) error
	AggregateUsageAnalyticsHourlyRange(ctx context.Context, start, end time.Time) error
	RebuildUsageAnalyticsDailyRange(ctx context.Context, start, end time.Time) error
	RecomputeUsageAnalyticsRange(ctx context.Context, start, end time.Time) error
	GetUsageAnalyticsAggregationState(ctx context.Context) (*UsageAnalyticsAggregationState, error)
	SaveUsageAnalyticsAggregationState(ctx context.Context, state *UsageAnalyticsAggregationState) error
	GetOldestUsageLogTime(ctx context.Context) (*time.Time, error)
	CleanupUsageAnalytics(ctx context.Context, hourlyCutoff, dailyCutoff time.Time) error
}

// PreAggregationRuntimeStatus 是设置接口返回的任务运行状态。
type PreAggregationRuntimeStatus struct {
	Phase          string     `json:"phase"`
	LiveWatermark  *time.Time `json:"live_watermark,omitempty"`
	CoverageStart  *time.Time `json:"coverage_start,omitempty"`
	SourceOldestAt *time.Time `json:"source_oldest_at,omitempty"`
	LagSeconds     int64      `json:"lag_seconds"`
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	LastSuccessAt  *time.Time `json:"last_success_at,omitempty"`
	LastErrorAt    *time.Time `json:"last_error_at,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	LastDurationMS int64      `json:"last_duration_ms"`
}
