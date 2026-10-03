package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 预览只扩充精确路径，不能让模型画图执行能力扩散到主站脚本或其它文档。
func TestIntelligencePreviewParentCSP(t *testing.T) {
	router := gin.New()
	router.Use(SecurityHeaders(config.CSPConfig{Enabled: true}, nil))
	router.GET("/intelligence", func(c *gin.Context) { c.Status(http.StatusOK) })
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://gateway.example/intelligence", nil))
	policy := recorder.Header().Get("Content-Security-Policy")
	require.True(t, directiveHasValue(policy, "frame-src", "gateway.example/api/v1/intelligence-tests/preview-content/"))
	require.False(t, directiveHasValue(policy, "frame-src", "'self'"))
	require.False(t, directiveHasValue(policy, "script-src", "'unsafe-inline'"))
	require.Contains(t, policy, "'nonce-")
	require.Equal(t, "DENY", recorder.Header().Get("X-Frame-Options"))
}

func TestIntelligencePreviewFrameSourceRejectsPolicyInjection(t *testing.T) {
	for _, host := range []string{"example.com; script-src *", "example.com/path", "example.com evil.test", "example.com\r\n", "*.example.com", "", "example.com:1:2"} {
		require.Empty(t, intelligencePreviewFrameSource(host), host)
	}
	for _, host := range []string{"example.com", "localhost:3000", "127.0.0.1:8080", "[::1]:8080"} {
		require.True(t, strings.HasPrefix(intelligencePreviewFrameSource(host), host+"/api/v1/"), host)
	}
}
