package routes

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/handler"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type ticketActionRepo struct {
	service.TicketRepository
	actor  service.TicketActor
	target string
	err    error
}

// 配置桩共用于实际路由和服务，确认关闭后配置入口仍能重新开启模块。
type ticketRouteSettings struct {
	service.SettingRepository
	values map[string]string
}

func (s *ticketRouteSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return s.values, nil
}

func (s *ticketRouteSettings) SetMultiple(_ context.Context, values map[string]string) error {
	for key, value := range values {
		s.values[key] = value
	}
	return nil
}

func (r *ticketActionRepo) Close(_ context.Context, actor service.TicketActor, id int64, target string) (*service.Ticket, error) {
	r.actor = actor
	r.target = target
	return &service.Ticket{ID: id, Status: target}, r.err
}

// 从实际路由发起请求，确认管理员撤销入口存在且普通用户不能借此操作其他工单。
func TestTicketRoutesCompletionAndCancellation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, path, role, target string
		admin                    bool
		err                      error
		status                   int
	}{
		{"管理撤销", "/admin/tickets/12/cancel", "admin", "cancelled", true, nil, 200},
		{"管理完成", "/admin/tickets/12/complete", "admin", "completed", true, nil, 200},
		{"个人撤销", "/tickets/12/cancel", "user", "cancelled", false, nil, 200},
		{"拒绝普通用户管理", "/admin/tickets/12/cancel", "user", "", true, nil, 403},
		{"终态拒绝改写", "/admin/tickets/12/cancel", "admin", "cancelled", true, service.ErrTicketClosed, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &ticketActionRepo{err: tc.err}
			cfg := service.NewTicketConfigService(&ticketRouteSettings{})
			h := &handler.Handlers{Ticket: handler.NewTicketHandler(service.NewTicketService(repo, cfg), cfg, nil)}
			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
				c.Set(string(middleware.ContextKeyUserRole), tc.role)
			})
			registerTicketRoutes(r.Group(""), h)
			registerAdminTicketRoutes(r.Group("/admin"), h)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("POST", tc.path, nil))
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.Equal(t, tc.target, repo.target)
			if tc.target != "" {
				require.Equal(t, tc.admin, repo.actor.IsAdmin)
				require.Equal(t, int64(7), repo.actor.UserID)
			}
		})
	}
}

// 覆盖两个角色的全部业务入口；关闭后只保留配置读取和管理员重新开启入口。
func TestTicketRoutesDisabledAndReenabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := &ticketRouteSettings{values: map[string]string{service.SettingKeyTicketEnabled: "false"}}
	cfg := service.NewTicketConfigService(settings)
	repo := &ticketActionRepo{}
	h := &handler.Handlers{Ticket: handler.NewTicketHandler(service.NewTicketService(repo, cfg), cfg, nil)}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
		c.Set(string(middleware.ContextKeyUserRole), "admin")
	})
	registerTicketRoutes(r.Group(""), h)
	registerAdminTicketRoutes(r.Group("/admin"), h)
	for _, prefix := range []string{"/tickets", "/admin/tickets"} {
		for _, request := range []struct{ method, suffix, body string }{
			{"GET", "", ""}, {"GET", "/12", ""}, {"GET", "/12/attachments/1", ""},
			{"GET", "/12/attachments/1/preview", ""},
			{"POST", "/12/replies", "invalid body"}, {"POST", "/12/complete", ""}, {"POST", "/12/cancel", ""},
		} {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(request.method, prefix+request.suffix, strings.NewReader(request.body)))
			require.Equal(t, 403, w.Code, prefix+request.suffix+": "+w.Body.String())
			require.Contains(t, w.Body.String(), "TICKET_DISABLED")
		}
	}
	for _, request := range []struct{ method, path, body string }{
		{"POST", "/tickets", "invalid body"}, {"PATCH", "/admin/tickets/12", `{"priority":"normal"}`},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(request.method, request.path, strings.NewReader(request.body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		require.Equal(t, 403, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), "TICKET_DISABLED")
	}
	for _, path := range []string{"/tickets/config", "/admin/tickets/settings"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		require.Equal(t, 200, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), `"enabled":false`)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/admin/tickets/settings", strings.NewReader(`{"enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code, w.Body.String())
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/admin/tickets/12/complete", nil))
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, "completed", repo.target)
}
