package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// 网关接口对未登记来源放行跨域（无凭据），管理接口仍按白名单拒绝。
func TestCORSGatewayAllowsAnyOriginWithoutCredentials(t *testing.T) {
	r := gin.New()
	r.Use(CORS(config.CORSConfig{AllowedOrigins: []string{"https://admin.example"}, AllowCredentials: true, GatewayAllowAllOrigins: true}))
	r.Any("/*path", func(c *gin.Context) { c.Status(http.StatusOK) })

	preflight := httptest.NewRequest(http.MethodOptions, "/v1/videos", nil)
	preflight.Header.Set("Origin", "https://app.example")
	preflight.Header.Set("Access-Control-Request-Headers", "authorization, content-type, x-custom")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, preflight)
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "authorization, content-type, x-custom", w.Header().Get("Access-Control-Allow-Headers"))
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Credentials"))

	get := httptest.NewRequest(http.MethodGet, "/v1/videos/vid_1", nil)
	get.Header.Set("Origin", "https://app.example")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, get)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, w.Header().Get("Access-Control-Expose-Headers"), "X-Video-Task-ID")

	admin := httptest.NewRequest(http.MethodOptions, "/api/v1/admin/settings", nil)
	admin.Header.Set("Origin", "https://app.example")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, admin)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))

	// 白名单来源在网关上仍保持原有的精确来源与凭据行为。
	listed := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	listed.Header.Set("Origin", "https://admin.example")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, listed)
	assert.Equal(t, "https://admin.example", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", w.Header().Get("Access-Control-Allow-Credentials"))

	// 关闭开关后保持原有拒绝行为。
	disabled := gin.New()
	disabled.Use(CORS(config.CORSConfig{}))
	disabled.Any("/*path", func(c *gin.Context) { c.Status(http.StatusOK) })
	w = httptest.NewRecorder()
	disabled.ServeHTTP(w, preflight)
	assert.Equal(t, http.StatusForbidden, w.Code)
}
