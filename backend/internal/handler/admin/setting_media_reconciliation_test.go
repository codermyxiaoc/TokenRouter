//go:build unit

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 管理接口读写必须返回实际核对期限，并保留未提交的原始值。
func TestSettingsMediaTaskReconciliationTimeout(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	h.GetSettings(c)
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, float64(60), body.Data[service.SettingKeyMediaTaskReconciliationTimeoutMinutes])

	for _, minutes := range []int{1, 120, 10080} {
		rec = doUpdateSettings(t, h, map[string]any{service.SettingKeyMediaTaskReconciliationTimeoutMinutes: minutes}, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, strconv.Itoa(minutes), repo.values[service.SettingKeyMediaTaskReconciliationTimeoutMinutes])
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, float64(minutes), body.Data[service.SettingKeyMediaTaskReconciliationTimeoutMinutes])
	}
	for _, stored := range []string{"180", "broken"} {
		repo.values[service.SettingKeyMediaTaskReconciliationTimeoutMinutes] = stored
		rec = doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, stored, repo.values[service.SettingKeyMediaTaskReconciliationTimeoutMinutes])
	}
}

// 非整数和越界超时必须在保存前拒绝，旧配置与其它字段不能被部分写入。
func TestSettingsRejectInvalidMediaTaskReconciliationTimeout(t *testing.T) {
	for _, value := range []any{0, -1, 10081, 1.5, "60"} {
		h, repo := newStepUpSwitchTestHandler(t, map[string]string{
			service.SettingKeyMediaTaskReconciliationTimeoutMinutes: "120",
			service.SettingKeySiteName:                              "unchanged",
		})
		rec := doUpdateSettings(t, h, map[string]any{
			service.SettingKeyMediaTaskReconciliationTimeoutMinutes: value,
			"site_name": "changed",
		}, nil)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		require.Equal(t, "120", repo.values[service.SettingKeyMediaTaskReconciliationTimeoutMinutes])
		require.Equal(t, "unchanged", repo.values[service.SettingKeySiteName])
	}
}

// 调整核对期限必须进入系统设置审计的字段列表。
func TestSettingsAuditMediaTaskReconciliationTimeout(t *testing.T) {
	before := &service.SystemSettings{MediaTaskReconciliationTimeoutMinutes: 60}
	after := &service.SystemSettings{MediaTaskReconciliationTimeoutMinutes: 120}
	require.Contains(t, diffSettings(before, after, nil, nil, UpdateSettingsRequest{}), service.SettingKeyMediaTaskReconciliationTimeoutMinutes)
	require.NotContains(t, diffSettings(after, after, nil, nil, UpdateSettingsRequest{}), service.SettingKeyMediaTaskReconciliationTimeoutMinutes)
}
