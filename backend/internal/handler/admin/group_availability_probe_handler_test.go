package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type probeAdminServiceStub struct {
	service.AdminService
	group *service.Group
	saved *service.UpdateGroupInput
}

func (s *probeAdminServiceStub) GetGroup(context.Context, int64) (*service.Group, error) {
	return s.group, nil
}
func (s *probeAdminServiceStub) UpdateGroup(_ context.Context, _ int64, input *service.UpdateGroupInput) (*service.Group, error) {
	s.saved = input
	return s.group, nil
}

type probeHandlerRunnerStub struct {
	calls   int
	err     error
	groupID int64
	config  service.GroupAvailabilityProbeConfig
}

func (s *probeHandlerRunnerStub) RunOnce(_ context.Context, id int64, cfg service.GroupAvailabilityProbeConfig) (*service.GroupAvailabilityProbeResult, error) {
	s.calls++
	s.groupID = id
	s.config = cfg
	if s.err != nil {
		return nil, s.err
	}
	return &service.GroupAvailabilityProbeResult{GroupID: id, ModelID: cfg.ModelID, Protocol: cfg.Protocol, Status: "failed", ErrorMessage: "upstream denied", StartedAt: time.Now(), FinishedAt: time.Now()}, nil
}

func TestGroupAvailabilityProbeHandlerForwardsOnlyProbeAndReturnsFailedObservation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	admin := &probeAdminServiceStub{group: &service.Group{ID: 42, Platform: service.PlatformKimi, Status: "active"}}
	runner := &probeHandlerRunnerStub{}
	h := &GroupHandler{adminService: admin, availabilityProbeRunner: runner}
	router := gin.New()
	router.POST("/groups/:id/test", h.TestAvailabilityProbe)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/groups/42/test", strings.NewReader(`{"name":"unsaved name","availability_probe_config":{"enabled":true,"protocol":"chat_completions","model_id":"kimi-k3","prompt":"hi"}}`)))
	require.Equal(t, 200, w.Code)
	require.Equal(t, 1, runner.calls)
	require.Nil(t, admin.saved, "立即测试不得调用整行更新分组的方法")
	require.Equal(t, int64(42), runner.groupID)
	require.Equal(t, service.GroupAvailabilityProbeConfig{Enabled: true, Protocol: "chat_completions", ModelID: "kimi-k3", Prompt: "hi"}, runner.config)
	require.Contains(t, w.Body.String(), `"group_id":42`)
	require.Contains(t, w.Body.String(), `"success":false`)
	require.Contains(t, w.Body.String(), `"status":"failed"`)
}

func TestGroupAvailabilityProbeHandlerRejectsInvalidRequests(t *testing.T) {
	for _, tc := range []struct{ name, id, body, status string }{
		{"bad_id", "0", `{}`, "active"},
		{"missing_config", "42", `{}`, "active"},
		{"disabled", "42", `{"availability_probe_config":{"enabled":false}}`, "active"},
		{"unsupported_protocol", "42", `{"availability_probe_config":{"enabled":true,"protocol":"gemini"}}`, "active"},
		{"inactive_group", "42", `{"availability_probe_config":{"enabled":true}}`, "inactive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			admin := &probeAdminServiceStub{group: &service.Group{ID: 42, Platform: service.PlatformKimi, Status: tc.status}}
			runner := &probeHandlerRunnerStub{}
			h := &GroupHandler{adminService: admin, availabilityProbeRunner: runner}
			router := gin.New()
			router.POST("/groups/:id/test", h.TestAvailabilityProbe)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/groups/"+tc.id+"/test", strings.NewReader(tc.body)))
			require.Equal(t, 400, w.Code)
			require.Zero(t, runner.calls)
			require.Nil(t, admin.saved)
		})
	}
}

func TestGroupAvailabilityProbeHandlerReturnsBusy(t *testing.T) {
	admin := &probeAdminServiceStub{group: &service.Group{ID: 42, Platform: service.PlatformKimi, Status: "active"}}
	runner := &probeHandlerRunnerStub{err: infraerrors.Conflict("GROUP_AVAILABILITY_PROBE_BUSY", "already running")}
	h := &GroupHandler{adminService: admin, availabilityProbeRunner: runner}
	router := gin.New()
	router.POST("/groups/:id/test", h.TestAvailabilityProbe)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/groups/42/test", strings.NewReader(`{"availability_probe_config":{"enabled":true,"protocol":"chat_completions","model_id":"kimi-k3","prompt":"hi"}}`)))
	require.Equal(t, 409, w.Code)
	require.Contains(t, w.Body.String(), "GROUP_AVAILABILITY_PROBE_BUSY")
	require.Nil(t, admin.saved, "忙碌时不应在领取租约之前保存配置")
	require.Equal(t, service.GroupAvailabilityProbeConfig{Enabled: true, Protocol: "chat_completions", ModelID: "kimi-k3", Prompt: "hi"}, runner.config)
}
