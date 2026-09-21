package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/handler"
	"github.com/TokenFlux/TokenRouter/internal/handler/admin"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type pluginStatusRepository struct{ service.PluginRepository }

func (*pluginStatusRepository) GetByID(context.Context, int64) (*service.PluginInstallation, error) {
	return &service.PluginInstallation{ID: 7, State: service.PluginStateDisabled}, nil
}

// 状态轮询免二次验证但仍继承管理员认证；变更接口继续强制二次验证。
func TestPluginStatusRoutesRetainAdminAndStepUpBoundaries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/v1/admin")
	group.Use(func(c *gin.Context) {
		if c.GetHeader("X-Test-Role") != "admin" {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	})
	manager := service.NewPluginManager(&pluginStatusRepository{}, nil, nil, service.PluginHostInfo{}, nil)
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{Plugin: admin.NewPluginHandler(manager)}}
	stepUpCalls := 0
	registerPluginRoutes(group, handlers, func(c *gin.Context) { stepUpCalls++; c.AbortWithStatus(http.StatusUnauthorized) })
	for _, test := range []struct {
		method, path, role string
		want               int
	}{
		{http.MethodGet, "/api/v1/admin/plugins/7/status", "", http.StatusForbidden},
		{http.MethodGet, "/api/v1/admin/plugins/7/status", "user", http.StatusForbidden},
		{http.MethodGet, "/api/v1/admin/plugins/7/status", "admin", http.StatusOK},
		{http.MethodGet, "/api/v1/admin/plugins/invalid/status", "admin", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/admin/plugins/7/enable", "admin", http.StatusUnauthorized},
		{http.MethodPut, "/api/v1/admin/plugins/7/config", "admin", http.StatusUnauthorized},
		{http.MethodPost, "/api/v1/admin/plugins/7/test", "admin", http.StatusUnauthorized},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		request.Header.Set("X-Test-Role", test.role)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		require.Equal(t, test.want, recorder.Code, test.path)
	}
	require.Equal(t, 3, stepUpCalls)
}
