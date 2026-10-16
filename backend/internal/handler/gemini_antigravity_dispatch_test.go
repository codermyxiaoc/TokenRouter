//go:build unit

package handler

import (
	"encoding/json"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 替身只记录派发契约，不创建网络连接。
type antigravityNativeDispatchUpstream struct {
	bodies [][]byte
	ids    []int64
}

func (u *antigravityNativeDispatchUpstream) Do(r *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	u.bodies = append(u.bodies, b)
	u.ids = append(u.ids, id)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2}}}` + "\n\n"))}, nil
}
func (u *antigravityNativeDispatchUpstream) DoWithTLS(r *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, p, id, n)
}

// 请求经过真实 Key 认证、复合/智能选组、Gemini handler、Antigravity 适配器和使用记录。
func TestGeminiAntigravityCompositeNativeDispatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"single", "composite", "smart"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Default.RateMultiplier = 1
			repo := &failoverStressAccounts{accounts: map[int64]service.Account{7101: {ID: 7101, Platform: service.PlatformAntigravity, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{1}, Credentials: map[string]any{"access_token": "test-only", "project_id": "test-project", "model_mapping": map[string]any{"friendly": "gemini-4-argon-high", "gemini-fake": "claude-opus-4-6"}, "model_whitelist": []any{"gemini-4-argon-high", "claude-opus-4-6"}}}}, groups: map[int64][]int64{1: {7101}}}
			// 同一公开别名在更优先账号上指向 Claude，必须在选号阶段跳过，不能先返回400。
			good := repo.accounts[7101]
			good.Priority = 2
			repo.accounts[7101] = good
			repo.accounts[7100] = service.Account{ID: 7100, Platform: service.PlatformAntigravity, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 1, GroupIDs: []int64{1}, Credentials: map[string]any{"access_token": "test-only-claude", "project_id": "test-project", "model_mapping": map[string]any{"friendly": "claude-opus-4-6"}}}
			repo.groups[1] = []int64{7100, 7101}
			groups := &failoverGeminiStressGroups{group: service.Group{ID: 1, Platform: service.PlatformAntigravity, Status: service.StatusActive, Hydrated: true, RateMultiplier: 1}}
			key := service.APIKey{ID: 1, Key: "sk-local-ag-native", UserID: 2, User: &service.User{ID: 2, Status: service.StatusActive, Balance: 100}, Status: service.StatusActive, IsComposite: mode == "composite", SmartRouting: mode == "smart", BillingMode: service.APIKeyBillingModeBalance, CompositeGroups: []service.APIKeyCompositeGroup{{GroupID: 1, Group: &groups.group, Prefix: "ag", NormalizedPrefix: "ag"}}}
			if mode == "single" {
				key.GroupID, key.Group = &groups.group.ID, &groups.group
				key.CompositeGroups = nil
			}
			encoded, _ := json.Marshal(key)
			keys := service.NewAPIKeyService(&routingConcurrentKeys{snapshots: map[string][]byte{key.Key: encoded}}, nil, nil, nil, nil, nil, cfg)
			upstream := &antigravityNativeDispatchUpstream{}
			usage := &routingConcurrentUsage{}
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billing.Stop)
			concurrency := service.NewConcurrencyService(nil)
			channels := newGatewayModelsChannelServiceForTest(1, service.PlatformAntigravity, service.Channel{ID: 1, Status: service.StatusActive, ModelMapping: map[string]map[string]string{service.PlatformAntigravity: {"channel-alias": "friendly"}}})
			gateway := service.NewGatewayService(repo, groups, usage, nil, nil, nil, nil, nil, cfg, nil, concurrency, service.NewBillingService(cfg, nil), nil, billing, nil, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, channels, nil, nil, nil)
			ag := service.NewAntigravityGatewayService(repo, nil, nil, service.NewAntigravityTokenProvider(nil, nil, nil), nil, upstream, service.NewSettingService(nil, cfg), nil)
			h := NewGatewayHandler(gateway, nil, nil, ag, nil, concurrency, billing, nil, keys, nil, nil, nil, nil, cfg, nil)
			router := gin.New()
			router.Use(middleware.Recovery())
			router.Use(h.SmartRoutingAuth(gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg))))
			router.POST("/v1beta/models/*modelAction", h.GeminiV1BetaModels)
			name := "channel-alias"
			if mode == "composite" {
				name = "ag/" + name
			}
			r := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+name+":generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
			r.Header.Set("Authorization", "Bearer "+key.Key)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "ok")
			require.Equal(t, []int64{7101}, upstream.ids)
			require.Equal(t, "gemini-4-argon-high", gjson.GetBytes(upstream.bodies[0], "model").String())
			require.Len(t, usage.snapshot(), 1)
		})
	}
}
