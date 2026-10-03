package routes

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/handler"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type intelligenceRouteRepo struct{ service.IntelligenceRepository }

func (*intelligenceRouteRepo) ListConfigs(context.Context) ([]service.IntelligenceConfig, error) {
	return []service.IntelligenceConfig{}, nil
}

// 用真实路由验证权限及关闭语义；未授权请求不得进入数据库或触发模型调用。
func TestIntelligenceRoutesAccess(t *testing.T) {
	svc := service.NewIntelligenceService(&intelligenceRouteRepo{}, nil, nil, nil, nil, func(context.Context) bool { return false })
	defer svc.Stop()
	h := &handler.Handlers{IntelligenceTests: handler.NewIntelligenceHandler(svc)}
	for _, tc := range []struct {
		name, role, method, path string
		status                   int
	}{
		{"匿名用户列表", "", "GET", "/intelligence-tests", 401},
		{"匿名管理配置", "", "POST", "/admin/intelligence-tests", 401},
		{"用户不能列管理配置", "user", "GET", "/admin/intelligence-tests", 403},
		{"用户不能创建配置", "user", "POST", "/admin/intelligence-tests", 403},
		{"用户不能编辑配置", "user", "PUT", "/admin/intelligence-tests/1", 403},
		{"用户不能删除配置", "user", "DELETE", "/admin/intelligence-tests/1", 403},
		{"用户不能触发测试", "user", "POST", "/admin/intelligence-tests/1/run", 403},
		{"用户不能读取管理历史", "user", "GET", "/admin/intelligence-tests/1/runs", 403},
		{"用户不能读取管理详情", "user", "GET", "/admin/intelligence-tests/runs/iq_1", 403},
		{"用户不能签管理预览", "user", "GET", "/admin/intelligence-tests/runs/iq_1/preview", 403},
		{"关闭后用户列表拒绝", "user", "GET", "/intelligence-tests", 403},
		{"关闭后用户详情拒绝", "user", "GET", "/intelligence-tests/runs/iq_1", 403},
		{"关闭后用户预览拒绝", "user", "GET", "/intelligence-tests/runs/iq_1/preview", 403},
		{"关闭后保留配置读取", "admin", "GET", "/admin/intelligence-tests", 200},
		{"关闭后管理员不能触发", "admin", "POST", "/admin/intelligence-tests/1/run", 403},
		{"用户无创建接口", "user", "POST", "/intelligence-tests", 404},
		{"用户无触发接口", "user", "POST", "/intelligence-tests/1/run", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(func(c *gin.Context) {
				if tc.role != "" {
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
					c.Set(string(middleware.ContextKeyUserRole), tc.role)
				}
			})
			registerIntelligenceRoutes(r.Group(""), h)
			registerAdminIntelligenceRoutes(r.Group("/admin"), h)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			require.Equal(t, tc.status, w.Code, w.Body.String())
		})
	}
}
