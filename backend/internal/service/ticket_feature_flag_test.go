package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 独立设置、公开接口和HTML注入始终使用同一个开关，显式false不能被默认值覆盖。
func TestTicketPublicSettingsSwitch(t *testing.T) {
	ctx := context.Background()
	repo := &ticketConfigRepoStub{}
	settings := NewSettingService(repo, &config.Config{})
	cfg := ProvideTicketConfigService(repo, settings)
	updates := 0
	// 路由的回调晚于Wire创建配置服务，仍必须收到之后的设置变更。
	settings.SetOnUpdateCallback(func() { updates++ })
	for i, enabled := range []bool{true, false, true} {
		if i > 0 {
			_, err := cfg.Update(ctx, &UpdateTicketConfigInput{Enabled: &enabled})
			require.NoError(t, err)
		}
		public, err := settings.GetPublicSettings(ctx)
		require.NoError(t, err)
		require.Equal(t, enabled, public.TicketEnabled)
		injected, err := settings.GetPublicSettingsForInjection(ctx)
		require.NoError(t, err)
		data, err := json.Marshal(injected)
		require.NoError(t, err)
		var fields map[string]any
		require.NoError(t, json.Unmarshal(data, &fields))
		require.Equal(t, enabled, fields["ticket_enabled"])
	}
	require.Equal(t, 2, updates)
	repo.writeErr = errors.New("write failed")
	_, err := cfg.Update(ctx, &UpdateTicketConfigInput{Enabled: ticketConfigPtr(false)})
	require.Error(t, err)
	require.Equal(t, 2, updates, "写入失败不能通知缓存加载未保存的开关")
}
