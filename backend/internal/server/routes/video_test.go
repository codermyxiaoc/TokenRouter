package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	servermiddleware "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 注册测试与旧Grok/Ark路由共用完整路由表，检测Gin冲突和误分派。
func TestVideoRoutesRegisteredAlongsideLegacyMedia(t *testing.T) {
	router := newGatewayRoutesTestRouter(service.PlatformVideo)
	for _, request := range []struct{ method, path string }{
		{"POST", "/v1/video/generations"}, {"GET", "/v1/video/generations/id"}, {"DELETE", "/v1/video/generations/id"},
		{"POST", "/video/generations"}, {"GET", "/v1/tasks/id"}, {"DELETE", "/v1/tasks/id"}, {"POST", "/omni-video/prefix/model"},
		{"POST", "/v1/videos/text2video"}, {"POST", "/v1/videos/omni-video"}, {"GET", "/tasks?task_ids=id"},
		{"POST", "/api/v1/services/aigc/video-generation/video-synthesis"}, {"GET", "/api/v1/tasks/id"},
		{"POST", "/v2/video_generation"}, {"GET", "/v2/query/video_generation/id"},
		{"POST", "/v1/videos"}, {"GET", "/v1/videos/vid_owned"}, {"GET", "/v1/videos/vid_owned/content"},
		{"POST", "/videos"}, {"GET", "/videos/vid_owned"}, {"GET", "/videos/vid_owned/content"},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(request.method, request.path, strings.NewReader(`{"model":"m"}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusServiceUnavailable, w.Code, request.method+" "+request.path)
	}
}

// 共享别名不能接管旧 Grok 的生成/编辑路径，已存在本站任务可在改组后取回。
func TestOpenAIVideoSharedRoutePlatformDispatch(t *testing.T) {
	for _, test := range []struct {
		platform, method, path string
		want                   bool
	}{
		{service.PlatformVideo, "POST", "/v1/videos", true},
		{service.PlatformGrok, "POST", "/v1/videos", false},
		{service.PlatformOpenAI, "POST", "/v1/videos", false},
		{service.PlatformVideo, "POST", "/v1/videos/generations", false},
		{service.PlatformVideo, "POST", "/v1/videos/text2video", false},
		{service.PlatformGrok, "GET", "/v1/videos/upstream_id/content", false},
		{service.PlatformOpenAI, "GET", "/v1/videos/vid_owned/content", true},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(test.method, test.path, nil)
		c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{Group: &service.Group{Platform: test.platform}})
		require.Equal(t, test.want, shouldHandleVideoPlatformRequest(c), test.path+" "+test.platform)
	}
}
