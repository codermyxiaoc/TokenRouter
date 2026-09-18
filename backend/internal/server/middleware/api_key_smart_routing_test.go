//go:build unit

package middleware

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type smartRoutingResolverStub struct {
	check func(context.Context, *service.Group, string, string) (service.SmartRoutingGroupAvailability, error)
}

func (s smartRoutingResolverStub) EvaluateSmartRoutingGroup(ctx context.Context, group *service.Group, model, endpoint string) (service.SmartRoutingGroupAvailability, error) {
	return s.check(ctx, group, model, endpoint)
}

// smartRoutingTestKey 使用不同平台的公开分组验证顺序与请求快照隔离。
func smartRoutingTestKey() *service.APIKey {
	key := &service.APIKey{ID: 1, UserID: 2, Key: "sk-smart", Status: service.StatusActive, SmartRouting: true,
		BillingMode: service.APIKeyBillingModeBalance, User: &service.User{ID: 2, Status: service.StatusActive, Balance: 100}}
	for index, platform := range []string{service.PlatformAnthropic, service.PlatformOpenAI, service.PlatformGemini} {
		groupID := int64(index + 1)
		group := &service.Group{ID: groupID, Platform: platform, Status: service.StatusActive, Hydrated: true}
		key.CompositeGroups = append(key.CompositeGroups, service.APIKeyCompositeGroup{GroupID: groupID, Group: group})
	}
	return key
}

func TestSmartRoutingOrderedCandidatesAndModelRedirect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, firstAvailable := range []bool{false, true} {
		key := smartRoutingTestKey()
		key.ModelMapping = map[string]string{"client-alias": "target-model"}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		original := `{"model":"client-alias","messages":[]}`
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(original))
		var visited []int64
		c.Set(smartRoutingResolverContextKey, smartRoutingResolverStub{check: func(_ context.Context, group *service.Group, model, endpoint string) (service.SmartRoutingGroupAvailability, error) {
			require.Equal(t, "target-model", model)
			require.Equal(t, "/v1/chat/completions", endpoint)
			visited = append(visited, group.ID)
			return service.SmartRoutingGroupAvailability{HasModel: group.ID > 1, Schedulable: group.ID == 3 || firstAvailable}, nil
		}})
		selected, err := resolveSmartRoutingAPIKeyRequest(c, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, nil), key)
		require.NoError(t, err)
		want := int64(3)
		if firstAvailable {
			want = 2
		}
		require.Equal(t, want, *selected.GroupID)
		require.Len(t, visited, int(want))
		require.Nil(t, key.GroupID)
		require.Nil(t, key.Group)
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.JSONEq(t, original, string(body))
	}
}

func TestSmartRoutingRejectsEmptyProbeAndUnsupportedRequests(t *testing.T) {
	for _, test := range []struct{ path, body, code string }{
		{"/v1/messages", `{}`, "SMART_ROUTING_MODEL_REQUIRED"},
		{"/v1/messages/count_tokens", `{"model":" "}`, "SMART_ROUTING_MODEL_REQUIRED"},
		{"/v1/responses", `{"model":42}`, "SMART_ROUTING_MODEL_REQUIRED"},
		{"/v1/responses", `{"model":"gpt"`, "SMART_ROUTING_INVALID_REQUEST"},
		{"/v1/live", `{"model":"gpt"}`, "SMART_ROUTING_ENDPOINT_UNSUPPORTED"},
		{"/v1/responses/unknown", `{"model":"gpt"}`, "SMART_ROUTING_ENDPOINT_UNSUPPORTED"},
	} {
		t.Run(test.path+test.body, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			_, err := resolveSmartRoutingAPIKeyRequest(c, nil, smartRoutingTestKey())
			require.Equal(t, test.code, infraerrors.Reason(err))
		})
	}
}

