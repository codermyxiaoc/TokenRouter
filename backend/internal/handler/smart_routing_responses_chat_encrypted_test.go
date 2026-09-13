//go:build unit

package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/openai_compat"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// encryptedRoutingAccountRepo 保留真实模型目录选组，只替代数据库读取。
type encryptedRoutingAccountRepo struct {
	service.AccountRepository
	accounts map[int64]*service.Account
}

func (r *encryptedRoutingAccountRepo) ListAllWithFilters(_ context.Context, platform, _, status, _ string, groupID int64, _ string) ([]service.Account, error) {
	if groupID <= 0 || status != "" {
		return nil, errors.New("智能目录查询必须限定分组且保留错误账号目录")
	}
	account := r.accounts[groupID]
	if account == nil || (platform != "" && account.Platform != platform) {
		return nil, nil
	}
	return []service.Account{*account}, nil
}

// encryptedRoutingKeyRepo 让请求经过生产认证流程，不直接注入已认证上下文。
type encryptedRoutingKeyRepo struct {
	service.APIKeyRepository
	key *service.APIKey
}

func (r *encryptedRoutingKeyRepo) GetByKeyForAuth(_ context.Context, value string) (*service.APIKey, error) {
	if value != r.key.Key {
		return nil, service.ErrAPIKeyNotFound
	}
	return r.key, nil
}

func (r *encryptedRoutingKeyRepo) UpdateLastUsed(context.Context, int64, time.Time) error { return nil }

// encryptedRoutingCooldownCache 仅替代缓存存储，冷却读写仍走生产 SmartRoutingService。
type encryptedRoutingCooldownCache struct {
	service.GatewayCache
	mu    sync.Mutex
	until map[[2]int64]time.Time
}

func (c *encryptedRoutingCooldownCache) GetSmartRoutingCooldown(_ context.Context, keyID, groupID int64) (time.Duration, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	left := time.Until(c.until[[2]int64{keyID, groupID}])
	if left < 0 {
		left = 0
	}
	return left, nil
}

func (c *encryptedRoutingCooldownCache) CooldownSmartRoutingGroup(_ context.Context, keyID, groupID int64, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := [2]int64{keyID, groupID}
	until := time.Now().Add(ttl)
	if until.After(c.until[id]) {
		c.until[id] = until
	}
	return nil
}

// encryptedRoutingLocalUpstream 严格限制请求到本测试创建的三个本地 HTTP 服务。
type encryptedRoutingLocalUpstream struct {
	service.HTTPUpstream
	allowed map[string]bool
}

func (u *encryptedRoutingLocalUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if !u.allowed[req.URL.Scheme+"://"+req.URL.Host] {
		return nil, fmt.Errorf("测试禁止访问非本地模拟上游: %s", req.URL.Host)
	}
	return http.DefaultClient.Do(req)
}

