//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 用真实选号与handler模拟选择后队列抢满，传输替身只允许健康账号真正生成。
type rescheduleConcurrencyCache struct {
	concurrencyCacheMock
	fullQueue     bool
	allBusy       bool
	waits         atomic.Int32
	releasedWaits atomic.Int32
}

func (c *rescheduleConcurrencyCache) AcquireAccountSlot(ctx context.Context, id int64, _ int, _ string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return id == 802 && !c.allBusy, nil
}

func (c *rescheduleConcurrencyCache) IncrementAccountWaitCount(context.Context, int64, int) (bool, error) {
	if c.fullQueue {
		return false, nil
	}
	c.waits.Add(1)
	return true, nil
}

func (c *rescheduleConcurrencyCache) DecrementAccountWaitCount(context.Context, int64) error {
	c.releasedWaits.Add(1)
	return nil
}

func TestResponsesAccountSlotReschedulesBeforeSubmission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scenario := range []string{"queue_full", "wait_timeout", "all_queues_full"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Gateway.MaxAccountSwitches = 2
			cfg.Gateway.Scheduling.FallbackWaitTimeout = 5 * time.Millisecond
			cfg.Gateway.Scheduling.LoadBatchEnabled = false
			accounts := []service.Account{
				{ID: 801, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1, Credentials: map[string]any{"api_key": "test-first"}},
				{ID: 802, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1, Priority: 2, Credentials: map[string]any{"api_key": "test-second"}},
			}
			cache := &rescheduleConcurrencyCache{fullQueue: scenario != "wait_timeout", allBusy: scenario == "all_queues_full"}
			cache.acquireUserSlotFn = func(context.Context, int64, int, string) (bool, error) { return true, nil }
			concurrency := service.NewConcurrencyService(cache)
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billing.Stop)
			usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 4)}
			upstream := &busyFailoverLoopUpstream{}
			gateway := service.NewOpenAIGatewayService(&grokCredentialHandlerRepo{accounts: accounts}, usage, nil, nil, nil, nil, nil, cfg, nil, concurrency, service.NewBillingService(cfg, nil), nil, billing, upstream, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
			h := NewOpenAIGatewayHandler(gateway, concurrency, billing, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
			h.concurrencyHelper.pingInterval = time.Millisecond
			groupID := int64(991)
			key := &service.APIKey{ID: 992, GroupID: &groupID, User: &service.User{ID: 993, Status: service.StatusActive}, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive}}
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), key)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 993, Concurrency: 2})
				c.Next()
			})
			router.POST("/v1/responses", h.Responses)
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol","input":"hello","stream":true}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if scenario == "all_queues_full" {
				require.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
				require.Contains(t, rec.Body.String(), gatewayQueueFullCode)
				require.Empty(t, upstream.hits)
				require.Empty(t, usage.created)
			} else {
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Equal(t, []int64{802}, upstream.hits, "满槽账号不能发出生成请求")
				require.Contains(t, rec.Body.String(), "visible-response")
				require.NotContains(t, rec.Body.String(), "Too many pending")
				require.Len(t, usage.created, 1)
				require.Equal(t, int64(802), (<-usage.created).AccountID)
				require.EqualValues(t, 1, atomic.LoadInt32(&cache.releaseAccountCalled))
			}
			require.Equal(t, cache.waits.Load(), cache.releasedWaits.Load())
			require.EqualValues(t, 1, atomic.LoadInt32(&cache.releaseUserCalled))
		})
	}
}

// 续接、用户并发、取消和切换预算均不能被本地重调度绕过。
func TestAccountSlotRescheduleBoundaries(t *testing.T) {
	for _, scenario := range []string{"normal", "bound", "canceled", "budget", "user_limit"} {
		t.Run(scenario, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			count := 0
			s := &accountSlotReschedule{excluded: map[int64]struct{}{}, switches: &count, maximum: 1, requiredAccount: scenario == "bound"}
			status, code := http.StatusTooManyRequests, gatewayQueueFullCode
			if scenario == "canceled" {
				cancel()
			}
			if scenario == "budget" {
				count = 1
			}
			if scenario == "user_limit" {
				code = "user_limit"
			}
			require.Equal(t, scenario == "normal", s.handleCapacity(c, 11, status, code, "busy"))
			if scenario == "normal" {
				require.Contains(t, s.excluded, int64(11))
				require.Equal(t, 1, count)
			} else {
				require.Empty(t, s.excluded)
			}
		})
	}
}

func TestAccountSlotRescheduleSharesWaitDeadline(t *testing.T) {
	s := &accountSlotReschedule{}
	require.LessOrEqual(t, s.waitTimeout(time.Second), time.Second)
	s.waitDeadline = time.Now().Add(-time.Second)
	require.Less(t, s.waitTimeout(time.Minute), time.Duration(0), "下一账号不得重新获得完整等待窗口")
}
