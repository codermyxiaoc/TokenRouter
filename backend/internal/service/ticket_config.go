package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

const (
	SettingKeyTicketEnabled             = "ticket_enabled"
	SettingKeyTicketMaxOpenTickets      = "ticket_max_open_tickets"
	SettingKeyTicketMaxAttachments      = "ticket_max_attachments"
	SettingKeyTicketMaxAttachmentSizeMB = "ticket_max_attachment_size_mb"
	SettingKeyTicketNotifyOnStaffReply  = "ticket_notify_on_staff_reply"
	SettingKeyTicketAutoExpireHours     = "ticket_auto_expire_hours"
)

// TicketConfig 是新建、回复、上传和过期任务共用的工单运行时配置。
// @project-doc docs/domains/support_tickets.md#ticket_configuration
type TicketConfig struct {
	Enabled             bool `json:"enabled"`
	MaxOpenTickets      int  `json:"max_open_tickets"`
	MaxAttachments      int  `json:"max_attachments"`
	MaxAttachmentSizeMB int  `json:"max_attachment_size_mb"`
	NotifyOnStaffReply  bool `json:"notify_on_staff_reply"`
	AutoExpireHours     int  `json:"auto_expire_hours"`
}

// UpdateTicketConfigInput 保留省略字段，显式 false 和 0 按原值保存。
type UpdateTicketConfigInput struct {
	Enabled             *bool `json:"enabled"`
	MaxOpenTickets      *int  `json:"max_open_tickets"`
	MaxAttachments      *int  `json:"max_attachments"`
	MaxAttachmentSizeMB *int  `json:"max_attachment_size_mb"`
	NotifyOnStaffReply  *bool `json:"notify_on_staff_reply"`
	AutoExpireHours     *int  `json:"auto_expire_hours"`
}

// TicketConfigService 从 settings 表读取配置，不用进程缓存放宽创建或上传限制。
type TicketConfigService struct {
	settingRepo SettingRepository
	onUpdate    func()
}

// NewTicketConfigService 创建工单配置服务。
func NewTicketConfigService(settingRepo SettingRepository) *TicketConfigService {
	return &TicketConfigService{settingRepo: settingRepo}
}

func defaultTicketConfig() *TicketConfig {
	return &TicketConfig{
		Enabled: true, MaxOpenTickets: 3, MaxAttachments: 5,
		MaxAttachmentSizeMB: 10, NotifyOnStaffReply: false, AutoExpireHours: 72,
	}
}

// Get 仅对缺失或损坏的字段使用默认值；数据库故障必须阻止调用方继续放行。
func (s *TicketConfigService) Get(ctx context.Context) (*TicketConfig, error) {
	if s == nil || s.settingRepo == nil {
		return nil, fmt.Errorf("工单配置服务不可用")
	}
	values, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyTicketEnabled, SettingKeyTicketMaxOpenTickets, SettingKeyTicketMaxAttachments,
		SettingKeyTicketMaxAttachmentSizeMB, SettingKeyTicketNotifyOnStaffReply, SettingKeyTicketAutoExpireHours,
	})
	if err != nil {
		return nil, fmt.Errorf("读取工单配置失败: %w", err)
	}
	cfg := defaultTicketConfig()
	cfg.Enabled = parseTicketConfigBool(values[SettingKeyTicketEnabled], cfg.Enabled)
	cfg.MaxOpenTickets = parseTicketConfigInt(values[SettingKeyTicketMaxOpenTickets], 1, 100, cfg.MaxOpenTickets)
	cfg.MaxAttachments = parseTicketConfigInt(values[SettingKeyTicketMaxAttachments], 0, 10, cfg.MaxAttachments)
	cfg.MaxAttachmentSizeMB = parseTicketConfigInt(values[SettingKeyTicketMaxAttachmentSizeMB], 1, 20, cfg.MaxAttachmentSizeMB)
	cfg.NotifyOnStaffReply = parseTicketConfigBool(values[SettingKeyTicketNotifyOnStaffReply], cfg.NotifyOnStaffReply)
	cfg.AutoExpireHours = parseTicketConfigInt(values[SettingKeyTicketAutoExpireHours], 0, 8760, cfg.AutoExpireHours)
	return cfg, nil
}

// Update 只原子写入本次提供的键，避免不同实例同时更新不同字段时相互覆盖。
func (s *TicketConfigService) Update(ctx context.Context, input *UpdateTicketConfigInput) (*TicketConfig, error) {
	cfg, err := s.Get(ctx)
	if err != nil {
		return nil, err
	}
	if input == nil {
		return cfg, nil
	}
	updates := make(map[string]string, 6)
	for _, field := range []struct {
		key      string
		value    *int
		target   *int
		min, max int
	}{
		{SettingKeyTicketMaxOpenTickets, input.MaxOpenTickets, &cfg.MaxOpenTickets, 1, 100},
		{SettingKeyTicketMaxAttachments, input.MaxAttachments, &cfg.MaxAttachments, 0, 10},
		{SettingKeyTicketMaxAttachmentSizeMB, input.MaxAttachmentSizeMB, &cfg.MaxAttachmentSizeMB, 1, 20},
		{SettingKeyTicketAutoExpireHours, input.AutoExpireHours, &cfg.AutoExpireHours, 0, 8760},
	} {
		if field.value == nil {
			continue
		}
		if *field.value < field.min || *field.value > field.max {
			return nil, infraerrors.BadRequest("TICKET_CONFIG_INVALID", fmt.Sprintf("%s 必须在 %d 到 %d 之间", field.key, field.min, field.max))
		}
		*field.target = *field.value
		updates[field.key] = strconv.Itoa(*field.value)
	}
	if input.Enabled != nil {
		cfg.Enabled = *input.Enabled
		updates[SettingKeyTicketEnabled] = strconv.FormatBool(*input.Enabled)
	}
	if input.NotifyOnStaffReply != nil {
		cfg.NotifyOnStaffReply = *input.NotifyOnStaffReply
		updates[SettingKeyTicketNotifyOnStaffReply] = strconv.FormatBool(*input.NotifyOnStaffReply)
	}
	if len(updates) > 0 {
		if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
			return nil, fmt.Errorf("保存工单配置失败: %w", err)
		}
		// 仅持久化成功后刷新页面注入缓存，避免关闭后仍展示旧工单入口。
		if s.onUpdate != nil {
			s.onUpdate()
		}
	}
	return cfg, nil
}

func parseTicketConfigInt(raw string, min, max, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < min || value > max {
		return fallback
	}
	return value
}

func parseTicketConfigBool(raw string, fallback bool) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return value
}
