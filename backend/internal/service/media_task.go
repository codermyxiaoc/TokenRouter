package service

import (
	"context"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

var (
	ErrMediaTaskNotFound = infraerrors.NotFound("MEDIA_TASK_NOT_FOUND", "任务记录不存在")
	ErrMediaTaskInvalid  = infraerrors.BadRequest("MEDIA_TASK_INVALID", "任务记录参数无效")
)

// MediaTaskObservation 是图片与视频共享的安全元数据，不接受提示词、凭据或原始响应。
// @project-doc docs/domains/media_tasks.md#media_task_observation
type MediaTaskObservation struct {
	Source         string
	TaskID         string
	MediaType      string
	Platform       string
	Model          string
	Status         string
	UpstreamStatus string
	UserID         int64
	APIKeyID       int64
	GroupID        *int64
	AccountID      *int64
	HTTPStatus     int
	ErrorMessage   string
	RequestID      string
	CreatedAt      time.Time
	CompletedAt    *time.Time
	ExpiresAt      *time.Time
}

type MediaTaskObserver interface {
	ObserveMediaTask(context.Context, MediaTaskObservation) error
}

// MediaTaskUser 只包含管理员识别任务归属所需的资料，不复用带余额和认证信息的完整用户结构。
type MediaTaskUser struct {
	ID        int64      `json:"id"`
	Email     string     `json:"email"`
	Username  string     `json:"username"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

// MediaTask 只投影展示字段；费用从既有使用记录关联，不在列表流程执行结算。
type MediaTask struct {
	ID             int64          `json:"id"`
	Source         string         `json:"source"`
	TaskID         string         `json:"task_id"`
	MediaType      string         `json:"media_type"`
	Platform       string         `json:"platform"`
	Model          string         `json:"model"`
	Status         string         `json:"status"`
	UpstreamStatus string         `json:"upstream_status"`
	UserID         int64          `json:"user_id"`
	User           *MediaTaskUser `json:"user,omitempty"`
	APIKeyID       int64          `json:"api_key_id"`
	GroupID        *int64         `json:"group_id"`
	GroupName      *string        `json:"group_name"`
	AccountID      *int64         `json:"account_id"`
	HTTPStatus     int            `json:"http_status"`
	ErrorMessage   string         `json:"error_message"`
	RequestID      string         `json:"request_id"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	CompletedAt    *time.Time     `json:"completed_at"`
	ExpiresAt      *time.Time     `json:"expires_at"`
	ActualCost     *float64       `json:"actual_cost"`
	BillingMode    *string        `json:"billing_mode"`
}

// MediaTaskActor 只能由认证层构建；管理员从个人入口访问时仍限制为本人。
type MediaTaskActor struct {
	UserID  int64
	IsAdmin bool
}

type MediaTaskFilter struct {
	Page, PageSize                   int
	MediaType, Status, Source, Model string
	UserID                           int64
}

type MediaTaskList struct {
	Items    []MediaTask `json:"items"`
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
	Pages    int         `json:"pages"`
}

type MediaTaskRepository interface {
	Observe(context.Context, MediaTaskObservation) error
	List(context.Context, MediaTaskActor, MediaTaskFilter) (*MediaTaskList, error)
	Get(context.Context, MediaTaskActor, int64) (*MediaTask, error)
}

type MediaTaskService struct{ repo MediaTaskRepository }

func NewMediaTaskService(repo MediaTaskRepository) *MediaTaskService {
	return &MediaTaskService{repo: repo}
}

func mediaTaskStatusValid(value string) bool {
	switch value {
	case "queued", "processing", "completed", "failed", "cancelled", "expired":
		return true
	}
	return false
}

func mediaTaskSourceValid(value string) bool {
	return value == "async_image" || value == "grok_video" || value == "seedance_video"
}

// ObserveMediaTask 统一限制持久化字段并脱敏，防止上游错误混入签名地址或密钥。
func (s *MediaTaskService) ObserveMediaTask(ctx context.Context, observation MediaTaskObservation) error {
	if s == nil || s.repo == nil {
		return ErrMediaTaskInvalid
	}
	observation.TaskID = strings.TrimSpace(observation.TaskID)
	if !mediaTaskSourceValid(observation.Source) || !mediaTaskStatusValid(observation.Status) ||
		observation.UserID <= 0 || observation.APIKeyID <= 0 || observation.TaskID == "" ||
		len(observation.TaskID) > 255 || len(observation.Model) > 255 || len(observation.Platform) > 32 ||
		len(observation.UpstreamStatus) > 64 || len(observation.RequestID) > 255 ||
		observation.HTTPStatus < 0 || observation.HTTPStatus > 599 ||
		(observation.MediaType != "image" && observation.MediaType != "video") {
		return ErrMediaTaskInvalid
	}
	if (observation.Source == "async_image") != (observation.MediaType == "image") {
		return ErrMediaTaskInvalid
	}
	if observation.CreatedAt.IsZero() {
		observation.CreatedAt = time.Now().UTC()
	}
	if observation.Status != "queued" && observation.Status != "processing" && observation.CompletedAt == nil {
		now := time.Now().UTC()
		observation.CompletedAt = &now
	}
	// 先脱敏再按字符数限制；不保留超长原文供用户详情意外读取。
	message := []rune(redactContentModerationSecrets(observation.ErrorMessage))
	if len(message) > 500 {
		message = message[:500]
	}
	observation.ErrorMessage = string(message)
	return s.repo.Observe(ctx, observation)
}

func (s *MediaTaskService) List(ctx context.Context, actor MediaTaskActor, filter MediaTaskFilter) (*MediaTaskList, error) {
	if actor.UserID <= 0 || s == nil || s.repo == nil {
		return nil, ErrMediaTaskInvalid
	}
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize < 1 {
		filter.PageSize = 20
	}
	if filter.PageSize > 100 {
		filter.PageSize = 100
	}
	filter.Model = strings.TrimSpace(filter.Model)
	if filter.Page > 1000000 || len(filter.Model) > 255 || filter.UserID < 0 ||
		(filter.MediaType != "" && filter.MediaType != "image" && filter.MediaType != "video") ||
		(filter.Status != "" && !mediaTaskStatusValid(filter.Status)) ||
		(filter.Source != "" && !mediaTaskSourceValid(filter.Source)) {
		return nil, ErrMediaTaskInvalid
	}
	if !actor.IsAdmin {
		filter.UserID = actor.UserID
	}
	result, err := s.repo.List(ctx, actor, filter)
	if err != nil {
		return nil, err
	}
	for i := range result.Items {
		projectMediaTaskForActor(&result.Items[i], actor)
	}
	return result, nil
}

func (s *MediaTaskService) Get(ctx context.Context, actor MediaTaskActor, id int64) (*MediaTask, error) {
	if actor.UserID <= 0 || id <= 0 || s == nil || s.repo == nil {
		return nil, ErrMediaTaskNotFound
	}
	result, err := s.repo.Get(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	projectMediaTaskForActor(result, actor)
	return result, nil
}

func projectMediaTaskForActor(task *MediaTask, actor MediaTaskActor) {
	if !actor.IsAdmin {
		// 账号是站点内部调度资源，个人任务列表不公开其身份。
		task.AccountID = nil
		// 用户身份展示仅属于管理端；个人入口即使由管理员访问也不返回此对象。
		task.User = nil
	}
}
