//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 检测会消耗测试 Key 的额度，旧库缺失配置时不能自动开启。
func TestIntelligenceSettingDefaultsAndPublicProjection(t *testing.T) {
	for _, value := range []string{"", "false", "true", "invalid"} {
		t.Run(value, func(t *testing.T) {
			repo := &settingPublicRepoStub{values: map[string]string{SettingKeyIntelligenceEnabled: value}}
			svc := NewSettingService(repo, &config.Config{})
			public, err := svc.GetPublicSettings(context.Background())
			require.NoError(t, err)
			require.Equal(t, value == "true", public.IntelligenceEnabled)
			injection, err := svc.GetPublicSettingsForInjection(context.Background())
			require.NoError(t, err)
			encoded, err := json.Marshal(injection)
			require.NoError(t, err)
			var payload map[string]any
			require.NoError(t, json.Unmarshal(encoded, &payload))
			require.Equal(t, value == "true", payload["intelligence_enabled"])
		})
	}
}

func TestIntelligenceSettingRuntimeGate(t *testing.T) {
	for _, value := range []string{"", "false", "true", "invalid"} {
		repo := &settingUpdateRepoStub{values: map[string]string{SettingKeyIntelligenceEnabled: value}}
		svc := NewSettingService(repo, &config.Config{})
		require.Equal(t, value == "true", svc.IsIntelligenceEnabled(context.Background()))
	}
	var absent *SettingService
	// 依赖缺失也应拒绝执行，而不是用可用性探测的默认开启语义。
	require.False(t, absent.IsIntelligenceEnabled(context.Background()))
}
