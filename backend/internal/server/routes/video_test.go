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

// 注册测试与旧Grok/Ark路由共用完整路由表，检测Gin冲突和误分派；
// 视频平台 Key 访问已退役的旧统一与原生入口返回 410，统一入口在服务缺失时返回 503。
func TestVideoRoutesRegisteredAlongsideLegacyMedia(t *testing.T) {
	router := newGatewayRoutesTestRouter(service.PlatformVideo)
	for _, request := range []struct {
		method, path string
		status       int
	}{
		{"POST", "/v1/video/generations", http.StatusGone}, {"GET", "/v1/video/generations/id", http.StatusGone}, {"DELETE", "/v1/video/generations/id", http.StatusGone},
		{"POST", "/video/generations", http.StatusGone}, {"GET", "/v1/tasks/id", http.StatusGone}, {"DELETE", "/v1/tasks/id", http.StatusGone}, {"POST", "/omni-video/prefix/model", http.StatusGone},
		{"POST", "/v1/videos/text2video", http.StatusGone}, {"POST", "/v1/videos/omni-video", http.StatusGone}, {"GET", "/tasks?task_ids=id", http.StatusGone},
		{"POST", "/api/v1/services/aigc/video-generation/video-synthesis", http.StatusGone}, {"GET", "/api/v1/tasks/id", http.StatusGone},
		{"POST", "/v2/video_generation", http.StatusGone}, {"GET", "/v2/query/video_generation/id", http.StatusGone},
		{"POST", "/ent/v2/img2video", http.StatusGone}, {"POST", "/ent/v2/reference2video", http.StatusGone}, {"POST", "/ent/v2/text2video", http.StatusGone},
		{"POST", "/ent/v2/start-end2video", http.StatusGone}, {"GET", "/ent/v2/tasks/id/creations", http.StatusGone},
		{"POST", "/v1/videos", http.StatusServiceUnavailable}, {"GET", "/v1/videos/vid_owned", http.StatusServiceUnavailable}, {"GET", "/v1/videos/vid_owned/content", http.StatusServiceUnavailable},
		{"POST", "/videos", http.StatusServiceUnavailable}, {"GET", "/videos/vid_owned", http.StatusServiceUnavailable}, {"GET", "/videos/vid_owned/content", http.StatusServiceUnavailable},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(request.method, request.path, strings.NewReader(`{"model":"m"}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		require.Equal(t, request.status, w.Code, request.method+" "+request.path)
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