func TestSmartRoutingDoesNotHideResolverFailure(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"target"}`))
	visits := 0
	c.Set(smartRoutingResolverContextKey, smartRoutingResolverStub{check: func(context.Context, *service.Group, string, string) (service.SmartRoutingGroupAvailability, error) {
		visits++
		return service.SmartRoutingGroupAvailability{}, errors.New("snapshot unavailable")
	}})
	_, err := resolveSmartRoutingAPIKeyRequest(c, nil, smartRoutingTestKey())
	require.Equal(t, "SMART_ROUTING_UNAVAILABLE", infraerrors.Reason(err))
	require.Equal(t, 1, visits)
}

func TestSmartRoutingFiltersRevokedAndForcedPlatformGroups(t *testing.T) {
	key := smartRoutingTestKey()
	key.User.DisabledPublicGroups = []int64{2}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"target"}`))
	var visits []int64
	c.Set(smartRoutingResolverContextKey, smartRoutingResolverStub{check: func(_ context.Context, group *service.Group, _, _ string) (service.SmartRoutingGroupAvailability, error) {
		visits = append(visits, group.ID)
		return service.SmartRoutingGroupAvailability{HasModel: true, Schedulable: group.ID == 3}, nil
	}})
	selected, err := resolveSmartRoutingAPIKeyRequest(c, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, nil), key)
	require.NoError(t, err)
	require.Equal(t, int64(3), *selected.GroupID)
	require.Equal(t, []int64{1, 3}, visits)
	visits = nil
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"target"}`))
	c.Set(string(ContextKeyForcePlatform), service.PlatformGemini)
	selected, err = resolveSmartRoutingAPIKeyRequest(c, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, nil), key)
	require.NoError(t, err)
	require.Equal(t, int64(3), *selected.GroupID)
	require.Equal(t, []int64{3}, visits)
}

func TestSmartRoutingAuthenticationBothProtocolsAndModelListQuota(t *testing.T) {
	for _, google := range []bool{false, true} {
		key := smartRoutingTestKey()
		repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}
		cfg := &config.Config{RunMode: config.RunModeStandard}
		keyService := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
		auth := gin.HandlerFunc(NewAPIKeyAuthMiddleware(keyService, nil, cfg))
		if google {
			auth = APIKeyAuthWithSubscriptionGoogle(keyService, nil, cfg)
		}
		resolver := smartRoutingResolverStub{check: func(_ context.Context, group *service.Group, _, _ string) (service.SmartRoutingGroupAvailability, error) {
			return service.SmartRoutingGroupAvailability{HasModel: group.ID == 2, Schedulable: true}, nil
		}}
		router := gin.New()
		router.Use(WithSmartRoutingResolver(resolver, auth))
		router.Use(RequireGroupAssignment(nil, AnthropicErrorWriter))
		router.POST("/v1/responses", func(c *gin.Context) {
			selected, _ := GetAPIKeyFromContext(c)
			require.Equal(t, int64(2), *selected.GroupID)
			c.Status(http.StatusNoContent)
		})
		router.GET("/v1/models", func(c *gin.Context) { c.Status(http.StatusNoContent) })
		router.GET("/v1/models/*model", func(c *gin.Context) { c.Status(http.StatusNoContent) })
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"target"}`))
		req.Header.Set("x-api-key", key.Key)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
		key.Quota = 1
		key.QuotaUsed = 1
		req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("x-api-key", key.Key)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusTooManyRequests, w.Code, w.Body.String())
		// 新检索入口同样不能绕过额度检查或被禁用的 Key 状态。
		req = httptest.NewRequest(http.MethodGet, "/v1/models/vendor/model", nil)
		req.Header.Set("x-api-key", key.Key)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusTooManyRequests, w.Code, w.Body.String())
		key.QuotaUsed = 0
		key.Status = service.StatusDisabled
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	}
}

// 不可用错误须保留协议错误类型，兼容没有结尾斜杠的视频任务创建入口。
func TestSmartRoutingNativeErrorEnvelopes(t *testing.T) {
	for _, test := range []struct{ path, kind string }{
		{"/v1/messages", "api_error"},
		{"/v1/responses", "server_error"},
		{"/v1/videos", "server_error"},
		{"/videos", "server_error"},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, test.path, nil)
		abortSmartRoutingKeyError(c, infraerrors.New(http.StatusServiceUnavailable, "SMART_ROUTING_UNAVAILABLE", "Unavailable"))
		require.Equal(t, http.StatusServiceUnavailable, w.Code)
		require.Equal(t, test.kind, gjson.GetBytes(w.Body.Bytes(), "error.type").String())
		require.Equal(t, "SMART_ROUTING_UNAVAILABLE", gjson.GetBytes(w.Body.Bytes(), "error.code").String())
		require.True(t, c.IsAborted())
	}
}
