//go:build unit

package handler

import (
	"context"
	"encoding/json"
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
	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// routingFocusBillingSink 仅观察结算入参；余额事务与持久幂等由真实数据库专项验证。
// 此处不模拟去重逻辑，任何多余的结算调用都会被计数断言捕获。
type routingFocusBillingSink struct {
	service.UsageBillingRepository
	mu       sync.Mutex
	commands []service.UsageBillingCommand
}

func (s *routingFocusBillingSink) Apply(_ context.Context, command *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, *command)
	rate := command.BalanceRateMultiplier
	return &service.UsageBillingApplyResult{
		Applied: true, BalanceAmountUSD: command.BillableAmountUSD, EffectiveRateMultiplier: &rate,
	}, nil
}

// routingFocusUsageSink 保存实际 RecordUsage 生成的使用行，核对最终分组与倍率归属。
type routingFocusUsageSink struct {
	service.UsageLogRepository
	mu   sync.Mutex
	logs []service.UsageLog
}

func (s *routingFocusUsageSink) CreateBestEffort(_ context.Context, usage *service.UsageLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, *usage)
	return nil
}

// TestRoutingFocusForwardThenBillSelectedGroup 将真实鉴权、选组、HTTP 转发和定价结算串联。
// 故障分组倍率刻意高于成功分组，避免仅验证状态码而遗漏错误归属或重复扣费。
func TestRoutingFocusForwardThenBillSelectedGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scenario := range []struct {
		name     string
		status   int
		requests int
		allFail  bool
	}{
		{name: "http_400", status: 400, requests: 1},
		{name: "http_401", status: 401, requests: 1},
		{name: "http_403", status: 403, requests: 1},
		{name: "http_404", status: 404, requests: 1},
		{name: "http_429", status: 429, requests: 1},
		{name: "http_500", status: 500, requests: 1},
		{name: "http_502", status: 502, requests: 1},
		{name: "http_503", status: 503, requests: 1},
		{name: "http_524", status: 524, requests: 1},
		{name: "sixty_concurrent_recoveries", status: 503, requests: 60},
		{name: "all_failed_no_usage", status: 503, requests: 1, allFail: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var upstreamCalls [3]atomic.Int32
			upstream := &encryptedRoutingLocalUpstream{allowed: make(map[string]bool)}
			accounts := &encryptedRoutingAccountRepo{accounts: make(map[int64]*service.Account)}
			key := &service.APIKey{
				ID: 8901, UserID: 8902, Key: "sk-local-routing-billing-only", Status: service.StatusActive,
				SmartRouting: true, BillingMode: service.APIKeyBillingModeBalance,
				User: &service.User{ID: 8902, Status: service.StatusActive, Balance: 100},
			}
			// 关闭跨请求冷却，确保 60 个请求都穿过三个候选，而不是只测试缓存绕过。
			for index, multiplier := range []float64{11, 7, 0.25} {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					upstreamCalls[index].Add(1)
					if request.URL.Path != "/v1/responses" {
						t.Errorf("上游端点发生意外变化: %s", request.URL.Path)
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_, _ = io.Copy(io.Discard, request.Body)
					if index < 2 || scenario.allFail {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(scenario.status)
						_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"local simulated upstream failure"}}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"routing-billing-success\"}\n\n"+
						"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"same-upstream-response-id\",\"status\":\"completed\",\"service_tier\":\"default\",\"model\":\"gpt-5.4\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"routing-billing-success\"}]}],\"usage\":{\"input_tokens\":1000,\"output_tokens\":200}}}\n\n")
				}))
				t.Cleanup(server.Close)
				upstream.allowed[server.URL] = true
				groupID := int64(index + 1)
				group := &service.Group{ID: groupID, Name: fmt.Sprintf("billing-group-%d", groupID), Platform: service.PlatformOpenAI,
					Status: service.StatusActive, Hydrated: true, RateMultiplier: multiplier}
				key.CompositeGroups = append(key.CompositeGroups, service.APIKeyCompositeGroup{GroupID: groupID, Group: group})
				accounts.accounts[groupID] = &service.Account{
					ID: 9000 + groupID, Name: group.Name, Platform: service.PlatformOpenAI,
					Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
					Credentials: map[string]any{"api_key": "local-only", "base_url": server.URL,
						"model_mapping": map[string]any{"gpt-5.4": "gpt-5.4"}},
					Extra: map[string]any{"openai_text_route_mode": "force_responses"},
				}
			}
			cfg := &config.Config{RunMode: config.RunModeStandard, Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{AllowInsecureHTTP: true}}}
			cfg.Default.RateMultiplier = 1
			billing := &routingFocusBillingSink{}
			usage := &routingFocusUsageSink{}
			cache := &encryptedRoutingCooldownCache{until: make(map[[2]int64]time.Time)}
			forward := service.NewOpenAIGatewayService(accounts, usage, billing, nil, nil, nil, nil, cfg, nil, nil,
				service.NewBillingService(cfg, nil), nil, nil, upstream, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
			gateway := service.NewGatewayService(accounts, nil, nil, nil, nil, nil, nil, cache, cfg, nil, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			resolver := service.NewSmartRoutingService(gateway, forward)
			snapshot, err := json.Marshal(key)
			require.NoError(t, err)
			keys := service.NewAPIKeyService(&responsesBusyKeyRepo{key: key.Key, snapshot: snapshot}, nil, nil, nil, nil, nil, cfg)
			pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
				WorkerCount: 2, QueueSize: 128, TaskTimeout: 10 * time.Second, OverflowPolicy: config.UsageRecordOverflowPolicySync,
			})
			t.Cleanup(pool.Stop)
			settle := make(chan struct{})
			recordErrors := make(chan error, scenario.requests)
			errorHandler := &OpenAIGatewayHandler{gatewayService: forward, cfg: cfg, usageRecordWorkerPool: pool}
			router := gin.New()
			router.Use(middleware.WithSmartRoutingResolver(resolver, gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg)), func(*gin.Context) bool { return true }))
			router.POST("/v1/responses", func(c *gin.Context) {
				selected, ok := middleware.GetAPIKeyFromContext(c)
				require.True(t, ok)
				account := *accounts.accounts[*selected.GroupID]
				account.Credentials = cloneCredentialMap(account.Credentials)
				body, readErr := io.ReadAll(c.Request.Body)
				require.NoError(t, readErr)
				before := service.OpenAICompactKeepaliveAdjustedWrittenSize(c)
				result, forwardErr := forward.Forward(c.Request.Context(), c, &account, body)
				if forwardErr == nil {
					require.NotNil(t, result)
					// 先让全部请求结束再结算，验证异步任务不会读取到被换组或复用的 Gin 上下文。
					errorHandler.submitOpenAIUsageRecordTask(c, result, func(ctx context.Context) {
						<-settle
						recordErrors <- forward.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
							Result: result, APIKey: selected, User: selected.User, Account: &account,
							RequestBody: body, RequestPayloadHash: "identical-input-hash", InboundEndpoint: "/v1/responses", UpstreamEndpoint: "/v1/responses",
						})
					})
					return
				}
				// 首字前 HTTP 失败必须没有可结算结果，不能先写零费用账单再占用幂等键。
				require.Nil(t, result)
				var failure *service.UpstreamFailoverError
				if errors.As(forwardErr, &failure) {
					errorHandler.handleFailoverExhausted(c, failure, c.Writer.Written())
				} else if !openAIForwardErrorAlreadyCommunicated(c, before, forwardErr) {
					errorHandler.ensureOpenAIForwardErrorResponse(c, c.Writer.Written(), forwardErr)
				}
			})
			responses := make([]*httptest.ResponseRecorder, scenario.requests)
			start := make(chan struct{})
			var pending sync.WaitGroup
			for index := range responses {
				pending.Add(1)
				go func() {
					defer pending.Done()
					<-start
					request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4","service_tier":"default","stream":true,"input":"hello"}`))
					request.Header.Set("Authorization", "Bearer "+key.Key)
					request.Header.Set("Content-Type", "application/json")
					request = request.WithContext(context.WithValue(request.Context(), ctxkey.RequestID, fmt.Sprintf("routing-focus-%d", index)))
					responses[index] = httptest.NewRecorder()
					router.ServeHTTP(responses[index], request)
				}()
			}
			close(start)
			pending.Wait()
			close(settle)
			pool.Stop()
			close(recordErrors)
			for recordErr := range recordErrors {
				require.NoError(t, recordErr)
			}
			require.Zero(t, pool.Stats().DroppedQueueFull)
			for index := range upstreamCalls {
				require.EqualValues(t, scenario.requests, upstreamCalls[index].Load(), "每个分组每次请求只允许访问一次")
			}
			if scenario.allFail {
				require.Empty(t, billing.commands)
				require.Empty(t, usage.logs)
				require.GreaterOrEqual(t, responses[0].Code, 400)
				return
			}
			require.Len(t, billing.commands, scenario.requests, "失败尝试不得提交额外账单")
			require.Len(t, usage.logs, scenario.requests)
			identities := make(map[string]bool)
			for _, command := range billing.commands {
				require.NotNil(t, command.GroupID)
				require.Equal(t, int64(3), *command.GroupID)
				require.Equal(t, int64(9003), command.AccountID)
				require.Equal(t, service.APIKeyBillingModeBalance, command.APIKeyBillingMode)
				require.Equal(t, 0.25, command.BalanceRateMultiplier)
				// 固定目录价：1000×0.0000025 + 200×0.000015 = 0.0055，再乘最终组 0.25。
				require.InDelta(t, 0.0055, command.BaseAmountUSD, 1e-12)
				require.InDelta(t, 0.001375, command.BillableAmountUSD, 1e-12)
				require.False(t, identities[command.RequestID], "不同请求不能因相同上游响应 ID 被合并")
				identities[command.RequestID] = true
				require.True(t, strings.HasPrefix(command.RequestID, "local:routing-focus-"))
			}
			for _, log := range usage.logs {
				require.Equal(t, int64(3), *log.GroupID)
				require.Equal(t, int64(9003), log.AccountID)
				require.Equal(t, 0.25, log.RateMultiplier)
				require.InDelta(t, 0.001375, log.ActualCost, 1e-12)
			}
			for _, response := range responses {
				require.Equal(t, http.StatusOK, response.Code)
				require.Contains(t, response.Body.String(), "routing-billing-success")
				require.Equal(t, 1, strings.Count(response.Body.String(), "event: response.completed\n"))
				require.NotContains(t, response.Body.String(), "local simulated upstream failure")
			}
		})
	}
}
