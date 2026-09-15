package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 验证实际公开DTO不会遗漏开关，匿名客户端只获得开关而非工单数据。
func TestTicketPublicSettingsResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, value := range []string{"", "true", "false"} {
		repo := &ticketHandlerSettings{values: map[string]string{service.SettingKeyTicketEnabled: value}}
		h := NewSettingHandler(service.NewSettingService(repo, &config.Config{}), "test")
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/api/v1/settings/public", nil)
		h.GetPublicSettings(c)
		require.Equal(t, 200, w.Code, w.Body.String())
		var result struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
		require.Equal(t, value != "false", result.Data["ticket_enabled"])
		require.NotContains(t, result.Data, "tickets")
	}
}
