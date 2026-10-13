package admin

import (
	"context"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type billingProbeHandlerSettings struct {
	service.SettingRepository
	value  string
	writes int
}

func (r *billingProbeHandlerSettings) GetValue(context.Context, string) (string, error) {
	if r.value == "" {
		return "", service.ErrSettingNotFound
	}
	return r.value, nil
}
func (r *billingProbeHandlerSettings) Set(_ context.Context, _ string, value string) error {
	r.value = value
	r.writes++
	return nil
}

// 异常字段不应把全局开关默默关闭，也不能保存非法周期。
func TestUpstreamBillingProbeSettingsHTTPValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, body := range []string{`{}`, `{"enabled":null,"interval_minutes":30}`, `{"enabled":true}`, `{"enabled":"true","interval_minutes":30}`, `{"enabled":true,"interval_minutes":4}`, `{"enabled":true,"interval_minutes":1441}`, `{"enabled":true,"interval_minutes":30.5}`} {
		t.Run(body, func(t *testing.T) {
			repo := &billingProbeHandlerSettings{}
			settings := service.NewSettingService(repo, nil)
			h := &AccountHandler{upstreamBillingProbe: service.NewUpstreamBillingProbeService(nil, nil, settings)}
			defer h.upstreamBillingProbe.Stop()
			router := gin.New()
			router.PUT("/settings", h.UpdateUpstreamBillingProbeSettings)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Zero(t, repo.writes)
		})
	}
}

func TestUpstreamBillingProbeRejectsInvalidHTTPAccountID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &AccountHandler{upstreamBillingProbe: service.NewUpstreamBillingProbeService(nil, nil, nil)}
	defer h.upstreamBillingProbe.Stop()
	router := gin.New()
	router.POST("/accounts/:id/probe", h.ProbeUpstreamBilling)
	for _, id := range []string{"0", "-1", "abc", "9223372036854775808"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/accounts/"+id+"/probe", nil))
		require.Equal(t, http.StatusBadRequest, w.Code)
	}
}
