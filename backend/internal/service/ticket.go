package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

const (
	// TicketMaxTitleLength 限制新建工单标题的字符数，不截断已有工单内容。
	TicketMaxTitleLength = 50

	TicketStatusPending     = "pending"
	TicketStatusWaitingUser = "waiting_user"
	TicketStatusCompleted   = "completed"
	TicketStatusCancelled   = "cancelled"
	TicketStatusExpired     = "expired"
)

var (
	ErrTicketNotFound            = infraerrors.NotFound("TICKET_NOT_FOUND", "工单不存在")
	ErrTicketClosed              = infraerrors.Conflict("TICKET_CLOSED", "该工单已结束，无法继续处理")
	ErrTicketOpenLimit           = infraerrors.Conflict("TICKET_OPEN_LIMIT", "您还有工单未处理，请先完成或撤销已有工单")
	ErrTicketOrderInvalid        = infraerrors.BadRequest("TICKET_ORDER_INVALID", "请选择当前用户的一笔有效订单")
	ErrTicketInputInvalid        = infraerrors.BadRequest("TICKET_INPUT_INVALID", "工单输入无效")
	ErrTicketTitleTooLong        = infraerrors.BadRequest("TICKET_TITLE_TOO_LONG", "工单标题最多 50 个字符")
	ErrTicketDisabled            = infraerrors.Forbidden("TICKET_DISABLED", "工单功能暂未开放")
	ErrTicketIdempotencyConflict = infraerrors.Conflict("TICKET_IDEMPOTENCY_CONFLICT", "此请求标识已经用于其他工单内容")
)

// TicketActor 只由认证中间件提供，不能从请求正文接受管理员身份。
type TicketActor struct {
	UserID  int64
	IsAdmin bool
}

type TicketAttachmentUpload struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Data        []byte `json:"-"`
}

