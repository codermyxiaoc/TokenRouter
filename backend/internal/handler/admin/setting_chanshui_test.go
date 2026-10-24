//go:build unit

package admin

import (
	"net/http"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 兼容老设置页面的局部保存，只有显式提供开关才允许改变检测状态。
func TestChanshuiSettingsPartialUpdate(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{service.SettingKeyChanshuiEnabled: "true"})
	rec := doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "true", repo.values[service.SettingKeyChanshuiEnabled])
	rec = doUpdateSettings(t, h, map[string]any{"chanshui_enabled": false}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "false", repo.values[service.SettingKeyChanshuiEnabled])
	rec = doUpdateSettings(t, h, map[string]any{"chanshui_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "true", repo.values[service.SettingKeyChanshuiEnabled])
}
