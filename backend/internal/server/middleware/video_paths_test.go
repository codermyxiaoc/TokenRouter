package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 实际鉴权链验证余额耗尽后可取回 Video 任务，但不因 vid_ 前缀改变旧 Grok 普通 Key 的门禁。
func TestSharedVideoTaskAuthPreservesLegacyGrokBillingGate(t *testing.T) {
	for _, google := range []bool{false, true} {
		for _, platform := range []string{service.PlatformVideo, service.PlatformGrok} {
			user := &service.User{ID: 7, Status: service.StatusActive, Balance: 0}
			group := &service.Group{ID: 8, Platform: platform, Status: service.StatusActive, Hydrated: true}
			key := &service.APIKey{ID: 9, UserID: user.ID, Key: "video-auth-fixture", Status: service.StatusAPIKeyQuotaExhausted, User: user, Group: group, GroupID: &group.ID, Quota: 1, QuotaUsed: 1}
			repo := fakeAPIKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}
			cfg := &config.Config{RunMode: config.RunModeStandard}
			svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
			router := gin.New()
			if google {
				router.Use(APIKeyAuthGoogle(svc, cfg))
			} else {
				router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(svc, nil, cfg)))
			}
			router.GET("/v1/videos/:id", func(c *gin.Context) { c.Status(http.StatusOK) })
			router.GET("/v1/videos/:id/content", func(c *gin.Context) { c.Status(http.StatusOK) })
			for _, path := range []string{"/v1/videos/vid_owned", "/v1/videos/vid_owned/content"} {
				request := httptest.NewRequest(http.MethodGet, path, nil)
				request.Header.Set("Authorization", "Bearer "+key.Key)
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				want := http.StatusOK
				if platform == service.PlatformGrok {
					want = http.StatusTooManyRequests
				}
				require.Equal(t, want, response.Code, "google=%v platform=%s path=%s body=%s", google, platform, path, response.Body.String())
			}
		}
	}
}

// 精确查询识别只免除消费检查，未知集合和子路径仍不能读取共享上游账号。
func TestVideoManagementPathBoundary(t *testing.T) {
	for _, path := range []string{"/v1/video/generations/owned", "/api/v1/tasks/owned", "/v2/query/video_generation/owned", "/v1/videos/vid_owned", "/v1/videos/vid_owned/content", "/videos/vid_owned/content"} {
		require.True(t, isVideoTaskManagementRequest(http.MethodGet, path))
		require.True(t, isCompositeKeyNoModelEndpoint(http.MethodGet, path))
		require.True(t, isAPIKeyNonConsumingRequest(http.MethodGet, path))
		require.False(t, isAPIKeyNonConsumingRequest(http.MethodPost, path))
	}
	for _, path := range []string{"/v1/video/generations", "/api/v1/tasks", "/api/v1/tasks/owned/content", "/v2/query/video_generation/../foreign", "/v1/videos", "/v1/videos/upstream_id", "/v1/videos/vid_owned/content/extra"} {
		require.False(t, isVideoTaskManagementRequest(http.MethodGet, path))
	}
}

// Video 保留供应商扩展字段；相同 URL 的旧 Grok 仍执行原有附加模型与响应别名转换。
func TestSharedVideoEndpointKeepsPlatformModelSemantics(t *testing.T) {
	for _, platform := range []string{service.PlatformVideo, service.PlatformGrok} {
		t.Run(platform, func(t *testing.T) {
			writer := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"alias","tools":[{"model":"alias"}]}`))
			c.Request.Header.Set("Content-Type", "application/json")
			group := &service.Group{Platform: platform}
			key := &service.APIKey{Group: group, ModelMapping: map[string]string{"alias": "real"}}
			applyAPIKeyModelRedirect(c, key)
			body, err := io.ReadAll(c.Request.Body)
			require.NoError(t, err)
			_, err = c.Writer.Write([]byte(`{"model":"real"}`))
			require.NoError(t, err)
			if platform == service.PlatformVideo {
				require.Contains(t, string(body), `"tools":[{"model":"alias"}]`)
				require.JSONEq(t, `{"model":"real"}`, writer.Body.String())
			} else {
				require.Contains(t, string(body), `"tools":[{"model":"real"}]`)
				require.JSONEq(t, `{"model":"alias"}`, writer.Body.String())
			}
			// 复合选组时尚未把选中 Key 写入 Context，必须使用显式传入的分组。
			recorder := httptest.NewRecorder()
			c, _ = gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/videos", nil)
			SetCompositeModelContext(c, "PREFIX/alias", "real", group)
			_, err = c.Writer.Write([]byte(`{"model":"real"}`))
			require.NoError(t, err)
			if platform == service.PlatformVideo {
				require.JSONEq(t, `{"model":"real"}`, recorder.Body.String())
			} else {
				require.JSONEq(t, `{"model":"PREFIX/alias"}`, recorder.Body.String())
			}
		})
	}
}

func TestVideoKlingPathMappingPreservesNativeBodyAndResponse(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/omni-video/alias", strings.NewReader(`{"prompt":"alias","tools":[{"model":"alias"}],"unknown":false}`))
	applyAPIKeyModelRedirect(c, &service.APIKey{ModelMapping: map[string]string{"alias": "real"}})
	require.Equal(t, "/omni-video/real", c.Request.URL.Path)
	body, err := io.ReadAll(c.Request.Body)
	require.NoError(t, err)
	require.Equal(t, `{"prompt":"alias","tools":[{"model":"alias"}],"unknown":false}`, string(body))
	response := `{"model":"real","id":"real","status":"submitted"}`
	_, err = c.Writer.Write([]byte(response))
	require.NoError(t, err)
	require.Equal(t, response, recorder.Body.String())
	conflict := httptest.NewRequest(http.MethodPost, "/omni-video/path", strings.NewReader(`{"model":"other"}`))
	_, err = compositeModelFromRequest(conflict)
	require.Error(t, err)
}
