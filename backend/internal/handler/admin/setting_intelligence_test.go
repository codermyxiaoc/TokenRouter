//go:build unit

package admin

import (
	"net/http"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 保存其它设置不能隐式关闭检测；显式 false 必须立即持久化。
func TestIntelligenceSettingsPartialUpdate(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{service.SettingKeyIntelligenceEnabled: "true"})
	rec := doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "true", repo.values[service.SettingKeyIntelligenceEnabled])
	rec = doUpdateSettings(t, h, map[string]any{"intelligence_enabled": false}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "false", repo.values[service.SettingKeyIntelligenceEnabled])
	rec = doUpdateSettings(t, h, map[string]any{"intelligence_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "true", repo.values[service.SettingKeyIntelligenceEnabled])
}
