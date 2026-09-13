package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/handler/dto"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 系统设置使用带 payment 前缀的接口字段，读写都映射到同一个独立余额支付开关。
func TestSettingHandlerWalletPaymentRoundTripAndPartialUpdate(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingBalancePayDisabled:  "true",
		service.SettingEnabledPaymentTypes: "stripe",
	})
	h.paymentConfigService = service.NewPaymentConfigService(nil, repo, nil)

	for _, tc := range []struct {
		body map[string]any
		want bool
	}{
		{body: map[string]any{"payment_wallet_payment_enabled": true}, want: true},
		{body: map[string]any{"payment_enabled": true}, want: true},
		{body: map[string]any{"payment_wallet_payment_enabled": false}},
	} {
		rec := doUpdateSettings(t, h, tc.body, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var updated struct {
			Data dto.SystemSettings `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &updated))
		require.Equal(t, tc.want, updated.Data.PaymentWalletPaymentEnabled)
		require.Equal(t, tc.want, repo.values[service.SettingWalletPaymentEnabled] == "true")
		require.Equal(t, "true", repo.values[service.SettingBalancePayDisabled])
		require.Equal(t, "stripe", repo.values[service.SettingEnabledPaymentTypes])

		// GET 响应也必须反映最新持久化值，避免设置刷新后显示旧状态。
		getRec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(getRec)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
		h.GetSettings(c)
		require.Equal(t, http.StatusOK, getRec.Code, getRec.Body.String())
		var loaded struct {
			Data dto.SystemSettings `json:"data"`
		}
		require.NoError(t, json.Unmarshal(getRec.Body.Bytes(), &loaded))
		require.Equal(t, tc.want, loaded.Data.PaymentWalletPaymentEnabled)
	}
}
