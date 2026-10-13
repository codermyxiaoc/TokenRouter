//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 公开设置和 HTML 注入只返回开关，不泄露奖励规则或允许旧库自动开启。
func TestDailyActivityPublicSetting(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		enabled bool
	}{
		{"", false}, {"bad", false}, {`{"enabled":true}`, false},
		{`{"enabled":true,"started_at":"bad"}`, false},
		{`{"enabled":false,"started_at":"2026-10-13T00:00:00Z"}`, false},
		{`{"enabled":true,"started_at":"2026-10-13T00:00:00Z"}`, true},
	} {
		repo := &settingPublicRepoStub{values: map[string]string{DailyActivitySettingKey: tc.raw}}
		svc := NewSettingService(repo, &config.Config{})
		public, err := svc.GetPublicSettings(context.Background())
		require.NoError(t, err)
		require.Equal(t, tc.enabled, public.DailyActivityEnabled)
		injected, err := svc.GetPublicSettingsForInjection(context.Background())
		require.NoError(t, err)
		raw, err := json.Marshal(injected)
		require.NoError(t, err)
		var values map[string]any
		require.NoError(t, json.Unmarshal(raw, &values))
		require.Equal(t, tc.enabled, values["daily_activity_enabled"])
		require.NotContains(t, values, "daily_activity_config")
	}
}
