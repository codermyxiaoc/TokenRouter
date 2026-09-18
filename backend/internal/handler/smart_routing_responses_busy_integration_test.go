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
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const responsesBusyEvent = "event: error\ndata: {\"error\":{\"code\":\"server_error\",\"message\":\"The service is busy. Please retry later.\",\"request_id\":\"upstream-busy-attempt\",\"type\":\"server_error\"},\"request_id\":\"upstream-busy-attempt\",\"sequence_number\":2,\"type\":\"error\"}\n\n"
const responsesBusyText = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"visible-response\"}\n\n"
const responsesBusyComplete = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"response-success\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"visible-response\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":3}}}\n\n"

// 并发测试的仓储每次返回独立实体，模拟数据库读取而不是共享可变认证对象。
type responsesBusyKeyRepo struct {
	service.APIKeyRepository
	key      string
	snapshot []byte
}

func (r *responsesBusyKeyRepo) GetByKeyForAuth(_ context.Context, value string) (*service.APIKey, error) {
	if value != r.key {
		return nil, service.ErrAPIKeyNotFound
	}
	var key service.APIKey
	if err := json.Unmarshal(r.snapshot, &key); err != nil {
		return nil, err
	}
	return &key, nil
}

func (r *responsesBusyKeyRepo) UpdateLastUsed(context.Context, int64, time.Time) error { return nil }

