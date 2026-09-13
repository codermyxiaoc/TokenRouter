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
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/openai_compat"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const routingChatRole = "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n"
const routingChatText = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"visible-output\"}}]}\n\n"
const routingChatFinish = "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"

// 验证真实协议桥的流内失败经过智能重试后，客户端终态与 Ops 采集保持一致。
// 仅用本地 HTTP 上游和内存仓储，不运行付费请求或数据库写入。
func TestSmartRoutingChatStreamFailureAndOps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scenario := range []string{"stream_error_then_success", "empty_then_success", "all_stream_errors", "partial_then_error", "multiline_error_then_success", "flat_error_then_success", "partial_multiline_error", "partial_flat_error"} {
		t.Run(scenario, func(t *testing.T) {
			partial := strings.HasPrefix(scenario, "partial_")
			setupOpsErrorLogTestQueue(t, 8)
			key := &service.APIKey{
				ID: 901, UserID: 902, Key: "sk-local-stream-routing", Status: service.StatusActive,
				SmartRouting: true, SmartRoutingCooldownSeconds: 60, BillingMode: service.APIKeyBillingModeBalance,
				User: &service.User{ID: 902, Status: service.StatusActive, Balance: 100},
			}
			upstream := &encryptedRoutingLocalUpstream{allowed: make(map[string]bool)}
			accounts := &encryptedRoutingAccountRepo{accounts: make(map[int64]*service.Account)}
			var visits []int64
			for index := range 3 {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1/chat/completions" {
						t.Errorf("模拟上游收到错误端点: %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					if scenario != "all_stream_errors" && index > 0 {
						_, _ = io.WriteString(w, routingChatRole+routingChatText+routingChatFinish)
						return
					}
					if scenario == "empty_then_success" {
						_, _ = io.WriteString(w, routingChatRole)
						return
					}
					_, _ = io.WriteString(w, routingChatRole)
					if partial {
						_, _ = io.WriteString(w, routingChatText)
					}
					status := []int{503, 429, 524}[index]
					// 多行 data 和 event 命名错误须经过同一真实桥、协调器和 Ops 队列。
					if strings.Contains(scenario, "multiline") {
						_, _ = fmt.Fprintf(w, "event: error\ndata: {\ndata: \"error\":{\"type\":\"server_error\",\"status\":%d,\"message\":\"local failure %d\"}\ndata: }\n\ndata: [DONE]\n\n", status, index+1)
						return
					}
					if strings.Contains(scenario, "flat") {
						_, _ = fmt.Fprintf(w, "event: error\ndata: {\"type\":\"server_error\",\"status\":%d,\"message\":\"local failure %d\"}\n\ndata: [DONE]\n\n", status, index+1)
						return
					}
					_, _ = fmt.Fprintf(w, "event: error\ndata: {\"error\":{\"type\":\"server_error\",\"status\":%d,\"message\":\"local failure %d\"}}\n\ndata: [DONE]\n\n", status, index+1)
				}))
				t.Cleanup(server.Close)
				upstream.allowed[server.URL] = true
				id := int64(index + 1)
				group := &service.Group{ID: id, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
				key.CompositeGroups = append(key.CompositeGroups, service.APIKeyCompositeGroup{GroupID: id, Group: group})
				accounts.accounts[id] = &service.Account{
					ID: id + 100, Name: fmt.Sprintf("local-%d", id), Platform: service.PlatformOpenAI,
					Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
					Credentials: map[string]any{"api_key": "local-only", "base_url": server.URL, "model_mapping": map[string]any{"gpt-6-astra": "gpt-6-astra"}},
					Extra:       map[string]any{openai_compat.ExtraKeyTextRouteMode: string(openai_compat.TextRouteModeForceChatCompletions)},
				}
			}
			cfg := &config.Config{RunMode: config.RunModeStandard, Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{AllowInsecureHTTP: true}}}
			cache := &encryptedRoutingCooldownCache{until: make(map[[2]int64]time.Time)}
			forward := service.NewOpenAIGatewayService(accounts, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			gateway := service.NewGatewayService(accounts, nil, nil, nil, nil, nil, nil, cache, cfg, nil, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			resolver := service.NewSmartRoutingService(gateway, forward)
			keys := service.NewAPIKeyService(&encryptedRoutingKeyRepo{key: key}, nil, nil, nil, nil, nil, cfg)
			handler := &OpenAIGatewayHandler{gatewayService: forward, cfg: cfg}
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)))
			router.Use(middleware.WithSmartRoutingResolver(resolver, gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg)), func(*gin.Context) bool { return true }))
			router.POST("/v1/responses", func(c *gin.Context) {
				selected, ok := middleware.GetAPIKeyFromContext(c)
				require.True(t, ok)
				visits = append(visits, *selected.GroupID)
				account := accounts.accounts[*selected.GroupID]
				c.Set(opsAccountIDKey, account.ID)
				c.Set(opsStreamKey, true)
				c.Set(opsModelKey, "gpt-6-astra")
				body, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				before := c.Writer.Size()
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
					// 对齐真实 handler 在部分结果进入用量队列前的禁止重放标记。
					service.MarkSmartRoutingAttemptNonReplayable(c.Request.Context(), "partial_usage_record")
				}
			})
			call := func() *httptest.ResponseRecorder {
				request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-6-astra","stream":true,"input":"hello"}`))
				request.Header.Set("Authorization", "Bearer "+key.Key)
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				return response
			}
			first := call()
			switch scenario {
			case "all_stream_errors":
				require.Equal(t, []int64{1, 2, 3}, visits)
				require.GreaterOrEqual(t, first.Code, 400)
				require.Contains(t, first.Body.String(), "error")
				require.NotContains(t, first.Body.String(), "response.completed")
			case "partial_then_error", "partial_multiline_error", "partial_flat_error":
				require.Equal(t, []int64{1}, visits)
				require.Equal(t, 200, first.Code)
				require.Contains(t, first.Body.String(), "visible-output")
				require.Contains(t, first.Body.String(), "response.failed")
				require.Equal(t, 1, strings.Count(first.Body.String(), "event: response.failed\n"), "service 与 handler 不能重复追加终态")
				require.NotContains(t, first.Body.String(), "response.completed")
			default:
				require.Equal(t, []int64{1, 2}, visits)
				require.Equal(t, 200, first.Code)
				require.Contains(t, first.Body.String(), "visible-output")
				require.Contains(t, first.Body.String(), "response.completed")
				require.NotContains(t, first.Body.String(), "local failure")
			}
			// 无论最终被下一组恢复还是返回失败，Ops 都必须收到真实上游异常。
			require.Len(t, opsErrorLogQueue, 1)
			job := <-opsErrorLogQueue
			require.NotNil(t, job.entry.UpstreamStatusCode)
			// 队列入队时已脱敏并序列化事件，断言最终可持久化内容而不是临时指针。
			require.NotNil(t, job.entry.UpstreamErrorsJSON)
			var recordedEvents []service.OpsUpstreamErrorEvent
			require.NoError(t, json.Unmarshal([]byte(*job.entry.UpstreamErrorsJSON), &recordedEvents))
			require.NotEmpty(t, recordedEvents)
			if scenario == "all_stream_errors" {
				require.Equal(t, 524, *job.entry.UpstreamStatusCode)
				require.Len(t, recordedEvents, 3)
			} else if partial {
				require.GreaterOrEqual(t, job.entry.StatusCode, 400)
				require.Equal(t, 503, *job.entry.UpstreamStatusCode)
			}
			ttl, err := resolver.GetSmartRoutingCooldown(context.Background(), key.ID, 1)
			require.NoError(t, err)
			require.Positive(t, ttl)
			visits = nil
			second := call()
			if scenario == "all_stream_errors" {
				require.Equal(t, 503, second.Code)
				require.Contains(t, second.Body.String(), "SMART_ROUTING_ALL_GROUPS_COOLING")
				require.Empty(t, visits)
			} else {
				require.Equal(t, 200, second.Code)
				require.Equal(t, []int64{2}, visits)
			}
		})
	}
}
