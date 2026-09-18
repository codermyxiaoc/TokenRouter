//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 完整 handler 测试只替换 HTTP 传输，并记录生产重试循环实际选择的账号。
type busyFailoverLoopUpstream struct {
	service.HTTPUpstream
	mu       sync.Mutex
	hits     []int64
	scenario string
}

func (u *busyFailoverLoopUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.hits = append(u.hits, accountID)
	attempt := len(u.hits)
	u.mu.Unlock()
	_, _ = io.Copy(io.Discard, req.Body)
	body := responsesBusyText + responsesBusyComplete
	if accountID == 801 && !(u.scenario == "same_account_retry" && attempt == 2) {
		body = responsesBusyEvent
		if u.scenario == "empty_delta" {
			body = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"\"}\n\n" + body
		}
		if u.scenario == "partial_output" {
			body = responsesBusyText + body
		}
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func (u *busyFailoverLoopUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, concurrency)
}

// 普通 Key 必须经过真实 Responses handler 的组内选号、并发槽释放和有界重试恢复。
// 排队场景使用生产心跳分支，仅缩短测试实例的心跳间隔，不改变默认配置。
func TestOpenAIResponsesBusyFailoverLoop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scenario := range []string{"bare_error", "queue_heartbeat", "empty_delta", "same_account_retry", "partial_output"} {
		t.Run(scenario, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 8)
			groupID := int64(901)
			accounts := []service.Account{
				{ID: 801, Name: "busy", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 60, Priority: 1, Credentials: map[string]any{"api_key": "local-first"}},
				{ID: 802, Name: "healthy", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 60, Priority: 2, Credentials: map[string]any{"api_key": "local-second"}},
			}
			if scenario == "same_account_retry" {
				accounts[0].Credentials["pool_mode"] = true
				accounts[0].Credentials["pool_mode_retry_count"] = 1
				accounts[0].Credentials["pool_mode_retry_status_codes"] = []any{502}
			}
			repo := &grokCredentialHandlerRepo{accounts: accounts}
			upstream := &busyFailoverLoopUpstream{scenario: scenario}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Gateway.MaxAccountSwitches = 2
			billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billingCache.Stop)
			var userAttempts atomic.Int32
			cache := &concurrencyCacheMock{
				acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) {
					if scenario == "queue_heartbeat" && userAttempts.Add(1) <= 2 {
						return false, nil
					}
					return true, nil
				},
				acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
			}
			concurrency := service.NewConcurrencyService(cache)
			usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 4)}
			gateway := service.NewOpenAIGatewayService(repo, usageRepo, nil, nil, nil, nil, nil, cfg, nil, concurrency, service.NewBillingService(cfg, nil), nil, billingCache, upstream, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
			handler := NewOpenAIGatewayHandler(gateway, concurrency, billingCache, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
			handler.concurrencyHelper.pingInterval = time.Millisecond
			key := &service.APIKey{
				ID: 902, GroupID: &groupID,
				User:  &service.User{ID: 903, Status: service.StatusActive},
				Group: &service.Group{ID: groupID, Name: "ordinary-group", Platform: service.PlatformOpenAI, Status: service.StatusActive, AllowedClientProtocols: []service.GroupClientProtocol{service.GroupClientProtocolOpenAIResponses}},
			}
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)))
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), key)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.User.ID, Concurrency: 60})
				c.Next()
			})
			router.POST("/v1/responses", handler.Responses)
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol","input":"hello","stream":true}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			if scenario == "queue_heartbeat" {
				require.GreaterOrEqual(t, userAttempts.Load(), int32(3))
				require.True(t, strings.HasPrefix(response.Body.String(), ":\n\n"), "生产等待分支应先向客户端发送心跳")
			}
			switch scenario {
			case "same_account_retry":
				require.Equal(t, []int64{801, 801}, upstream.hits)
			case "partial_output":
				require.Equal(t, []int64{801}, upstream.hits)
				require.Contains(t, response.Body.String(), "The service is busy")
				require.NotContains(t, response.Body.String(), "response.completed")
			default:
				require.Equal(t, []int64{801, 802}, upstream.hits)
			}
			if scenario != "partial_output" {
				require.Contains(t, response.Body.String(), "visible-response")
				require.Equal(t, 1, strings.Count(response.Body.String(), "event: response.completed\n"))
				require.NotContains(t, response.Body.String(), "The service is busy")
				require.NotContains(t, response.Body.String(), "upstream-busy-attempt")
				require.Len(t, usageRepo.created, 1, "恢复成功仅记录成功账号的一条用量")
				usage := <-usageRepo.created
				require.Equal(t, upstream.hits[len(upstream.hits)-1], usage.AccountID)
			}
			require.Equal(t, int32(1), atomic.LoadInt32(&cache.releaseUserCalled))
			require.Equal(t, int32(len(upstream.hits)), atomic.LoadInt32(&cache.releaseAccountCalled), "失败和成功尝试均应释放账号并发槽")
			require.Len(t, opsErrorLogQueue, 1)
			job := <-opsErrorLogQueue
			require.Equal(t, "upstream", job.entry.ErrorPhase)
			require.NotNil(t, job.entry.UpstreamErrorsJSON)
			var events []service.OpsUpstreamErrorEvent
			require.NoError(t, json.Unmarshal([]byte(*job.entry.UpstreamErrorsJSON), &events))
			require.Len(t, events, 1)
			require.Equal(t, int64(801), events[0].AccountID)
			if scenario != "partial_output" {
				require.Equal(t, http.StatusOK, job.entry.StatusCode)
				require.Contains(t, job.entry.ErrorMessage, "Recovered upstream error")
			}
		})
	}
}
