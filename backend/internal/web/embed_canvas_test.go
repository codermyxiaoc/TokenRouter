//go:build embed

package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 子应用丢失的脚本不能得到主站 HTML，入口目录仍由静态文件服务独立交付。
func TestCanvasStaticFilesDoNotFallbackToMainSPA(t *testing.T) {
	files := fstest.MapFS{
		"index.html":                  {Data: []byte("main-spa")},
		"canvas-app/index.html":       {Data: []byte("private-canvas")},
		"canvas-app/assets/editor.js": {Data: []byte("export default 'editor'")},
	}
	server := &FrontendServer{distFS: files, fileServer: http.FileServer(http.FS(files))}
	r := gin.New()
	r.Use(server.Middleware())
	for _, tc := range []struct {
		path string
		code int
		body string
	}{
		{"/canvas-app/", http.StatusOK, "private-canvas"},
		{"/canvas-app/assets/editor.js", http.StatusOK, "export default 'editor'"},
		{"/canvas-app/assets/missing.js", http.StatusNotFound, ""},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		require.Equal(t, tc.code, w.Code, tc.path)
		require.Equal(t, tc.body, w.Body.String(), tc.path)
	}
}
