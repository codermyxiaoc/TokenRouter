package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/handler"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMediaTaskReconciliationRouteOnlyRegistersAdminPOST(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		method, path, role string
		code               int
	}{
		{http.MethodPost, "/api/v1/media-tasks/1/settlement", "admin", http.StatusNotFound},
		{http.MethodGet, "/api/v1/admin/media-tasks/1/settlement", "admin", http.StatusNotFound},
		{http.MethodDelete, "/api/v1/admin/media-tasks/1/settlement", "admin", http.StatusNotFound},
		{http.MethodPut, "/api/v1/admin/media-tasks/1/settlement", "admin", http.StatusNotFound},
		{http.MethodPost, "/api/v1/admin/media-tasks/1/settlement", "", http.StatusUnauthorized},
		{http.MethodPost, "/api/v1/admin/media-tasks/1/settlement", "user", http.StatusForbidden},
		// 使用非法 ID 在真实注册路由上验证命中处理器，无需创建账务仓储。
		{http.MethodPost, "/api/v1/admin/media-tasks/bad/settlement", "admin", http.StatusBadRequest},
	} {
		t.Run(test.method+test.path+test.role, func(t *testing.T) {
			router := gin.New()
			router.Use(func(c *gin.Context) {
				if test.role != "" {
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
					c.Set(string(middleware.ContextKeyUserRole), test.role)
				}
			})
			h := &handler.Handlers{MediaTask: handler.NewMediaTaskHandler(nil)}
			registerMediaTaskRoutes(router.Group("/api/v1"), h, false)
			registerMediaTaskRoutes(router.Group("/api/v1/admin"), h, true)
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(`{"action":"release","reason":"核对"}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, test.code, response.Code, response.Body.String())
		})
	}
}
