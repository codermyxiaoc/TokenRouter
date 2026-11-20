//go:build unit

package middleware

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 通过真实鉴权中间件验证三类入口的原生错误、正常请求及未授权请求的处理顺序。
func TestGatewayFieldValidationAtAuthentication(t *testing.T) {
	cfg := &config.Config{RunMode: config.RunModeSimple}
	group := &service.Group{ID: 1, Status: service.StatusActive, Hydrated: true}
	key := &service.APIKey{ID: 1, UserID: 1, Group: group, GroupID: &group.ID, Status: service.StatusActive, User: &service.User{ID: 1, Status: service.StatusActive, Balance: 10}}
	repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}
	svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	for _, test := range []struct {
		path, marker string
		google       bool
	}{
		{"/v1/messages", `"type":"error"`, false},
		{"/v1/responses", `"type":"invalid_request_error"`, false},
		{"/v1beta/models/sample:generateContent", `"status":"INVALID_ARGUMENT"`, true},
	} {
		auth := gin.HandlerFunc(NewAPIKeyAuthMiddleware(svc, nil, cfg))
		if test.google {
			auth = APIKeyAuthWithSubscriptionGoogle(svc, nil, cfg)
		}
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Next()
			if test.google && (c.Writer.Status() == 400 || c.Writer.Status() == 413) {
				errType, _ := service.OpsLocalErrorType(c)
				require.Equal(t, "invalid_request_error", errType)
			}
		}, RequestBodyLimit(128), auth)
		r.POST(test.path, func(c *gin.Context) { c.Status(http.StatusNoContent) })
		for _, tc := range []struct {
			body   string
			status int
			auth   bool
		}{
			{`{"model":"sample","options":{"label":"x","Label":"x"}}`, 400, true},
			{`{"model":"sample"}`, 204, true},
			{strings.Repeat("x", 129), 413, true},
			{`{"model":"sample"}`, 401, false},
		} {
			req := httptest.NewRequest("POST", test.path, bytes.NewBufferString(tc.body))
			if tc.auth {
				req.Header.Set("x-api-key", "sample")
				req.Header.Set("x-goog-api-key", "sample")
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code, "%s: %s", test.path, w.Body.String())
			if tc.status == 400 {
				require.Contains(t, w.Body.String(), test.marker)
			}
		}
	}
}
