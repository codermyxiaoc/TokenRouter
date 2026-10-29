//go:build unit

package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	middleware2 "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// runDeferredAsyncImageHandler 走真实图片处理器与预占接口，并安装异步任务的延迟结算容器。
func runDeferredAsyncImageHandler(t *testing.T, platform string, billing *asyncImageBillingRepoStub, upstream *asyncImageBillingUpstream) *service.AsyncImageDeferredCapture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Default.RateMultiplier = 1
	groupID := int64(4201)
	price := 0.12
	key := &service.APIKey{ID: 4202, UserID: 4203, Status: service.StatusActive, BillingMode: service.APIKeyBillingModeAuto,
		GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: platform, RateMultiplier: 6, AllowImageGeneration: true, ImagePrice1K: &price},
		User: &service.User{ID: 4203, Status: service.StatusActive, Balance: 100}}
	accounts := openAIImagesFailoverAccountRepo{accounts: []service.Account{
		{ID: 4204, Platform: platform, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 0, Credentials: map[string]any{"api_key": "local-test"}},
	}}
	usage := &routingFocusUsageSink{}
	forward := service.NewOpenAIGatewayService(accounts, usage, billing, nil, nil, asyncImageBillingRateRepo{}, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, nil, upstream, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	eligibility := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple}, nil)
	t.Cleanup(eligibility.Stop)
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{WorkerCount: 1, QueueSize: 4, TaskTimeout: time.Minute})
	t.Cleanup(pool.Stop)
	handler := NewOpenAIGatewayHandler(forward, service.NewConcurrencyService(&fakeConcurrencyCache{}), eligibility,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), pool, nil, nil, nil, cfg)
	handler.maxAccountSwitches = 0
	model := "gpt-image-2"
	if platform == service.PlatformGrok {
		model = "grok-imagine-image"
	}
	base, holder := service.WithAsyncImageDeferredCapture(context.Background())
	ctx := context.WithValue(service.WithAsyncImageExecutionContext(base), ctxkey.ClientRequestID, "deferred-billing-test")
	request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"`+model+`","prompt":"local test","n":1,"size":"1024x1024"}`)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = request
	c.Set(string(middleware2.ContextKeyAPIKey), key)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: key.UserID})
	if platform == service.PlatformGrok {
		handler.GrokImages(c)
	} else {
		handler.Images(c)
	}
	return holder
}

// 先交付后扣费：生成成功时只冻结并标记待核对；交付后捕获沿用原请求 ID，未交付则释放。
func TestAsyncImageDeferredCaptureLedgerOrder(t *testing.T) {
	for _, platform := range []string{service.PlatformOpenAI, service.PlatformGrok} {
		t.Run(platform+"/delivered", func(t *testing.T) {
			billing := &asyncImageBillingRepoStub{}
			holder := runDeferredAsyncImageHandler(t, platform, billing, &asyncImageBillingUpstream{repo: billing})
			require.Equal(t, []string{"reserve", "upstream", "reconciliation"}, billing.events, "交付前不得捕获")
			require.True(t, holder.Pending())
			// 结算在后台任务中以全新上下文触发，仍须带原请求身份。
			require.NoError(t, holder.Capture(context.Background()))
			require.Equal(t, []string{"reserve", "upstream", "reconciliation", "capture"}, billing.events)
			require.NotNil(t, billing.captureCommand)
			require.Equal(t, "client:deferred-billing-test", billing.captureCommand.RequestID)
			require.InDelta(t, 0.12, billing.captureCommand.BaseAmountUSD, 1e-10)
		})
		t.Run(platform+"/undelivered", func(t *testing.T) {
			billing := &asyncImageBillingRepoStub{}
			holder := runDeferredAsyncImageHandler(t, platform, billing, &asyncImageBillingUpstream{repo: billing})
			require.NoError(t, holder.Release(context.Background()))
			require.Equal(t, []string{"reserve", "upstream", "reconciliation", "release"}, billing.events, "未交付必须释放且不能捕获")
			require.Nil(t, billing.captureCommand)
		})
		t.Run(platform+"/capture_failure_is_retryable", func(t *testing.T) {
			billing := &asyncImageBillingRepoStub{captureErr: errors.New("database unavailable")}
			holder := runDeferredAsyncImageHandler(t, platform, billing, &asyncImageBillingUpstream{repo: billing})
			require.Error(t, holder.Capture(context.Background()))
			require.True(t, holder.Pending(), "一次捕获失败不能消耗结算机会")
			billing.captureErr = nil
			require.NoError(t, holder.Capture(context.Background()))
			require.True(t, holder.Captured())
		})
		t.Run(platform+"/rejected_never_defers", func(t *testing.T) {
			billing := &asyncImageBillingRepoStub{}
			holder := runDeferredAsyncImageHandler(t, platform, billing, &asyncImageBillingUpstream{repo: billing, statuses: []int{400}})
			require.Equal(t, []string{"reserve", "upstream", "release"}, billing.events)
			require.False(t, holder.Pending(), "上游明确拒绝没有产图，不能登记扣费")
		})
	}
}
