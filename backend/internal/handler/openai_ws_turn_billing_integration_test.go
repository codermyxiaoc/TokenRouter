package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/TokenFlux/TokenRouter/internal/testutil"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 只替换认证仓储，保留真实认证快照、WebSocket handler、转发及用量计算。
type wsTurnMutableKeyRepo struct {
	service.APIKeyRepository
	mu  sync.Mutex
	key *service.APIKey
}

func (r *wsTurnMutableKeyRepo) GetByKeyForAuth(context.Context, string) (*service.APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *r.key
	return &copy, nil
}

func (r *wsTurnMutableKeyRepo) set(key *service.APIKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.key = key
}

// 同一真实 WS 连接发送两轮，确认新价进入第二轮用量、第一轮和付款身份不变。
// 使用简单模式隔离持久扣款，仅验证本改动的 handler 到 RecordUsage 集成边界。
func TestOpenAIWSTurnBillingHandlerTwoTurns(t *testing.T) {
	for _, mode := range []string{"ordinary", "composite", "smart"} {
		t.Run(mode, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			upstreamErr := make(chan error, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					upstreamErr <- err
					return
				}
				defer conn.CloseNow()
				for turn := 1; turn <= 2; turn++ {
					ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
					_, _, err = conn.Read(ctx)
					if err == nil {
						err = conn.Write(ctx, coderws.MessageText, []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_price_%d","model":"gpt-5.4","usage":{"input_tokens":100,"output_tokens":100}}}`, turn)))
					}
					cancel()
					if err != nil {
						upstreamErr <- err
						return
					}
				}
				upstreamErr <- nil
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				_, _, _ = conn.Read(ctx)
			}))
			defer upstream.Close()

			cfg := &config.Config{}
			cfg.RunMode = config.RunModeSimple
			cfg.Default.RateMultiplier = 1
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			cfg.Gateway.OpenAIWS.Enabled = true
			cfg.Gateway.OpenAIWS.APIKeyEnabled = true
			cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
			cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
			cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 5
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 5
			cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 5
			group := &service.Group{ID: 4201, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, RateMultiplier: 3}
			connKey := &service.APIKey{ID: 1801, Key: "test-ws-price", UserID: 1701, Status: service.StatusActive,
				GroupID: &group.ID, Group: group, User: &service.User{ID: 1701, Status: service.StatusActive},
				IsComposite: mode == "composite", SmartRouting: mode == "smart", BillingMode: service.APIKeyBillingModeBalance}
			latest := *connKey
			latestGroup := *group
			latestGroup.RateMultiplier = 0.3
			latest.Group = &latestGroup
			if mode != "ordinary" {
				latest.Group, latest.GroupID = nil, nil
				latest.CompositeGroups = []service.APIKeyCompositeGroup{{GroupID: group.ID, Group: &latestGroup, Prefix: "group", NormalizedPrefix: "group"}}
			}
			keyRepo := &wsTurnMutableKeyRepo{key: connKey}
			keyService := service.NewAPIKeyService(keyRepo, nil, nil, nil, nil, nil, cfg)
			accountRepo := &openAIWSUsageHandlerAccountRepoStub{account: service.Account{
				ID: 9901, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"api_key": "test-upstream", "base_url": upstream.URL},
				Extra:       map[string]any{"openai_apikey_responses_websockets_v2_enabled": true, "openai_apikey_responses_websockets_v2_mode": service.OpenAIWSIngressModePassthrough},
			}}
			usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 2)}
			billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			gateway := service.NewOpenAIGatewayService(accountRepo, usageRepo, nil, nil, nil, nil, testutil.NewRedisGatewayCache(t), cfg, nil, nil,
				service.NewBillingService(cfg, nil), nil, billingCache, nil, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
			concurrency := &concurrencyCacheMock{
				acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
				acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
			}
			h := &OpenAIGatewayHandler{gatewayService: gateway, billingCacheService: billingCache, apiKeyService: keyService,
				concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(concurrency), SSEPingFormatNone, time.Second)}
			done := make(chan struct{})
			router := gin.New()
			router.GET("/v1/responses", func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), connKey)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: connKey.User.ID, Concurrency: 1})
				h.ResponsesWebSocket(c)
				close(done)
			})
			server := httptest.NewServer(router)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			require.NoError(t, err)
			defer client.CloseNow()
			var costs []float64
			for turn, wantRate := range []float64{3, 0.3} {
				if turn == 1 {
					keyRepo.set(&latest)
				}
				require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.4","input":"hello"}`)))
				_, _, err = client.Read(ctx)
				require.NoError(t, err)
				select {
				case usage := <-usageRepo.created:
					require.Equal(t, connKey.ID, usage.APIKeyID)
					require.Equal(t, connKey.User.ID, usage.UserID)
					require.Equal(t, group.ID, *usage.GroupID)
					require.InDelta(t, wantRate, usage.RateMultiplier, 1e-12)
					require.Positive(t, usage.ActualCost)
					costs = append(costs, usage.ActualCost)
				case <-ctx.Done():
					t.Fatal("未记录本轮用量")
				}
			}
			require.InDelta(t, costs[0]/10, costs[1], 1e-12)
			require.InDelta(t, 3, connKey.Group.RateMultiplier, 1e-12)
			require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("handler 未退出")
			}
			require.NoError(t, <-upstreamErr)
		})
	}
}
