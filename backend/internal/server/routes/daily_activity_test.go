package routes

import (
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/handler"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 真实注册路径即使脱离上层认证中间件，处理器也必须阻止未授权访问。
func TestDailyActivityRoutesAuthorization(t *testing.T) {
	for _, tc := range []struct {
		method, path, role string
		status             int
	}{
		{"GET", "/daily-activities", "", 401},
		{"GET", "/daily-activities/history", "", 401},
		{"POST", "/daily-activities/claim", "", 401},
		{"GET", "/admin/settings/daily-activity", "", 401},
		{"PUT", "/admin/settings/daily-activity", "user", 403},
		{"GET", "/admin/settings/daily-activity", "user", 403},
		{"DELETE", "/daily-activities/history", "user", 404},
	} {
		t.Run(tc.method+tc.path+tc.role, func(t *testing.T) {
			r := gin.New()
			r.Use(func(c *gin.Context) {
				if tc.role != "" {
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
					c.Set(string(middleware.ContextKeyUserRole), tc.role)
				}
			})
			h := &handler.Handlers{DailyActivities: handler.NewDailyActivityHandler(nil)}
			limited := false
			registerDailyActivityRoutes(r.Group(""), h, func(c *gin.Context) { limited = true; c.Next() })
			registerAdminDailyActivityRoutes(r.Group("/admin"), h)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			require.Equal(t, tc.status, w.Code)
			if tc.path == "/daily-activities/claim" {
				require.True(t, limited)
			}
		})
	}
}
