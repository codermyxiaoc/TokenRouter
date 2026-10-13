//go:build unit

package middleware

import (
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

// 缓存豁免必须同时约束方法、资源层级和 ID，防止未来路由新增后意外开放收费操作。
func TestGeminiCacheNonConsumingRequestExactBoundary(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodGet, "/v1beta/cachedContents", true},
		{http.MethodGet, "/v1beta/cachedContents/cache123", true},
		{http.MethodDelete, "/v1beta/cachedContents/cache123", true},
		{http.MethodGet, "/v1beta/cachedContents/" + strings.Repeat("a", 64), true},
		{http.MethodPost, "/v1beta/cachedContents", false},
		{http.MethodPatch, "/v1beta/cachedContents/cache123", false},
		{http.MethodPost, "/v1beta/cachedContents/cache123", false},
		{http.MethodPut, "/v1beta/cachedContents/cache123", false},
		{http.MethodHead, "/v1beta/cachedContents/cache123", false},
		{http.MethodOptions, "/v1beta/cachedContents/cache123", false},
		{http.MethodDelete, "/v1beta/cachedContents", false},
		{http.MethodGet, "/v1beta/cachedContents/", false},
		{http.MethodGet, "/v1beta/cachedContents/cache123/", false},
		{http.MethodGet, "/v1beta/cachedContents/cache123/nested", false},
		{http.MethodGet, "/v1beta/cachedContents/cachedContents/cache123", false},
		{http.MethodGet, "/v1beta/cachedContents//cache123", false},
		{http.MethodGet, "/v1beta/cachedContents/cache123:generateContent", false},
		{http.MethodDelete, "/v1beta/cachedContents/cache123:delete", false},
		{http.MethodGet, "/v1beta/cachedContents/cache123%2fnested", false},
		{http.MethodGet, "/v1beta/cachedContents/cache123\\nested", false},
		{http.MethodGet, "/v1beta/cachedContents/..", false},
		{http.MethodGet, "/v1beta/cachedContents/ cache123", false},
		{http.MethodGet, "/v1beta/cachedContents/cache123 ", false},
		{http.MethodGet, "/v1beta/cachedContents/Cache123", false},
		{http.MethodGet, "/v1beta/cachedContents/" + strings.Repeat("a", 65), false},
		{http.MethodGet, "/v1beta/cachedContentsExtra/cache123", false},
		{http.MethodGet, "/other/v1beta/cachedContents/cache123", false},
		{http.MethodGet, "/v1/cachedContents/cache123", false},
		{http.MethodGet, "/v1beta/models/cache123:generateContent", false},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			require.Equal(t, tc.want, isGeminiCacheNonConsumingRequest(tc.method, tc.path))
			// 团队限额与余额/Key 限额使用同一精确边界，避免两层准入不一致。
			require.Equal(t, tc.want, isAPIKeyNonConsumingRequest(tc.method, tc.path))
		})
	}
}

func TestAPIKeyAuthGeminiCacheManagementAfterFundsExhausted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, google := range []bool{false, true} {
		name := "standard"
		if google {
			name = "google"
		}
		for _, state := range []string{"zero_balance", "quota_status", "runtime_quota", "subscription_unavailable"} {
			t.Run(name+"/"+state, func(t *testing.T) {
				key := geminiCacheAuthTestKey()
				blockedStatus := http.StatusForbidden
				switch state {
				case "zero_balance":
					key.User.Balance = 0
				case "quota_status":
					key.Status = service.StatusAPIKeyQuotaExhausted
					blockedStatus = http.StatusTooManyRequests
				case "runtime_quota":
					key.Quota, key.QuotaUsed = 1, 1
					blockedStatus = http.StatusTooManyRequests
				case "subscription_unavailable":
					key.BillingMode = service.APIKeyBillingModeSubscription
				}
				router := newGeminiCacheAuthTestRouter(t, key, google)
				for _, req := range []struct {
					method string
					path   string
					want   int
				}{
					{http.MethodGet, "/v1beta/cachedContents", http.StatusOK},
					{http.MethodGet, "/v1beta/cachedContents/cache123", http.StatusOK},
					{http.MethodDelete, "/v1beta/cachedContents/cache123", http.StatusOK},
					{http.MethodPost, "/v1beta/cachedContents", blockedStatus},
					{http.MethodPatch, "/v1beta/cachedContents/cache123", blockedStatus},
					{http.MethodDelete, "/v1beta/cachedContents", blockedStatus},
					{http.MethodGet, "/v1beta/cachedContents/cache123/nested", blockedStatus},
					{http.MethodGet, "/v1beta/cachedContents/cache123%2fnested", blockedStatus},
					{http.MethodDelete, "/v1beta/cachedContents/cache123/", blockedStatus},
				} {
					w := httptest.NewRecorder()
					request := httptest.NewRequest(req.method, req.path, nil)
					request.Header.Set("Authorization", "Bearer "+key.Key)
					router.ServeHTTP(w, request)
					require.Equal(t, req.want, w.Code, "%s %s: %s", req.method, req.path, w.Body.String())
				}
			})
		}
	}
}