// 通过真实原生 Responses 转发、认证选组、缓冲写入器和 Ops 队列验证 busy 恢复边界。
// 数据库与上游只使用内存仓储和本机模拟服务，不向生产服务发请求。
func TestSmartRoutingResponsesBusyRecoveryIntegration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scenario := range []string{"bare_error", "queue_heartbeat", "empty_delta", "partial_output", "all_groups_fail", "concurrent_heartbeats", "third_group_recovers", "upstream_400", "upstream_401", "upstream_403", "upstream_404"} {
		t.Run(scenario, func(t *testing.T) {
			concurrent := scenario == "concurrent_heartbeats"
			recoveredGroupID := int64(2)
			upstreamStatus := map[string]int{"upstream_400": 400, "upstream_401": 401, "upstream_403": 403, "upstream_404": 404}[scenario]
			if scenario == "third_group_recovers" || upstreamStatus > 0 {
				recoveredGroupID = 3
			}
			setupOpsErrorLogTestQueue(t, 128)
			key := &service.APIKey{
				ID: 1901, UserID: 1902, Key: "sk-local-responses-busy", Status: service.StatusActive,
				SmartRouting: true, SmartRoutingCooldownSeconds: 60, BillingMode: service.APIKeyBillingModeBalance,
				User: &service.User{ID: 1902, Status: service.StatusActive, Balance: 100},
			}
			if concurrent {
				// 并发专项关闭跨请求冷却，确保每一个请求都实际经历两组转发与独立心跳计数。
				key.SmartRoutingCooldownSeconds = 0
			}
			upstream := &encryptedRoutingLocalUpstream{allowed: make(map[string]bool)}
			accounts := &encryptedRoutingAccountRepo{accounts: make(map[int64]*service.Account)}
			for index := range 3 {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1/responses" {
						t.Errorf("原生 Responses 不应切换端点: %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					if int64(index+1) >= recoveredGroupID && scenario != "all_groups_fail" {
						_, _ = io.WriteString(w, responsesBusyText+responsesBusyComplete)
						return
					}
					if upstreamStatus > 0 {
						// 用真实上游 4xx 验证恢复采集不会按客户端请求错误阶段丢弃记录。
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(upstreamStatus)
						errType := map[int]string{400: "invalid_request_error", 401: "authentication_error", 403: "permission_error", 404: "not_found_error"}[upstreamStatus]
						_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": errType, "message": "upstream failure before output"}})
						return
					}
					if scenario == "empty_delta" {
						_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"\"}\n\n")
					}
					if scenario == "partial_output" {
						_, _ = io.WriteString(w, responsesBusyText)
					}
					_, _ = io.WriteString(w, responsesBusyEvent)
				}))
				t.Cleanup(server.Close)
				upstream.allowed[server.URL] = true
				groupID := int64(index + 1)
				group := &service.Group{ID: groupID, Name: fmt.Sprintf("busy-group-%d", groupID), Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
				key.CompositeGroups = append(key.CompositeGroups, service.APIKeyCompositeGroup{GroupID: groupID, Group: group})
				accounts.accounts[groupID] = &service.Account{
					ID: 200 + groupID, Name: fmt.Sprintf("busy-account-%d", groupID), Platform: service.PlatformOpenAI,
					Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
					Credentials: map[string]any{"api_key": "local-only", "base_url": server.URL, "model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"}},
				}
			}
			cfg := &config.Config{RunMode: config.RunModeStandard, Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{AllowInsecureHTTP: true}}}
			cache := &encryptedRoutingCooldownCache{until: make(map[[2]int64]time.Time)}
			forward := service.NewOpenAIGatewayService(accounts, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			gateway := service.NewGatewayService(accounts, nil, nil, nil, nil, nil, nil, cache, cfg, nil, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			resolver := service.NewSmartRoutingService(gateway, forward)
			keySnapshot, err := json.Marshal(key)
			require.NoError(t, err)
			keys := service.NewAPIKeyService(&responsesBusyKeyRepo{key: key.Key, snapshot: keySnapshot}, nil, nil, nil, nil, nil, cfg)
			handler := &OpenAIGatewayHandler{gatewayService: forward, cfg: cfg}
			opsRepo := &ingressRejectOpsRepo{}
			ops := service.NewOpsService(opsRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.Use(middleware.WithSmartRoutingResolver(resolver, gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg)), func(*gin.Context) bool { return true }))
			var visits []int64
			var visitsMu sync.Mutex
			router.POST("/v1/responses", func(c *gin.Context) {
				selected, ok := middleware.GetAPIKeyFromContext(c)
				require.True(t, ok)
				groupID := *selected.GroupID
				visitsMu.Lock()
				visits = append(visits, groupID)
				visitsMu.Unlock()
				// 与生产调度仓储一样，每轮转发使用独立账号实体和凭据快照。
				accountSnapshot := *accounts.accounts[groupID]
				account := &accountSnapshot
				account.Credentials = cloneCredentialMap(account.Credentials)
				c.Set(opsAccountIDKey, account.ID)
				c.Set(opsStreamKey, true)
				c.Set(opsModelKey, "gpt-5.6-sol")
				// 每个新组的缓冲写入器必须从未写出状态开始，不能继承上一轮心跳计数。
				require.Equal(t, -1, service.OpenAICompactKeepaliveAdjustedWrittenSize(c))
				if (scenario == "queue_heartbeat" || concurrent) && groupID == 1 {
					c.Header("Content-Type", "text/event-stream")
					n, err := c.Writer.WriteString(":\n\n")
					require.NoError(t, err)
					recordGatewayStreamHeartbeat(c, n)
					c.Writer.Flush()
				}
				body, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				before := service.OpenAICompactKeepaliveAdjustedWrittenSize(c)
				result, err := forward.Forward(c.Request.Context(), c, account, body)
				if err == nil {
					require.NotNil(t, result)
					return
				}
				var failure *service.UpstreamFailoverError
				if errors.As(err, &failure) {
					handler.handleFailoverExhausted(c, failure, c.Writer.Written())
					return
				}
				if !openAIForwardErrorAlreadyCommunicated(c, before, err) {
					handler.ensureOpenAIForwardErrorResponse(c, c.Writer.Written(), err)
				}
				if result != nil {
					// 对齐真实 handler：部分结果已经进入结算生命周期后不能跨组重放。
					service.MarkSmartRoutingAttemptNonReplayable(c.Request.Context(), "partial_usage_record")
				}
			})
			call := func() *httptest.ResponseRecorder {
				request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol","stream":true,"input":"hello"}`))
				request.Header.Set("Authorization", "Bearer "+key.Key)
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				return response
			}
			requestCount := 1
			if concurrent {
				requestCount = 60
			}
			responses := make([]*httptest.ResponseRecorder, requestCount)
			if concurrent {
				var requests sync.WaitGroup
				requests.Add(requestCount)
				start := make(chan struct{})
				for index := range responses {
					go func() {
						defer requests.Done()
						<-start
						responses[index] = call()
					}()
				}
				close(start)
				requests.Wait()
			} else {
				responses[0] = call()
			}
			response := responses[0]
			switch scenario {
			case "concurrent_heartbeats":
				require.Len(t, visits, 120, "每个客户端请求只能执行两组尝试")
				counts := make(map[int64]int)
				for _, groupID := range visits {
					counts[groupID]++
				}
				require.Equal(t, map[int64]int{1: 60, 2: 60}, counts)
				for _, result := range responses {
					require.NotNil(t, result)
					require.Equal(t, http.StatusOK, result.Code)
					require.Contains(t, result.Body.String(), "visible-response")
					require.Equal(t, 1, strings.Count(result.Body.String(), "event: response.completed\n"))
					require.NotContains(t, result.Body.String(), "The service is busy")
					require.NotContains(t, result.Body.String(), "upstream-busy-attempt")
				}
			case "all_groups_fail":
				require.Equal(t, []int64{1, 2, 3}, visits)
				require.Contains(t, response.Body.String(), "error")
				require.NotContains(t, response.Body.String(), "response.completed")
			case "partial_output":
				require.Equal(t, []int64{1}, visits, "已有正文时不能拼接另一组的回答")
				require.Contains(t, response.Body.String(), "visible-response")
				require.Contains(t, response.Body.String(), "The service is busy")
				require.NotContains(t, response.Body.String(), "response.completed")
			default:
				wantVisits := []int64{1, 2}
				if recoveredGroupID == 3 {
					wantVisits = append(wantVisits, 3)
				}
				require.Equal(t, wantVisits, visits)
				require.Equal(t, http.StatusOK, response.Code)
				require.Contains(t, response.Body.String(), "visible-response")
				require.Contains(t, response.Body.String(), "response.completed")
				require.NotContains(t, response.Body.String(), "The service is busy")
				require.NotContains(t, response.Body.String(), "upstream-busy-attempt")
			}
			// 恢复成功与最终失败都必须保留失败尝试的账号、分组及原生上游端点。
			require.Len(t, opsErrorLogQueue, requestCount)
			for range requestCount {
				job := <-opsErrorLogQueue
				require.NotNil(t, job.entry.UpstreamErrorsJSON)
				var events []service.OpsUpstreamErrorEvent
				require.NoError(t, json.Unmarshal([]byte(*job.entry.UpstreamErrorsJSON), &events))
				wantEvents := 1
				if recoveredGroupID == 3 {
					wantEvents = 2
				}
				if scenario == "all_groups_fail" {
					wantEvents = 3
				}
				require.Len(t, events, wantEvents)
				for index, event := range events {
					require.Equal(t, int64(index+1), event.GroupID)
					require.Equal(t, fmt.Sprintf("busy-group-%d", index+1), event.GroupName)
					require.Equal(t, int64(index+201), event.AccountID)
					require.Equal(t, "/v1/responses", event.UpstreamEndpoint)
					// 裸 server_error 没有明确 HTTP 状态，沿用流失败策略的 502 归一化。
					wantStatus := http.StatusBadGateway
					if upstreamStatus > 0 {
						wantStatus = upstreamStatus
					}
					require.Equal(t, wantStatus, event.UpstreamStatusCode)
					if scenario != "partial_output" && scenario != "all_groups_fail" {
						require.Equal(t, recoveredGroupID, event.RecoveredGroupID)
						require.Equal(t, fmt.Sprintf("busy-group-%d", recoveredGroupID), event.RecoveredGroupName)
						require.Equal(t, service.PlatformOpenAI, event.RecoveredPlatform)
					}
				}
				require.Equal(t, "upstream", job.entry.ErrorPhase)
				if scenario != "partial_output" && scenario != "all_groups_fail" {
					require.Equal(t, http.StatusOK, job.entry.StatusCode)
					require.Contains(t, job.entry.ErrorMessage, "Recovered upstream error")
				} else {
					require.GreaterOrEqual(t, job.entry.StatusCode, 400)
				}
				// 执行真实异步批处理的落库入口，确保清洗阶段不会丢掉恢复目标快照。
				flushOpsErrorLogBatch([]opsErrorLogJob{job})
				persisted := opsRepo.entries[len(opsRepo.entries)-1]
				require.Equal(t, *job.entry.UpstreamErrorsJSON, *persisted.UpstreamErrorsJSON)
				require.Equal(t, events[len(events)-1].GroupID, *persisted.GroupID)
				if upstreamStatus > 0 {
					require.Equal(t, "upstream", persisted.ErrorPhase)
					require.Equal(t, "provider", persisted.ErrorOwner)
					require.Equal(t, upstreamStatus, *persisted.UpstreamStatusCode)
				}
			}
			if concurrent {
				require.Empty(t, cache.until, "冷却配置为 0 时不得产生跨请求冷却")
				return
			}
			left, err := resolver.GetSmartRoutingCooldown(context.Background(), key.ID, 1)
			require.NoError(t, err)
			require.Positive(t, left)
			visits = nil
			next := call()
			if scenario == "all_groups_fail" {
				require.Equal(t, http.StatusServiceUnavailable, next.Code)
				require.Contains(t, next.Body.String(), "SMART_ROUTING_ALL_GROUPS_COOLING")
				require.Empty(t, visits)
			} else {
				require.Equal(t, http.StatusOK, next.Code)
				require.Equal(t, []int64{recoveredGroupID}, visits, "冷却后的新请求必须跳过故障组")
			}
		})
	}
}
