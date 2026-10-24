package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 即使绕过路由组装，处理器也必须在访问服务或凭据前拦截未授权请求。
func TestChanshuiEveryHandlerRequiresAdministrator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewChanshuiHandler(nil)
	for _, role := range []string{"", service.RoleUser} {
		for name, handler := range map[string]gin.HandlerFunc{"list": h.List, "targets": h.Targets, "save": h.Save, "delete": h.Delete, "run": h.Run, "history": h.History, "detail": h.Detail} {
			t.Run(role+"/"+name, func(t *testing.T) {
				r := gin.New()
				r.Use(func(c *gin.Context) {
					if role != "" {
						c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
						c.Set(string(middleware.ContextKeyUserRole), role)
					}
				})
				r.Any("/:id", handler)
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/1", nil))
				want := http.StatusForbidden
				if role == "" {
					want = http.StatusUnauthorized
				}
				require.Equal(t, want, w.Code)
			})
		}
	}
}