func (u *encryptedRoutingLocalUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

// 本用例覆盖真实鉴权、目录选组、冷却服务、Responses→Chat 转发和 handler 最终错误映射。
// 账号并发及用量持久化由各自专项测试覆盖；这里每个组固定一个账号，避免数据库及付费上游依赖。
func TestSmartRoutingResponsesChatEncryptedHistoryRetriesAndCools(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls [3]atomic.Int32
	upstream := &encryptedRoutingLocalUpstream{allowed: make(map[string]bool)}
	accounts := &encryptedRoutingAccountRepo{accounts: make(map[int64]*service.Account)}
	key := &service.APIKey{
		ID: 901, UserID: 902, Key: "sk-local-encrypted-routing", Status: service.StatusActive,
		SmartRouting: true, SmartRoutingCooldownSeconds: 60, BillingMode: service.APIKeyBillingModeBalance,
		User: &service.User{ID: 902, Status: service.StatusActive, Balance: 100},
	}
	for index := range calls {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls[index].Add(1)
			body, err := io.ReadAll(r.Body)
			if err != nil || r.URL.Path != "/v1/chat/completions" || gjson.GetBytes(body, "model").String() != "gpt-6-astra" || !gjson.GetBytes(body, "stream").Bool() {
				t.Errorf("本地上游请求不符合 Chat 转换契约: path=%s body=%s err=%v", r.URL.Path, body, err)
			}
			if index != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"error":{"message":"local upstream temporarily unavailable","type":"server_error"}}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-local\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-6-astra\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n"+
				"data: {\"id\":\"chatcmpl-local\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-6-astra\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"second-group-success\"}}]}\n\n"+
				"data: {\"id\":\"chatcmpl-local\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-6-astra\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":3,\"total_tokens\":13}}\n\ndata: [DONE]\n\n")
		}))
		t.Cleanup(server.Close)
		upstream.allowed[server.URL] = true
		groupID := int64(index + 1)
		group := &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
		key.CompositeGroups = append(key.CompositeGroups, service.APIKeyCompositeGroup{GroupID: groupID, Group: group})
		accounts.accounts[groupID] = &service.Account{
			ID: groupID + 100, Name: fmt.Sprintf("test%d", groupID), Platform: service.PlatformOpenAI,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
			Credentials: map[string]any{"api_key": "sk-local-only", "base_url": server.URL, "model_mapping": map[string]any{"gpt-6-astra": "gpt-6-astra"}},
			Extra:       map[string]any{openai_compat.ExtraKeyTextRouteMode: string(openai_compat.TextRouteModeForceChatCompletions)},
		}
	}
	cfg := &config.Config{RunMode: config.RunModeStandard, Security: config.SecurityConfig{
		URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true},
	}}
	cache := &encryptedRoutingCooldownCache{until: make(map[[2]int64]time.Time)}
	forward := service.NewOpenAIGatewayService(accounts, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	gateway := service.NewGatewayService(accounts, nil, nil, nil, nil, nil, nil, cache, cfg, nil, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	resolver := service.NewSmartRoutingService(gateway, forward)
	keys := service.NewAPIKeyService(&encryptedRoutingKeyRepo{key: key}, nil, nil, nil, nil, nil, cfg)
	auth := gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg))
	handler := &OpenAIGatewayHandler{gatewayService: forward, cfg: cfg}
	router := gin.New()
	router.Use(middleware.WithSmartRoutingResolver(resolver, auth, func(*gin.Context) bool { return true }))
	var attempts []int64
	var mappedStatuses []int
	router.POST("/v1/responses", func(c *gin.Context) {
		selected, ok := middleware.GetAPIKeyFromContext(c)
		require.True(t, ok)
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.Equal(t, "opaque-local-history", gjson.GetBytes(body, "input.0.encrypted_content").String(), "换组不得修改客户端加密历史")
		attempts = append(attempts, *selected.GroupID)
		result, err := forward.Forward(c.Request.Context(), c, accounts.accounts[*selected.GroupID], body)
		if err != nil {
			var failure *service.UpstreamFailoverError
			require.ErrorAs(t, err, &failure)
			require.Equal(t, http.StatusServiceUnavailable, failure.StatusCode)
			require.Nil(t, result)
			require.False(t, c.Writer.Written(), "上游 HTTP 错误不能提前提交客户端响应")
			handler.handleFailoverExhausted(c, failure, false)
			mappedStatuses = append(mappedStatuses, c.Writer.Status())
			return
		}
		require.NotNil(t, result)
		require.Equal(t, 3, result.Usage.OutputTokens)
	})
	body := `{"model":"gpt-6-astra","stream":true,"input":[{"type":"reasoning","id":"rs_local","encrypted_content":"opaque-local-history","summary":[{"type":"summary_text","text":"local history summary"}]},{"role":"user","content":"hello"}]}`
	call := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+key.Key)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	first := call()
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Contains(t, first.Body.String(), "second-group-success")
	require.Contains(t, first.Body.String(), "response.completed")
	require.NotContains(t, first.Body.String(), "Upstream service temporarily unavailable")
	require.Equal(t, []int64{1, 2}, attempts)
	require.Equal(t, []int{http.StatusBadGateway}, mappedStatuses, "原始 503 经真实 handler 映射为 502 后仍应换组")
	remaining, err := resolver.GetSmartRoutingCooldown(context.Background(), key.ID, 1)
	require.NoError(t, err)
	require.Positive(t, remaining)
	second := call()
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.Contains(t, second.Body.String(), "second-group-success")
	require.Equal(t, []int64{1, 2, 2}, attempts)
	require.Equal(t, int32(1), calls[0].Load())
	require.Equal(t, int32(2), calls[1].Load())
	require.Zero(t, calls[2].Load(), "第二组成功后不能继续调用第三组")
}