type TicketAttachment struct {
	ID          int64     `json:"id"`
	MessageID   int64     `json:"message_id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	CreatedAt   time.Time `json:"created_at"`
	Data        []byte    `json:"-"`
}

type TicketMessage struct {
	ID          int64              `json:"id"`
	TicketID    int64              `json:"ticket_id"`
	UserID      int64              `json:"user_id"`
	SenderName  string             `json:"sender_name"`
	IsStaff     bool               `json:"is_staff"`
	Content     string             `json:"content"`
	CreatedAt   time.Time          `json:"created_at"`
	Attachments []TicketAttachment `json:"attachments"`
}

// TicketOrder 仅投影展示所需字段，支付凭据和提供商快照不得进入响应。
type TicketOrder struct {
	ID         int64   `json:"id"`
	OutTradeNo string  `json:"out_trade_no"`
	Amount     float64 `json:"amount"`
	PayAmount  float64 `json:"pay_amount"`
	Currency   string  `json:"currency"`
	Status     string  `json:"status"`
}

type Ticket struct {
	ID               int64        `json:"id"`
	UserID           int64        `json:"user_id"`
	UserEmail        string       `json:"user_email"`
	UserName         string       `json:"user_name"`
	Type             string       `json:"type"`
	Title            string       `json:"title"`
	Content          string       `json:"content"`
	Priority         string       `json:"priority"`
	Status           string       `json:"status"`
	OrderID          *int64       `json:"order_id"`
	Order            *TicketOrder `json:"order"`
	CreatedAt        time.Time    `json:"created_at"`
	UpdatedAt        time.Time    `json:"updated_at"`
	LastStaffReplyAt *time.Time   `json:"last_staff_reply_at"`
	ClosedAt         *time.Time   `json:"closed_at"`
	// 结束操作仅记录认证身份快照；历史未记录的操作保持为空，不从消息推断。
	ClosedBy       *int64          `json:"closed_by"`
	ClosedByRole   string          `json:"closed_by_role"`
	Messages       []TicketMessage `json:"messages,omitempty"`
	ReplyCreated   bool            `json:"-"`
	ReplyMessageID int64           `json:"-"`
	// 仅首次成功提交关单的请求触发通知，终态重试不能重复发送。
	ClosureCreated bool `json:"-"`
}

type CreateTicketInput struct {
	Type           string                   `json:"type"`
	Title          string                   `json:"title"`
	Content        string                   `json:"content"`
	Priority       string                   `json:"priority"`
	OrderID        *int64                   `json:"order_id"`
	Attachments    []TicketAttachmentUpload `json:"-"`
	IdempotencyKey string                   `json:"-"`
}

type ReplyTicketInput struct {
	Content        string                   `json:"content"`
	Attachments    []TicketAttachmentUpload `json:"-"`
	IdempotencyKey string                   `json:"-"`
}

type UpdateTicketInput struct {
	Priority *string `json:"priority"`
}

type TicketListFilter struct {
	Page, PageSize                 int
	Status, Type, Priority, Search string
}
type TicketListResult struct {
	Items    []Ticket `json:"items"`
	Total    int64    `json:"total"`
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
	Pages    int      `json:"pages"`
}

type TicketConfigProvider interface {
	Get(context.Context) (*TicketConfig, error)
}

// TicketRepository 将权限检查与状态判断放进同一事务，避免检查后写入的竞争窗口。
type TicketRepository interface {
	List(context.Context, TicketActor, TicketListFilter) (*TicketListResult, error)
	Get(context.Context, TicketActor, int64) (*Ticket, error)
	Create(context.Context, TicketActor, *CreateTicketInput, int, string) (*Ticket, error)
	Reply(context.Context, TicketActor, int64, *ReplyTicketInput, string) (*Ticket, error)
	Close(context.Context, TicketActor, int64, string) (*Ticket, error)
	Update(context.Context, TicketActor, int64, *UpdateTicketInput) (*Ticket, error)
	Attachment(context.Context, TicketActor, int64, int64) (*TicketAttachment, error)
	Expire(context.Context, time.Time) (int64, error)
}

// TicketService 统一拥有用户与后台工单的输入规则及配置约束。
// @project-doc docs/domains/support_tickets.md#ticket_lifecycle
type TicketService struct {
	repo   TicketRepository
	config TicketConfigProvider
}

func NewTicketService(repo TicketRepository, config TicketConfigProvider) *TicketService {
	return &TicketService{repo: repo, config: config}
}

func (s *TicketService) Config(ctx context.Context) (*TicketConfig, error) {
	if s.config == nil {
		return nil, fmt.Errorf("工单配置服务不可用")
	}
	cfg, err := s.config.Get(ctx)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, fmt.Errorf("工单配置为空")
	}
	return cfg, nil
}

// enabledConfig 在所有业务入口读取同一总开关；故障时不访问历史数据或继续写入。
func (s *TicketService) enabledConfig(ctx context.Context) (*TicketConfig, error) {
	cfg, err := s.Config(ctx)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, ErrTicketDisabled
	}
	return cfg, nil
}

func (s *TicketService) List(ctx context.Context, actor TicketActor, filter TicketListFilter) (*TicketListResult, error) {
	if actor.UserID <= 0 {
		return nil, ErrTicketNotFound
	}
	if _, err := s.enabledConfig(ctx); err != nil {
		return nil, err
	}
	filter.Type = normalizeTicketType(filter.Type)
	filter.Priority = normalizeTicketPriority(filter.Priority)
	if filter.Status != "" && !validTicketStatus(filter.Status) || filter.Type != "" && !validTicketType(filter.Type) || filter.Priority != "" && !validTicketPriority(filter.Priority) {
		return nil, ErrTicketInputInvalid
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
	filter.Search = strings.TrimSpace(filter.Search)
	if utf8.RuneCountInString(filter.Search) > 200 || filter.Page > 1000000 {
		return nil, ErrTicketInputInvalid
	}
	return s.repo.List(ctx, actor, filter)
}

func (s *TicketService) Get(ctx context.Context, actor TicketActor, id int64) (*Ticket, error) {
	if actor.UserID <= 0 || id <= 0 {
		return nil, ErrTicketNotFound
	}
	if _, err := s.enabledConfig(ctx); err != nil {
		return nil, err
	}
	return s.repo.Get(ctx, actor, id)
}

func (s *TicketService) Create(ctx context.Context, actor TicketActor, input *CreateTicketInput) (*Ticket, error) {
	if actor.UserID <= 0 {
		return nil, ErrTicketNotFound
	}
	cfg, err := s.enabledConfig(ctx)
	if err != nil {
		return nil, err
	}
	if input == nil {
		return nil, ErrTicketInputInvalid
	}
	in := *input
	// 兼容升级前的表单；持久化统一使用咨询类型，旧请求摘要仍保持可重试。
	requestedType := in.Type
	in.Type = normalizeTicketType(in.Type)
	in.Title = strings.TrimSpace(in.Title)
	in.Content = strings.TrimSpace(in.Content)
	if in.Priority == "" {
		in.Priority = "normal"
	}
	// 紧急归入高优先级，但保留旧请求摘要；省略值沿用既有普通优先级摘要。
	requestedPriority := in.Priority
	in.Priority = normalizeTicketPriority(in.Priority)
	if !validTicketType(in.Type) || !validTicketPriority(in.Priority) || in.Title == "" || in.Content == "" || utf8.RuneCountInString(in.Content) > 20000 || !validTicketRequestKey(in.IdempotencyKey) {
		return nil, ErrTicketInputInvalid
	}
	// 按 Unicode 字符而非 UTF-8 字节计数，与前端计数器保持一致。
	if utf8.RuneCountInString(in.Title) > TicketMaxTitleLength {
		return nil, ErrTicketTitleTooLong
	}
	if in.OrderID != nil && (in.Type != "financial" || *in.OrderID <= 0) {
		return nil, ErrTicketOrderInvalid
	}
	in.Attachments, err = ValidateTicketAttachments(in.Attachments, cfg)
	if err != nil {
		return nil, err
	}
	hashInput := in
	hashInput.Type = requestedType
	hashInput.Priority = requestedPriority
	return s.repo.Create(ctx, actor, &in, cfg.MaxOpenTickets, ticketRequestHash(hashInput, in.Attachments))
}

func (s *TicketService) Reply(ctx context.Context, actor TicketActor, id int64, input *ReplyTicketInput) (*Ticket, error) {
	if actor.UserID <= 0 || id <= 0 {
		return nil, ErrTicketNotFound
	}
	cfg, err := s.enabledConfig(ctx)
	if err != nil {
		return nil, err
	}
	if input == nil {
		return nil, ErrTicketInputInvalid
	}
	in := *input
	in.Content = strings.TrimSpace(in.Content)
	if (in.Content == "" && len(in.Attachments) == 0) || utf8.RuneCountInString(in.Content) > 20000 || !validTicketRequestKey(in.IdempotencyKey) {
		return nil, ErrTicketInputInvalid
	}
	in.Attachments, err = ValidateTicketAttachments(in.Attachments, cfg)
	if err != nil {
		return nil, err
	}
	return s.repo.Reply(ctx, actor, id, &in, ticketRequestHash(in, in.Attachments))
}

func (s *TicketService) Close(ctx context.Context, actor TicketActor, id int64, status string) (*Ticket, error) {
	if actor.UserID <= 0 || id <= 0 {
		return nil, ErrTicketNotFound
	}
	if _, err := s.enabledConfig(ctx); err != nil {
		return nil, err
	}
	if status != TicketStatusCompleted && status != TicketStatusCancelled {
		return nil, ErrTicketInputInvalid
	}
	return s.repo.Close(ctx, actor, id, status)
}

func (s *TicketService) Update(ctx context.Context, actor TicketActor, id int64, in *UpdateTicketInput) (*Ticket, error) {
	if !actor.IsAdmin || actor.UserID <= 0 || id <= 0 {
		return nil, ErrTicketNotFound
	}
	if _, err := s.enabledConfig(ctx); err != nil {
		return nil, err
	}
	if in == nil || in.Priority == nil {
		return nil, ErrTicketInputInvalid
	}
	priority := normalizeTicketPriority(*in.Priority)
	if !validTicketPriority(priority) {
		return nil, ErrTicketInputInvalid
	}
	return s.repo.Update(ctx, actor, id, &UpdateTicketInput{Priority: &priority})
}

func (s *TicketService) Attachment(ctx context.Context, actor TicketActor, ticketID, attachmentID int64) (*TicketAttachment, error) {
	if actor.UserID <= 0 || ticketID <= 0 || attachmentID <= 0 {
		return nil, ErrTicketNotFound
	}
	if _, err := s.enabledConfig(ctx); err != nil {
		return nil, err
	}
	return s.repo.Attachment(ctx, actor, ticketID, attachmentID)
}

func (s *TicketService) Expire(ctx context.Context) (int64, error) {
	cfg, err := s.Config(ctx)
	if err != nil {
		return 0, err
	}
	if !cfg.Enabled || cfg.AutoExpireHours <= 0 {
		return 0, nil
	}
	return s.repo.Expire(ctx, time.Now().Add(-time.Duration(cfg.AutoExpireHours)*time.Hour))
}

func validTicketType(v string) bool {
	switch v {
	case "consultation", "financial", "technical":
		return true
	}
	return false
}

// normalizeTicketType 将历史分类归入咨询，不接受任意未知分类。
func normalizeTicketType(v string) string {
	switch v {
	case "presales", "aftersales", "other":
		return "consultation"
	default:
		return v
	}
}
func validTicketPriority(v string) bool {
	switch v {
	case "low", "normal", "high":
		return true
	}
	return false
}

// normalizeTicketPriority 兼容升级前的紧急优先级，正式存储只保留低、中、高。
func normalizeTicketPriority(v string) string {
	if v == "urgent" {
		return "high"
	}
	return v
}
func validTicketStatus(v string) bool {
	switch v {
	case TicketStatusPending, TicketStatusWaitingUser, TicketStatusCompleted, TicketStatusCancelled, TicketStatusExpired:
		return true
	}
	return false
}
func IsTicketOpen(status string) bool {
	return status == TicketStatusPending || status == TicketStatusWaitingUser
}
func validTicketRequestKey(key string) bool {
	if len(key) > 128 {
		return false
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

// 摘要包含附件内容和顺序，阻止同一幂等键悄悄指向不同的附件或正文。
func ticketRequestHash(input any, files []TicketAttachmentUpload) string {
	h := sha256.New()
	data, _ := json.Marshal(input)
	_, _ = h.Write(data)
	for _, file := range files {
		meta, _ := json.Marshal([]string{file.Name, file.ContentType})
		_, _ = h.Write(meta)
		sum := sha256.Sum256(file.Data)
		_, _ = h.Write(sum[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}
