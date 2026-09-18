//go:build unit

package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 临时读取故障必须拒绝私有请求，同时保留与真正用户失效不同的 HTTP 语义。
func TestPanelAuthUserLookupFailureClassification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, admin := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			err    error
			status int
			code   string
		}{
			{"missing", service.ErrUserNotFound, 401, "USER_NOT_FOUND"},
			{"wrapped_missing", fmt.Errorf("lookup: %w", service.ErrUserNotFound), 401, "USER_NOT_FOUND"},
			{"timeout", context.DeadlineExceeded, 500, "INTERNAL_ERROR"},
			{"connection", errors.New("database connection closed"), 500, "INTERNAL_ERROR"},
		} {
			t.Run(fmt.Sprintf("admin_%t/%s", admin, tc.name), func(t *testing.T) {
				cfg := &config.Config{JWT: config.JWTConfig{Secret: "test-auth-temporary-secret", ExpireHour: 1}}
				auth := service.NewAuthService(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
				users := service.NewUserService(&stubUserRepo{getByID: func(context.Context, int64) (*service.User, error) { return nil, tc.err }}, nil, nil, nil)
				token, err := auth.GenerateToken(context.Background(), &service.User{ID: 1, Role: service.RoleAdmin})
				require.NoError(t, err)
				r := gin.New()
				if admin {
					r.Use(gin.HandlerFunc(NewAdminAuthMiddleware(auth, users, nil, nil)))
				} else {
					r.Use(gin.HandlerFunc(NewJWTAuthMiddleware(auth, users, nil, nil)))
				}
				called := false
				r.GET("/private", func(c *gin.Context) { called = true; c.Status(200) })
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/private", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				r.ServeHTTP(w, req)
				require.Equal(t, tc.status, w.Code)
				require.Contains(t, w.Body.String(), tc.code)
				require.NotContains(t, w.Body.String(), "database connection")
				require.False(t, called)
			})
		}
	}
}
