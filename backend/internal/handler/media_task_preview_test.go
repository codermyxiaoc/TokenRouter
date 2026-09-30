package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type mediaTaskPreviewHandlerRepo struct {
	service.MediaTaskRepository
	actor  service.MediaTaskActor
	filter service.MediaTaskFilter
}

func (r *mediaTaskPreviewHandlerRepo) ListModels(_ context.Context, actor service.MediaTaskActor, filter service.MediaTaskFilter) ([]string, error) {
	r.actor, r.filter = actor, filter
	return []string{"image-a", "image-b"}, nil
}
func (r *mediaTaskPreviewHandlerRepo) Get(_ context.Context, actor service.MediaTaskActor, id int64) (*service.MediaTask, error) {
	r.actor = actor
	if id != 1 || (!actor.IsAdmin && actor.UserID != 7) {
		return nil, service.ErrMediaTaskNotFound
	}
	return &service.MediaTask{ID: 1, UserID: 7, APIKeyID: 9, Source: "grok_video", Status: "completed"}, nil
}

func TestMediaTaskPreviewHTTPAuthorizationAndSafeResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, path, role string
		userID           int64
		admin            bool
		status           int
	}{
		{"模型需登录", "/tasks/models", "", 0, false, 401},
		{"预览需登录", "/tasks/1/preview", "", 0, false, 401},
		{"个人模型忽略其他owner", "/tasks/models?user_id=99&model=image-a&page=999", "user", 7, false, 200},
		{"普通用户无管理权限", "/tasks/1/preview", "user", 7, true, 403},
		{"管理员个人入口仍隔离", "/tasks/1/preview", "admin", 8, false, 404},
		{"管理员可预览他人", "/tasks/1/preview", "admin", 8, true, 200},
		{"管理员模型指定owner", "/tasks/models?user_id=99", "admin", 8, true, 200},
		{"管理员用户参数必须有效", "/tasks/models?user_id=bad", "admin", 8, true, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &mediaTaskPreviewHandlerRepo{}
			h := NewMediaTaskHandler(service.NewMediaTaskService(repo))
			router := gin.New()
			router.Use(func(c *gin.Context) {
				if tc.userID > 0 {
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: tc.userID})
					c.Set(string(middleware.ContextKeyUserRole), tc.role)
				}
			})
			if tc.admin {
				router.GET("/tasks/models", h.AdminModels)
				router.GET("/tasks/:id/preview", h.AdminPreview)
			} else {
				router.GET("/tasks/models", h.Models)
				router.GET("/tasks/:id/preview", h.Preview)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.status == 200 {
				require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
				require.NotContains(t, w.Body.String(), "account_id")
				if tc.path == "/tasks/models?user_id=99&model=image-a&page=999" {
					require.EqualValues(t, 7, repo.filter.UserID)
					require.JSONEq(t, `{"code":0,"message":"success","data":["image-a","image-b"]}`, w.Body.String())
				}
			}
		})
	}
}

func TestMediaTaskPreviewContentRejectsMissingTicketWithoutSession(t *testing.T) {
	h := NewMediaTaskHandler(service.NewMediaTaskService(&mediaTaskPreviewHandlerRepo{}))
	router := gin.New()
	router.GET("/content/:ticket", h.PreviewContent)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/content/not-a-ticket", nil))
	require.Equal(t, 404, w.Code)
	require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "no-referrer", w.Header().Get("Referrer-Policy"))
}

func TestMediaTaskPreviewContentFilenameMatchesContainer(t *testing.T) {
	for mime, filename := range map[string]string{"video/mp4": "preview.mp4", "video/webm": "preview.webm", "video/ogg": "preview.ogv", "video/quicktime": "preview.mov"} {
		require.Equal(t, filename, mediaTaskPreviewFilename(mime))
	}
}
