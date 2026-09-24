package routes

import (
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/handler"
	adminhandler "github.com/TokenFlux/TokenRouter/internal/handler/admin"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestAdminGroupAvailabilityProbeRoute 验证立即测试仅挂载在管理员分组路由，并继承认证中间件。
func TestAdminGroupAvailabilityProbeRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	admin := router.Group("/api/v1/admin", func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) })
	h := &handler.Handlers{Admin: &handler.AdminHandlers{Group: adminhandler.NewGroupHandler(nil, nil, nil, nil)}}
	registerGroupRoutes(admin, h)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups/42/availability-probe/test", nil))
	require.Equal(t, http.StatusUnauthorized, w.Code)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/groups/42/availability-probe/test", nil))
	require.Equal(t, http.StatusNotFound, w.Code)
}

// TestAdminImageStorageRoutesRequireAdminAndStepUp 锁定已恢复的异步图片配置入口及权限边界。
func TestAdminImageStorageRoutesRequireAdminAndStepUp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	authorized := false
	admin := router.Group("/api/v1/admin", func(c *gin.Context) {
		if !authorized {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Next()
	})
	storage := service.NewImageStorageSettingService(nil, nil, nil, nil, config.ImageStorageConfig{Bucket: "test-images", SecretAccessKey: "must-not-expose"})
	h := &handler.Handlers{Admin: &handler.AdminHandlers{Backup: adminhandler.NewBackupHandler(nil, nil, storage)}}
	stepUpCalls := 0
	registerBackupRoutes(admin, h, func(c *gin.Context) { stepUpCalls++; c.AbortWithStatus(http.StatusForbidden) })
	registered := make(map[string]bool)
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	imageRoutes := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/backups/image-storage"},
		{http.MethodPut, "/api/v1/admin/backups/image-storage"},
		{http.MethodPost, "/api/v1/admin/backups/image-storage/test"},
	}
	for _, route := range imageRoutes {
		require.True(t, registered[route.method+" "+route.path])
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(route.method, route.path, nil))
		require.Equal(t, http.StatusUnauthorized, w.Code)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(route.method, "/api/v1"+route.path[len("/api/v1/admin"):], nil))
		require.Equal(t, http.StatusNotFound, w.Code, "用户路径不能读取或修改图片存储凭据")
	}
	require.Zero(t, stepUpCalls, "管理员身份校验应先于敏感操作验证")
	authorized = true
	for _, route := range imageRoutes {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(route.method, route.path, nil))
		if route.method == http.MethodGet {
			require.Equal(t, http.StatusOK, w.Code)
			require.Contains(t, w.Body.String(), "test-images")
			require.Contains(t, w.Body.String(), `"secret_configured":true`)
			require.NotContains(t, w.Body.String(), "must-not-expose")
			require.Zero(t, stepUpCalls, "管理员只读设置无需二次验证")
		} else {
			require.Equal(t, http.StatusForbidden, w.Code, "修改配置与连接测试必须先通过二次验证")
		}
	}
	require.Equal(t, 2, stepUpCalls)
	// 图片存储配置与数据库备份并存，恢复入口不能覆盖原有备份路由。
	for _, route := range []string{
		"GET /api/v1/admin/backups/storage-config",
		"PUT /api/v1/admin/backups/storage-config",
		"POST /api/v1/admin/backups/storage-config/test",
		"GET /api/v1/admin/backups/content-config",
		"PUT /api/v1/admin/backups/content-config",
		"GET /api/v1/admin/backups/s3-config",
		"PUT /api/v1/admin/backups/s3-config",
		"POST /api/v1/admin/backups/s3-config/test",
		"GET /api/v1/admin/backups/schedule",
		"PUT /api/v1/admin/backups/schedule",
	} {
		require.True(t, registered[route], "%s 应保持注册", route)
	}
}

// TestAdminUpstreamBillingProbeRoutesAreRemoved 锁定声明倍率探测管理接口全部返回普通 404。
func TestAdminUpstreamBillingProbeRoutesAreRemoved(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	admin := router.Group("/api/v1/admin")
	h := &handler.Handlers{Admin: &handler.AdminHandlers{
		Account:          &adminhandler.AccountHandler{},
		OAuth:            &adminhandler.OAuthHandler{},
		OpenAIOAuth:      &adminhandler.OpenAIOAuthHandler{},
		CodexInviteReset: &adminhandler.CodexInviteResetHandler{},
	}}
	registerAccountRoutes(admin, h, func(c *gin.Context) { c.Next() })

	registered := make(map[string]bool)
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	removed := []struct {
		method      string
		routePath   string
		requestPath string
	}{
		{method: http.MethodGet, routePath: "/api/v1/admin/accounts/upstream-billing-probe/settings", requestPath: "/api/v1/admin/accounts/upstream-billing-probe/settings"},
		{method: http.MethodPut, routePath: "/api/v1/admin/accounts/upstream-billing-probe/settings", requestPath: "/api/v1/admin/accounts/upstream-billing-probe/settings"},
		{method: http.MethodPost, routePath: "/api/v1/admin/accounts/upstream-billing-probe/batch", requestPath: "/api/v1/admin/accounts/upstream-billing-probe/batch"},
		{method: http.MethodPut, routePath: "/api/v1/admin/accounts/:id/upstream-billing-probe", requestPath: "/api/v1/admin/accounts/42/upstream-billing-probe"},
		{method: http.MethodPost, routePath: "/api/v1/admin/accounts/:id/upstream-billing-probe", requestPath: "/api/v1/admin/accounts/42/upstream-billing-probe"},
	}
	for _, route := range removed {
		require.False(t, registered[route.method+" "+route.routePath])
		w := httptest.NewRecorder()
		req := httptest.NewRequest(route.method, route.requestPath, nil)
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusNotFound, w.Code, "method=%s path=%s", route.method, route.requestPath)
	}
}

func TestAdminAdvancedSchedulerScoreRoutesAreRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	admin := router.Group("/api/v1/admin")
	h := &handler.Handlers{Admin: &handler.AdminHandlers{
		Account:          &adminhandler.AccountHandler{},
		OAuth:            &adminhandler.OAuthHandler{},
		OpenAIOAuth:      &adminhandler.OpenAIOAuthHandler{},
		CodexInviteReset: &adminhandler.CodexInviteResetHandler{},
	}}
	registerAccountRoutes(admin, h, func(c *gin.Context) { c.Next() })

	registered := make(map[string]bool)
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	require.True(t, registered["GET /api/v1/admin/accounts/:id/advanced-scheduler-score"])
	require.True(t, registered["POST /api/v1/admin/accounts/:id/advanced-scheduler-score/preview"])
}

// TestCanonicalBackupIDRouteGuard 验证备份通配路由只接受服务实际生成的 ID 格式。
func TestCanonicalBackupIDRouteGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/backups/:id", requireCanonicalBackupID, func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	tests := []struct {
		name       string
		id         string
		wantStatus int
	}{
		{name: "canonical", id: "0a1b2c3d", wantStatus: http.StatusNoContent},
		{name: "named segment is not a backup ID", id: "image-storage", wantStatus: http.StatusNotFound},
		{name: "uppercase", id: "0A1B2C3D", wantStatus: http.StatusNotFound},
		{name: "invalid character", id: "0a1b2c3g", wantStatus: http.StatusNotFound},
		{name: "wrong length", id: "0a1b2c3", wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/backups/"+tt.id, nil)
			router.ServeHTTP(w, req)
			require.Equal(t, tt.wantStatus, w.Code)
		})
	}
}
