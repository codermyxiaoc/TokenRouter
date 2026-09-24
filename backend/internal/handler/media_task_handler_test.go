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

type mediaTaskHandlerRepo struct {
	service.MediaTaskRepository
	actor  service.MediaTaskActor
	filter service.MediaTaskFilter
	called bool
}

func (r *mediaTaskHandlerRepo) List(_ context.Context, actor service.MediaTaskActor, filter service.MediaTaskFilter) (*service.MediaTaskList, error) {
	r.called, r.actor, r.filter = true, actor, filter
	return &service.MediaTaskList{Items: []service.MediaTask{}, Page: filter.Page, PageSize: filter.PageSize}, nil
}
func (r *mediaTaskHandlerRepo) Get(_ context.Context, actor service.MediaTaskActor, id int64) (*service.MediaTask, error) {
	r.called, r.actor = true, actor
	return nil, service.ErrMediaTaskNotFound
}

func TestMediaTaskHTTPAuthorizationAndScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name, role, path        string
		loggedIn, admin, detail bool
		status                  int
	}{
		{"no session", "", "/tasks", false, false, false, 401},
		{"user cannot access admin", "user", "/tasks", true, true, false, 403},
		{"user cannot choose another owner", "user", "/tasks?user_id=99", true, false, false, 200},
		{"admin personal route stays scoped", "admin", "/tasks?user_id=99", true, false, false, 200},
		{"admin can filter users", "admin", "/tasks?user_id=99", true, true, false, 200},
		{"invalid page", "user", "/tasks?page=oops", true, false, false, 400},
		{"hidden task", "user", "/tasks/123", true, false, true, 404},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &mediaTaskHandlerRepo{}
			h := NewMediaTaskHandler(service.NewMediaTaskService(repo))
			router := gin.New()
			router.Use(func(c *gin.Context) {
				if tt.loggedIn {
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
					c.Set(string(middleware.ContextKeyUserRole), tt.role)
				}
			})
			if tt.admin {
				router.GET("/tasks", h.AdminList)
			} else {
				router.GET("/tasks", h.List)
			}
			router.GET("/tasks/:id", h.Get)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.path, nil))
			require.Equal(t, tt.status, w.Code, w.Body.String())
			if tt.status == 200 {
				if tt.admin {
					require.EqualValues(t, 99, repo.filter.UserID)
				} else {
					require.EqualValues(t, 7, repo.filter.UserID)
				}
				require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			} else if !tt.detail {
				require.False(t, repo.called)
			}
		})
	}
}
