package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/handler"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type mediaTaskRouteRepo struct{ service.MediaTaskRepository }

func (*mediaTaskRouteRepo) ListModels(context.Context, service.MediaTaskActor, service.MediaTaskFilter) ([]string, error) {
	return []string{"image-a"}, nil
}
func (*mediaTaskRouteRepo) Get(context.Context, service.MediaTaskActor, int64) (*service.MediaTask, error) {
	return nil, service.ErrMediaTaskNotFound
}

// 固定 models 路径不能落入 :id，预览与详情也必须同时保留个人和管理员入口。
func TestMediaTaskRoutesModelsAndPreview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
		c.Set(string(middleware.ContextKeyUserRole), "admin")
	})
	h := &handler.Handlers{MediaTask: handler.NewMediaTaskHandler(service.NewMediaTaskService(&mediaTaskRouteRepo{}))}
	// 公开票据播放与鉴权静态/动态路由共用前缀，必须能够同时注册且命中自己的处理器。
	r.GET("/api/v1/media-tasks/preview-content/:ticket", h.MediaTask.PreviewContent)
	r.HEAD("/api/v1/media-tasks/preview-content/:ticket", h.MediaTask.PreviewContent)
	registerMediaTaskRoutes(r.Group("/api/v1"), h, false)
	registerMediaTaskRoutes(r.Group("/api/v1/admin"), h, true)
	for _, prefix := range []string{"/api/v1", "/api/v1/admin"} {
		for _, tc := range []struct {
			path   string
			status int
		}{{"/media-tasks/models", 200}, {"/media-tasks/123", 404}, {"/media-tasks/123/preview", 404}} {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, prefix+tc.path, nil))
			require.Equal(t, tc.status, w.Code, w.Body.String())
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/media-tasks/preview-content/invalid-ticket", nil))
		require.Equal(t, http.StatusNotFound, w.Code)
		require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"), "必须命中票据处理器，不能落入普通任务详情或兜底404")
		require.Equal(t, "no-referrer", w.Header().Get("Referrer-Policy"))
	}
}
