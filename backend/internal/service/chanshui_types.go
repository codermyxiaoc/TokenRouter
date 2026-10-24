package service

import (
	"context"
	"encoding/json"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

var (
	ErrChanshuiNotFound = infraerrors.NotFound("CHANSHUI_NOT_FOUND", "掺水检测配置或记录不存在")
	ErrChanshuiDisabled = infraerrors.Forbidden("CHANSHUI_DISABLED", "掺水检测未开启或账号不可用")
	ErrChanshuiBusy     = infraerrors.Conflict("CHANSHUI_BUSY", "当前配置正在检测，请等待本次检测结束")
)

// ChanshuiConfig 只绑定上游账号身份，不复制凭据，也不关联分组或渠道实体。
type ChanshuiConfig struct {
	ID                    int64         `json:"id"`
	AccountID             int64         `json:"account_id"`
	AccountName           string        `json:"account_name"`
	Model                 string        `json:"model"`
	Protocol              string        `json:"protocol"`
	Sections              []string      `json:"sections"`
	TimeoutSeconds        int           `json:"timeout_seconds"`
	Enabled               bool          `json:"enabled"`
	ScheduleEnabled       bool          `json:"schedule_enabled"`
	IntervalMinutes       int           `json:"interval_minutes"`
	DisableOnAdulteration bool          `json:"disable_on_adulteration"`
	AutoRecover           bool          `json:"auto_recover"`
	RecoveryPending       bool          `json:"recovery_pending"`
	MinimumScore          *float64      `json:"minimum_score"`
	NextRunAt             *time.Time    `json:"next_run_at"`
	CreatedAt             time.Time     `json:"created_at"`
	UpdatedAt             time.Time     `json:"updated_at"`
	LatestRun             *ChanshuiRun  `json:"latest_run,omitempty"`
	RecentRuns            []ChanshuiRun `json:"recent_runs"`
}

// ChanshuiConfigInput 的账号与模型身份创建后固定，执行时读取账号自己的凭据。
type ChanshuiConfigInput struct {
	AccountID             int64    `json:"account_id"`
	Model                 string   `json:"model"`
	Protocol              string   `json:"protocol"`
	Sections              []string `json:"sections"`
	TimeoutSeconds        int      `json:"timeout_seconds"`
	Enabled               bool     `json:"enabled"`
	ScheduleEnabled       bool     `json:"schedule_enabled"`
	IntervalMinutes       int      `json:"interval_minutes"`
	DisableOnAdulteration bool     `json:"disable_on_adulteration"`
	AutoRecover           bool     `json:"auto_recover"`
	MinimumScore          *float64 `json:"minimum_score"`
}

// ChanshuiRun 的远端身份、租约和配置指纹只供恢复使用，永不返回面板。
type ChanshuiRun struct {
	ID               string          `json:"id"`
	ConfigID         int64           `json:"config_id"`
	AccountID        int64           `json:"account_id"`
	Model            string          `json:"model"`
	Trigger          string          `json:"trigger,omitempty"`
	Status           string          `json:"status"`
	Verdict          string          `json:"verdict"`
	Score            *float64        `json:"score,omitempty"`
	ScoreComplete    bool            `json:"score_complete"`
	Summary          string          `json:"summary,omitempty"`
	Report           json.RawMessage `json:"report,omitempty"`
	ErrorMessage     string          `json:"error_message,omitempty"`
	ActionStatus     string          `json:"action_status"`
	ActionReason     string          `json:"action_reason,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	FinishedAt       *time.Time      `json:"finished_at,omitempty"`
	RemoteID         string          `json:"-"`
	RemoteExpiresAt  *time.Time      `json:"-"`
	LeaseToken       string          `json:"-"`
	AccountSnapshot  string          `json:"-"`
	AccountRevision  int64           `json:"-"`
	AccountUpdatedAt *time.Time      `json:"-"`
	ConfigUpdatedAt  time.Time       `json:"-"`
}

// ChanshuiDisableDecision 的账号版本须在比对持久身份指纹后读取，最终仍由数据库比较交换。
type ChanshuiDisableDecision struct {
	AccountUpdatedAt time.Time
	Reason           string
	Recover          bool
}

// ChanshuiRepository 以持久租约、全局提交限速和原子终态保证跨实例执行安全。
type ChanshuiRepository interface {
	GetAccountRevision(context.Context, int64) (int64, error)
	ListConfigs(context.Context) ([]ChanshuiConfig, error)
	GetConfig(context.Context, int64) (*ChanshuiConfig, error)
	SaveConfig(context.Context, *ChanshuiConfig) error
	DeleteConfig(context.Context, int64) error
	CreateRun(context.Context, int64, time.Time) (*ChanshuiRun, error)
	ScheduleDue(context.Context, time.Time, int) error
	ClaimRun(context.Context, time.Time, time.Time, string) (*ChanshuiRun, error)
	SaveRun(context.Context, *ChanshuiRun, time.Time, bool) (bool, error)
	AcquireSubmitSlot(context.Context, time.Time) (time.Time, error)
	FinalizeRun(context.Context, *ChanshuiRun, *ChanshuiDisableDecision) (bool, error)
	GetRun(context.Context, string) (*ChanshuiRun, error)
	ListRuns(context.Context, int64) ([]ChanshuiRun, error)
}