// 资金耗尽后的资源管理不能跳过身份、Key 停用、分组授权和 IP 限制。
func TestAPIKeyAuthGeminiCacheManagementRetainsAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, google := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			mutate func(*service.APIKey)
			token  string
			want   int
		}{
			{name: "missing_key", want: http.StatusUnauthorized},
			{name: "invalid_key", token: "wrong-key", want: http.StatusUnauthorized},
			{name: "disabled_key", token: "valid", mutate: func(k *service.APIKey) { k.Status = service.StatusDisabled }, want: http.StatusUnauthorized},
			{name: "inactive_user", token: "valid", mutate: func(k *service.APIKey) { k.User.Status = service.StatusDisabled }, want: http.StatusUnauthorized},
			{name: "disabled_group", token: "valid", mutate: func(k *service.APIKey) { k.Group.Status = service.StatusDisabled }, want: http.StatusForbidden},
			{name: "exclusive_group", token: "valid", mutate: func(k *service.APIKey) { k.Group.IsExclusive = true }, want: http.StatusForbidden},
			{name: "ip_denied", token: "valid", mutate: func(k *service.APIKey) { k.IPWhitelist = []string{"192.0.2.7"} }, want: http.StatusForbidden},
		} {
			name := "standard/"
			if google {
				name = "google/"
			}
			t.Run(name+tc.name, func(t *testing.T) {
				key := geminiCacheAuthTestKey()
				key.User.Balance = 0
				if tc.mutate != nil {
					tc.mutate(key)
				}
				router := newGeminiCacheAuthTestRouter(t, key, google)
				for _, method := range []string{http.MethodGet, http.MethodDelete} {
					w := httptest.NewRecorder()
					req := httptest.NewRequest(method, "/v1beta/cachedContents/cache123", nil)
					if tc.token == "valid" {
						req.Header.Set("Authorization", "Bearer "+key.Key)
					} else if tc.token != "" {
						req.Header.Set("Authorization", "Bearer "+tc.token)
					}
					router.ServeHTTP(w, req)
					require.Equal(t, tc.want, w.Code, "%s: %s", method, w.Body.String())
				}
			})
		}
	}
}

func geminiCacheAuthTestKey() *service.APIKey {
	user := &service.User{ID: 11, Role: service.RoleUser, Status: service.StatusActive, Balance: 10}
	group := &service.Group{ID: 9, Platform: service.PlatformGemini, Status: service.StatusActive, RateMultiplier: 1, Hydrated: true}
	return &service.APIKey{ID: 201, UserID: user.ID, Key: "gemini-cache-auth", Status: service.StatusActive, User: user, GroupID: &group.ID, Group: group}
}

func newGeminiCacheAuthTestRouter(t *testing.T, key *service.APIKey, google bool) *gin.Engine {
	t.Helper()
	repo := &stubApiKeyRepo{getByKey: func(_ context.Context, token string) (*service.APIKey, error) {
		if token != key.Key {
			return nil, service.ErrAPIKeyNotFound
		}
		clone, userClone, groupClone := *key, *key.User, *key.Group
		clone.User, clone.Group = &userClone, &groupClone
		return &clone, nil
	}}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	router := gin.New()
	if google {
		router.Use(APIKeyAuthWithSubscriptionGoogle(svc, nil, cfg))
	} else {
		router.Use(apiKeyAuthWithSubscription(svc, nil, cfg))
	}
	// 通配路由只用于验证中间件本身，确保伪装路径不会因测试路由缺失而绕开断言。
	router.Any("/*path", func(c *gin.Context) {
		got, ok := GetAPIKeyFromContext(c)
		require.True(t, ok)
		require.Equal(t, key.ID, got.ID)
		require.Equal(t, key.User.ID, got.User.ID)
		require.Equal(t, key.Group.ID, *got.GroupID)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return router
}
