package service

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/manxue"
)

// IntelligenceConfig 保存分组检测配置，只引用站内密钥，不另存密钥明文。
type IntelligenceConfig struct {
	ID               int64            `json:"id"`
	GroupID          int64            `json:"group_id"`
	GroupName        string           `json:"group_name"`
	Model            string           `json:"model"`
	Benchmark        string           `json:"benchmark"`
	BaseURL          string           `json:"base_url"`
	APIKeyID         int64            `json:"-"`
	APIKeyConfigured bool             `json:"api_key_configured"`
	Protocol         string           `json:"protocol"`
	ReasoningEffort  string           `json:"reasoning_effort"`
	ServiceTier      string           `json:"service_tier"`
	Enabled          bool             `json:"enabled"`
	ScheduleEnabled  bool             `json:"schedule_enabled"`
	IntervalMinutes  int              `json:"interval_minutes"`
	NextRunAt        *time.Time       `json:"next_run_at"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
	LatestRun        *IntelligenceRun `json:"latest_run,omitempty"`
}

// IntelligenceConfigInput 的 APIKey 为仅写字段；空字符串保留已有引用。
type IntelligenceConfigInput struct {
	GroupID         int64  `json:"group_id"`
	Model           string `json:"model"`
	Benchmark       string `json:"benchmark"`
	BaseURL         string `json:"base_url"`
	APIKey          string `json:"api_key"`
	Protocol        string `json:"protocol"`
	ReasoningEffort string `json:"reasoning_effort"`
	ServiceTier     string `json:"service_tier"`
	Enabled         bool   `json:"enabled"`
	ScheduleEnabled bool   `json:"schedule_enabled"`
	IntervalMinutes int    `json:"interval_minutes"`
}

// 调试格式化也不暴露管理员提交的凭据。
func (IntelligenceConfigInput) String() string {
	return "IntelligenceConfigInput{credentials:redacted}"
}
func (v IntelligenceConfigInput) GoString() string { return v.String() }

// IntelligenceRun 的远端编号和租约永不进入用户或管理员响应。
type IntelligenceRun struct {
	ID               string     `json:"id"`
	ConfigID         int64      `json:"config_id"`
	GroupID          int64      `json:"group_id"`
	Model            string     `json:"model"`
	Benchmark        string     `json:"benchmark"`
	Status           string     `json:"status"`
	Verdict          string     `json:"verdict"`
	Phase            string     `json:"phase"`
	Question         string     `json:"question,omitempty"`
	Answer           string     `json:"answer,omitempty"`
	HTML             string     `json:"html,omitempty"`
	InputTokens      *int64     `json:"input_tokens,omitempty"`
	OutputTokens     *int64     `json:"output_tokens,omitempty"`
	ReasoningTokens  *int64     `json:"reasoning_tokens,omitempty"`
	DurationMS       *int64     `json:"duration_ms,omitempty"`
	AssessmentReason string     `json:"assessment_reason,omitempty"`
	ErrorMessage     string     `json:"error_message,omitempty"`
	HasDetail        bool       `json:"has_detail"`
	HasArtifact      bool       `json:"has_artifact"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
	RemoteID         string     `json:"-"`
	LeaseToken       string     `json:"-"`
}

// IntelligenceTest 是用户只读投影，不包含检测站点、协议或执行密钥。
type IntelligenceTest struct {
	ID        int64             `json:"id"`
	GroupID   int64             `json:"group_id"`
	GroupName string            `json:"group_name"`
	Model     string            `json:"model"`
	Benchmark string            `json:"benchmark"`
	Runs      []IntelligenceRun `json:"runs"`
	Artifacts []IntelligenceRun `json:"artifacts"`
}

// IntelligenceRepository 以租约比较交换保护后台跨实例执行和结果写回。
type IntelligenceRepository interface {
	ListConfigs(context.Context) ([]IntelligenceConfig, error)
	GetConfig(context.Context, int64) (*IntelligenceConfig, error)
	SaveConfig(context.Context, *IntelligenceConfig) error
	DeleteConfig(context.Context, int64) error
	CreateRun(context.Context, int64, time.Time) (*IntelligenceRun, error)
	ScheduleDue(context.Context, time.Time, int) error
	ClaimRun(context.Context, time.Time, time.Time, string) (*IntelligenceRun, error)
	SaveRun(context.Context, *IntelligenceRun, time.Time, bool) (bool, error)
	GetRun(context.Context, string) (*IntelligenceRun, error)
	ListRuns(context.Context, int64) ([]IntelligenceRun, error)
}

type IntelligenceGroupReader interface {
	GetAvailableGroups(context.Context, int64) ([]Group, error)
}

type IntelligenceClient interface {
	Create(context.Context, manxue.CreateRequest, string) (*manxue.TestResult, error)
	Get(context.Context, string) (*manxue.TestResult, error)
}
