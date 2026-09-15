package service

import (
	"context"
	"errors"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// ticketConfigRepoStub 仅模拟配置读写，保留实际服务的批量 PATCH 行为。
type ticketConfigRepoStub struct {
	SettingRepository
	values            map[string]string
	readErr, writeErr error
	writes            []map[string]string
}

func (s *ticketConfigRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	out := make(map[string]string)
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func (s *ticketConfigRepoStub) SetMultiple(_ context.Context, values map[string]string) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	if s.values == nil {
		s.values = make(map[string]string)
	}
	s.writes = append(s.writes, values)
	for key, value := range values {
		s.values[key] = value
	}
	return nil
}

func ticketConfigPtr[T any](value T) *T { return &value }

func TestTicketConfigDefaultsAndSafeParsing(t *testing.T) {
	t.Run("缺失使用默认值", func(t *testing.T) {
		cfg, err := NewTicketConfigService(&ticketConfigRepoStub{}).Get(context.Background())
		require.NoError(t, err)
		require.Equal(t, &TicketConfig{Enabled: true, MaxOpenTickets: 3, MaxAttachments: 5, MaxAttachmentSizeMB: 10, NotifyOnStaffReply: false, AutoExpireHours: 72}, cfg)
	})
	t.Run("坏值不能放宽上限", func(t *testing.T) {
		repo := &ticketConfigRepoStub{values: map[string]string{
			SettingKeyTicketEnabled: "invalid", SettingKeyTicketMaxOpenTickets: "0",
			SettingKeyTicketMaxAttachments: "999", SettingKeyTicketMaxAttachmentSizeMB: "-1",
			SettingKeyTicketNotifyOnStaffReply: "invalid", SettingKeyTicketAutoExpireHours: "9999999999999999999999",
		}}
		cfg, err := NewTicketConfigService(repo).Get(context.Background())
		require.NoError(t, err)
		require.Equal(t, defaultTicketConfig(), cfg)
	})
	t.Run("显式关闭及零值保留", func(t *testing.T) {
		repo := &ticketConfigRepoStub{values: map[string]string{
			SettingKeyTicketEnabled: "false", SettingKeyTicketMaxAttachments: "0",
			SettingKeyTicketAutoExpireHours: "0", SettingKeyTicketNotifyOnStaffReply: "true",
		}}
		cfg, err := NewTicketConfigService(repo).Get(context.Background())
		require.NoError(t, err)
		require.False(t, cfg.Enabled)
		require.Zero(t, cfg.MaxAttachments)
		require.Zero(t, cfg.AutoExpireHours)
		require.True(t, cfg.NotifyOnStaffReply)
	})
}

func TestTicketConfigPatchPreservesOmittedFields(t *testing.T) {
	repo := &ticketConfigRepoStub{values: map[string]string{
		SettingKeyTicketMaxOpenTickets: "7", SettingKeyTicketMaxAttachments: "4",
		SettingKeyTicketMaxAttachmentSizeMB: "12", SettingKeyTicketNotifyOnStaffReply: "true",
	}}
	svc := NewTicketConfigService(repo)
	cfg, err := svc.Update(context.Background(), &UpdateTicketConfigInput{
		Enabled: ticketConfigPtr(false), AutoExpireHours: ticketConfigPtr(0), MaxAttachments: ticketConfigPtr(0),
	})
	require.NoError(t, err)
	require.Equal(t, &TicketConfig{Enabled: false, MaxOpenTickets: 7, MaxAttachments: 0, MaxAttachmentSizeMB: 12, NotifyOnStaffReply: true, AutoExpireHours: 0}, cfg)
	require.Equal(t, []map[string]string{{SettingKeyTicketEnabled: "false", SettingKeyTicketAutoExpireHours: "0", SettingKeyTicketMaxAttachments: "0"}}, repo.writes)
	_, err = svc.Update(context.Background(), &UpdateTicketConfigInput{})
	require.NoError(t, err)
	_, err = svc.Update(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, repo.writes, 1)
}

func TestTicketConfigValidatesRangesBeforeWriting(t *testing.T) {
	tests := []struct {
		name  string
		input *UpdateTicketConfigInput
	}{
		{"工单下限", &UpdateTicketConfigInput{MaxOpenTickets: ticketConfigPtr(0)}},
		{"工单上限", &UpdateTicketConfigInput{MaxOpenTickets: ticketConfigPtr(101)}},
		{"附件下限", &UpdateTicketConfigInput{MaxAttachments: ticketConfigPtr(-1)}},
		{"附件上限", &UpdateTicketConfigInput{MaxAttachments: ticketConfigPtr(11)}},
		{"大小下限", &UpdateTicketConfigInput{MaxAttachmentSizeMB: ticketConfigPtr(0)}},
		{"大小上限", &UpdateTicketConfigInput{MaxAttachmentSizeMB: ticketConfigPtr(21)}},
		{"过期下限", &UpdateTicketConfigInput{AutoExpireHours: ticketConfigPtr(-1)}},
		{"过期上限", &UpdateTicketConfigInput{AutoExpireHours: ticketConfigPtr(8761)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &ticketConfigRepoStub{}
			_, err := NewTicketConfigService(repo).Update(context.Background(), tt.input)
			require.Equal(t, "TICKET_CONFIG_INVALID", infraerrors.Reason(err))
			require.Empty(t, repo.writes)
		})
	}
	for _, value := range []int{1, 100} {
		_, err := NewTicketConfigService(&ticketConfigRepoStub{}).Update(context.Background(), &UpdateTicketConfigInput{MaxOpenTickets: &value})
		require.NoError(t, err)
	}
	_, err := NewTicketConfigService(&ticketConfigRepoStub{}).Update(context.Background(), &UpdateTicketConfigInput{
		MaxAttachments: ticketConfigPtr(10), MaxAttachmentSizeMB: ticketConfigPtr(20), AutoExpireHours: ticketConfigPtr(8760), NotifyOnStaffReply: ticketConfigPtr(true),
	})
	require.NoError(t, err)
}

func TestTicketConfigRepositoryErrorsFailClosed(t *testing.T) {
	dbErr := errors.New("数据库不可用")
	repo := &ticketConfigRepoStub{readErr: dbErr}
	svc := NewTicketConfigService(repo)
	cfg, err := svc.Get(context.Background())
	require.Nil(t, cfg)
	require.ErrorIs(t, err, dbErr)
	cfg, err = svc.Update(context.Background(), &UpdateTicketConfigInput{Enabled: ticketConfigPtr(true)})
	require.Nil(t, cfg)
	require.ErrorIs(t, err, dbErr)
	require.Empty(t, repo.writes)
	repo.readErr, repo.writeErr = nil, dbErr
	cfg, err = svc.Update(context.Background(), &UpdateTicketConfigInput{Enabled: ticketConfigPtr(true)})
	require.Nil(t, cfg)
	require.ErrorIs(t, err, dbErr)
	var unavailable *TicketConfigService
	_, err = unavailable.Get(context.Background())
	require.Error(t, err)
	_, err = NewTicketConfigService(nil).Get(context.Background())
	require.Error(t, err)
}
