//go:build unit

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
	"github.com/tidwall/gjson"
)

func TestSeedanceTaskManagementPathBoundary(t *testing.T) {
	for _, root := range []string{"/api/v3", "/v3", "/v1", ""} {
		path := root + "/contents/generations/tasks"
		require.True(t, isSeedanceCreateRequest(http.MethodPost, path))
		require.False(t, isSeedanceTaskManagementRequest(http.MethodGet, path), "不能开放共享账号的任务列表")
		require.False(t, isAPIKeyNonConsumingRequest(http.MethodPost, path))
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			require.True(t, isSeedanceTaskManagementRequest(method, path+"/cgt-123"))
			require.True(t, isCompositeKeyNoModelEndpoint(method, path+"/cgt-123"))
			require.True(t, isCompositeKeyBillingBypassEndpoint(method, path+"/cgt-123"))
			require.True(t, isAPIKeyNonConsumingRequest(method, path+"/cgt-123"))
		}
		for _, suffix := range []string{"/..", "/.", "//cgt-123", "/cgt-123/content", "/cgt%2f123", "/cgt\\123"} {
			require.False(t, isSeedanceTaskManagementRequest(http.MethodGet, path+suffix), suffix)
		}
		require.False(t, isSeedanceTaskManagementRequest(http.MethodPost, path+"/cgt-123"))
		require.False(t, isSeedanceTaskManagementRequest(http.MethodPatch, path+"/cgt-123"))
	}
	require.False(t, isSeedanceTaskManagementRequest(http.MethodGet, "/other/contents/generations/tasks/cgt-123"))
}

func TestSeedanceSmartRoutingSelectsOpenAIWithoutCreateReplay(t *testing.T) {
	for _, prefix := range []string{"/api/v3", "/v3", "/v1", ""} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, prefix+"/contents/generations/tasks", strings.NewReader(`{"model":"video","content":[{"type":"text","text":"waves"}]}`))
		require.True(t, smartRoutingModelEndpoint(c))
		require.False(t, smartRoutingReplayEndpoint(c), "异步任务不能因失败跨组重放")
		var checked []int64
		c.Set(smartRoutingResolverContextKey, smartRoutingResolverStub{check: func(_ context.Context, group *service.Group, model, _ string) (service.SmartRoutingGroupAvailability, error) {
			checked = append(checked, group.ID)
			require.Equal(t, service.PlatformOpenAI, group.Platform)
			require.Equal(t, "video", model)
			return service.SmartRoutingGroupAvailability{HasModel: true, Schedulable: true}, nil
		}})
		selected, err := resolveSmartRoutingAPIKeyRequest(c, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, nil), smartRoutingTestKey())
		require.NoError(t, err)
		require.Equal(t, int64(2), *selected.GroupID)
		require.Equal(t, []int64{2}, checked)
	}
}

func TestSeedanceTaskManagementAuthRetainsIdentityChecks(t *testing.T) {
	for _, google := range []bool{false, true} {
		for _, smart := range []bool{false, true} {
			key := smartRoutingTestKey()
			key.SmartRouting = smart
			if !smart {
				key.Group = key.CompositeGroups[1].Group
				key.GroupID = &key.Group.ID
			}
			key.Status = service.StatusAPIKeyQuotaExhausted
			key.Quota, key.QuotaUsed = 1, 1
			key.User.Balance = 0
			repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}
			cfg := &config.Config{RunMode: config.RunModeStandard}
			keyService := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
			auth := gin.HandlerFunc(NewAPIKeyAuthMiddleware(keyService, nil, cfg))
			if google {
				auth = APIKeyAuthWithSubscriptionGoogle(keyService, nil, cfg)
			}
			router := gin.New()
			router.Use(auth, RequireGroupAssignment(nil, AnthropicErrorWriter))
			for _, method := range []string{http.MethodGet, http.MethodDelete} {
				router.Handle(method, "/api/v3/contents/generations/tasks/:task_id", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			}
			for _, method := range []string{http.MethodGet, http.MethodDelete} {
				request := httptest.NewRequest(method, "/api/v3/contents/generations/tasks/cgt-123", nil)
				request.Header.Set("x-api-key", key.Key)
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
			}
			key.Status = service.StatusDisabled
			request := httptest.NewRequest(http.MethodDelete, "/api/v3/contents/generations/tasks/cgt-123", nil)
			request.Header.Set("x-api-key", key.Key)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusUnauthorized, recorder.Code, "免消费准入不得绕过禁用 Key")
		}
	}
}

func TestSeedanceModelRedirectPreservesNativeResponseAndExtensionFields(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v3/contents/generations/tasks", strings.NewReader(`{"model":"video","tools":[{"model":"video"}],"content":[{"type":"text","text":"video"}]}`))
	SetCompositeModelContext(c, "ARK/video", "video")
	applyAPIKeyModelRedirect(c, &service.APIKey{ModelMapping: map[string]string{"video": "ep-video"}})
	body, err := io.ReadAll(c.Request.Body)
	require.NoError(t, err)
	require.Equal(t, "ep-video", gjson.GetBytes(body, "model").String())
	require.Equal(t, "video", gjson.GetBytes(body, "tools.0.model").String())
	response := `{"id":"ep-video","model":"ep-video","status":"queued","unknown":{"model":"ep-video"}}`
	_, err = c.Writer.Write([]byte(response))
	require.NoError(t, err)
	require.Equal(t, response, recorder.Body.String(), "Ark 原生响应不能恢复为客户端别名")
}
