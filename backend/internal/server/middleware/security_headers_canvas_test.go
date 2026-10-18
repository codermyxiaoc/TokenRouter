package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 内置画布需要同源嵌入，但不能因此放开其它页面或远程脚本执行权限。
func TestSecurityHeadersCanvasIsolation(t *testing.T) {
	for _, path := range []string{"/canvas-app/index.html", "/canvas-app/", "/creative", "/login", "/canvas-app/other.html"} {
		t.Run(path, func(t *testing.T) {
			r := gin.New()
			r.Use(SecurityHeaders(config.CSPConfig{Enabled: true}, nil))
			r.GET(path, func(c *gin.Context) { c.Status(http.StatusOK) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://example.test"+path, nil))
			policy := w.Header().Get("Content-Security-Policy")
			require.NotContains(t, policy, "unsafe-eval")
			if path == "/canvas-app/index.html" || path == "/canvas-app/" {
				require.Equal(t, "SAMEORIGIN", w.Header().Get("X-Frame-Options"))
				require.Contains(t, policy, "frame-ancestors 'self'")
				require.Contains(t, policy, "connect-src 'self';")
				require.Contains(t, policy, "frame-src 'none';")
				require.NotContains(t, policy, "script-src 'self' 'unsafe-inline'")
			} else {
				require.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
				require.Contains(t, policy, "frame-ancestors 'none'")
				require.Contains(t, policy, "example.test/canvas-app/")
			}
		})
	}
}
