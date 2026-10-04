//go:build unit

package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	middleware2 "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// asyncImageBillingRepoStub 只观察处理器资金操作次序，不模拟数据库锁定或访问真实账本。
type asyncImageBillingRepoStub struct {
	service.UsageBillingRepository
	mu             sync.Mutex
	events         []string
	reserveErr     error
	captureErr     error
	reserveCommand *service.ImageBillingReserveCommand
	captureCommand *service.UsageBillingCommand
}

func (r *asyncImageBillingRepoStub) event(value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, value)
}

func (r *asyncImageBillingRepoStub) ResolveUsableSubscriptionForGroup(context.Context, int64, int64) (*service.UserSubscription, error) {
	return nil, nil
}

func (r *asyncImageBillingRepoStub) ReserveImageBilling(_ context.Context, command *service.ImageBillingReserveCommand) (*service.ImageBillingReservation, error) {
	r.event("reserve")
	r.reserveCommand = command
	if r.reserveErr != nil {
		return nil, r.reserveErr
	}
	return &service.ImageBillingReservation{ID: command.Hold.BatchID, State: service.ImageBillingReserved, Applied: true, Hold: command.Hold, Quote: command.Quote}, nil
}

func (r *asyncImageBillingRepoStub) CaptureImageBilling(_ context.Context, _ string, command *service.UsageBillingCommand, base float64) (*service.UsageBillingApplyResult, error) {
	r.event("capture")
	r.captureCommand = command
	if r.captureErr != nil {
		return nil, r.captureErr
	}
	return &service.UsageBillingApplyResult{Applied: true, BalanceAmountUSD: base * command.BalanceRateMultiplier}, nil
}

func (r *asyncImageBillingRepoStub) ReleaseImageBilling(context.Context, string, int64, int64) error {
	r.event("release")
	return nil
}

func (r *asyncImageBillingRepoStub) MarkImageBillingReconciliation(context.Context, string, int64, int64) error {
	r.event("reconciliation")
	return nil
}

func (*asyncImageBillingRepoStub) ReconcileExpiredImageBilling(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func (r *asyncImageBillingRepoStub) Apply(context.Context, *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	r.event("unexpected_regular_billing")
	return nil, errors.New("reserved images must not use regular billing")
}

// asyncImageBillingRateRepo 为余额提供独立倍率，检验订阅默认倍率不会污染预占命令。
type asyncImageBillingRateRepo struct {
	service.UserGroupRateRepository
}

func (asyncImageBillingRateRepo) GetByUserAndGroup(context.Context, int64, int64) (*float64, error) {
	rate := 1.0
	return &rate, nil
}

// asyncImageBillingUpstream 始终返回本地构造的响应，任何测试都不建立外部 HTTP 连接。
type asyncImageBillingUpstream struct {
	repo         *asyncImageBillingRepoStub
	statuses     []int
	transportErr error
	responseBody string
	onRequest    func()
	calls        int
}

func (u *asyncImageBillingUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.repo.event("upstream")
	u.calls++
	if u.onRequest != nil {
		u.onRequest()
	}
	if u.transportErr != nil {
		return nil, u.transportErr
	}
	status := http.StatusOK
	if len(u.statuses) >= u.calls {
		status = u.statuses[u.calls-1]
	}
	body := `{"data":[{"b64_json":"YQ==","size":"1024x1024"}]}`
	if u.responseBody != "" {
		body = u.responseBody
	}
	if status >= http.StatusBadRequest {
		body = `{"error":{"type":"invalid_request_error","message":"local image request rejected"}}`
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func (u *asyncImageBillingUpstream) DoWithTLS(request *http.Request, proxy string, account int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(request, proxy, account, concurrency)
}

// TestAsyncImageBillingHandlers 覆盖真实图片处理器与预占接口的生命周期，后台 usage worker 被故意占满。
func TestAsyncImageBillingHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, platform := range []string{service.PlatformOpenAI, service.PlatformGrok} {
		for _, scenario := range []struct {
			name                                 string
			statuses                             []int
			reserveErr, captureErr, transportErr error
			responseBody                         string
			changePrice                          bool
			invalidBaseURL                       bool
			wantEvents                           []string
		}{
			{name: "成功同步捕获", wantEvents: []string{"reserve", "upstream", "capture"}},
			{name: "余额不足不调用上游", reserveErr: service.ErrInsufficientBalance, wantEvents: []string{"reserve"}},
			{name: "本地地址配置错误释放", invalidBaseURL: true, wantEvents: []string{"reserve", "release"}},
			{name: "明确拒绝释放", statuses: []int{400}, wantEvents: []string{"reserve", "upstream", "release"}},
			{name: "传输未知保留预占", transportErr: errors.New("local simulated response interrupted"), wantEvents: []string{"reserve", "upstream", "reconciliation"}},
			{name: "捕获失败待核对", captureErr: errors.New("local simulated capture failure"), wantEvents: []string{"reserve", "upstream", "capture", "reconciliation"}},
			{name: "切换账号仅预占一次", statuses: []int{503, 200}, wantEvents: []string{"reserve", "upstream", "upstream", "capture"}},
			{name: "空成功响应不能扣费或重放", responseBody: `{"data":[]}`, wantEvents: []string{"reserve", "upstream", "reconciliation"}},
			{name: "运行中改价沿用预占报价", changePrice: true, wantEvents: []string{"reserve", "upstream", "capture"}},
		} {
			t.Run(platform+"/"+scenario.name, func(t *testing.T) {
				billing := &asyncImageBillingRepoStub{reserveErr: scenario.reserveErr, captureErr: scenario.captureErr}
				upstream := &asyncImageBillingUpstream{repo: billing, statuses: scenario.statuses, transportErr: scenario.transportErr, responseBody: scenario.responseBody}
				cfg := &config.Config{RunMode: config.RunModeStandard}
				cfg.Default.RateMultiplier = 1
				groupID := int64(4101)
				price := 0.12
				key := &service.APIKey{ID: 4102, UserID: 4103, Status: service.StatusActive, BillingMode: service.APIKeyBillingModeAuto,
					GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: platform, RateMultiplier: 6, AllowImageGeneration: true, ImagePrice1K: &price},
					User: &service.User{ID: 4103, Status: service.StatusActive, Balance: 100}}
				if scenario.changePrice {
					upstream.onRequest = func() { price = 9 }
				}
				accounts := openAIImagesFailoverAccountRepo{accounts: []service.Account{
					{ID: 4104, Platform: platform, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 0, Credentials: map[string]any{"api_key": "local-test"}},
					{ID: 4105, Platform: platform, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 1, Credentials: map[string]any{"api_key": "local-test"}},
				}}
				if scenario.invalidBaseURL {
					accounts.accounts[0].Credentials["base_url"] = "://invalid-local-url"
				}
				usage := &routingFocusUsageSink{}
				forward := service.NewOpenAIGatewayService(accounts, usage, billing, nil, nil, asyncImageBillingRateRepo{}, nil, cfg, nil, nil,
					service.NewBillingService(cfg, nil), nil, nil, upstream, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
				// 入口资格预检使用独立空配置跳过外部缓存；本例资金准入由真实 prepare 和预占桩验证。
				eligibilityCfg := &config.Config{RunMode: config.RunModeSimple}
				eligibility := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, eligibilityCfg, nil)
				t.Cleanup(eligibility.Stop)
				pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{WorkerCount: 1, QueueSize: 4, TaskTimeout: time.Minute})
				t.Cleanup(pool.Stop)
				blocked, started := make(chan struct{}), make(chan struct{})
				t.Cleanup(func() { close(blocked) })
				pool.Submit(func(context.Context) { close(started); <-blocked })
				<-started
				handler := NewOpenAIGatewayHandler(forward, service.NewConcurrencyService(&fakeConcurrencyCache{}), eligibility,
					service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), pool, nil, nil, nil, cfg)
				handler.maxAccountSwitches = 0
				if len(scenario.statuses) > 1 {
					handler.maxAccountSwitches = 1
				}
				model := "gpt-image-2"
				if platform == service.PlatformGrok {
					model = "grok-imagine-image"
				}
				ctx := context.WithValue(service.WithAsyncImageExecutionContext(context.Background()), ctxkey.ClientRequestID, "async-billing-handler-test")
				request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"`+model+`","prompt":"local test","n":1,"size":"1024x1024"}`)).WithContext(ctx)
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = request
				c.Set(string(middleware2.ContextKeyAPIKey), key)
				c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: key.UserID})
				if platform == service.PlatformGrok {
					handler.GrokImages(c)
				} else {
					handler.Images(c)
				}
				require.Equal(t, scenario.wantEvents, billing.events, recorder.Body.String())
				require.Equal(t, upstream.calls, service.ImageUpstreamAttemptCount(c), "发送计数必须与实际 HTTP 调用一致")
				if billing.reserveCommand != nil && scenario.reserveErr == nil {
					require.InDelta(t, 0.12, billing.reserveCommand.Hold.BaseAmountUSD, 1e-10)
					require.InDelta(t, 1, billing.reserveCommand.Hold.BalanceRateMultiplier, 1e-10)
				}
				if billing.captureCommand != nil {
					require.Equal(t, "client:async-billing-handler-test", billing.captureCommand.RequestID)
					require.InDelta(t, 0.12, billing.captureCommand.BaseAmountUSD, 1e-10)
				}
			})
		}
	}
}

// TestAsyncImagePreflightFailurePreservesPriorUncertainty 防止内部预检失败抹掉前一次已发送请求的未知结果。
func TestAsyncImagePreflightFailurePreservesPriorUncertainty(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	state := &asyncImageBillingState{reservation: &service.OpenAIImageBillingReservation{}, uncertain: true}
	state.startForward(c)
	state.forwardFailed(c, errors.New("local request validation failed"))
	require.True(t, state.uncertain)
}

// TestAsyncImageFailureClassification 防止历史账号错误或网关合成的 502 被误当成可退款依据。
func TestAsyncImageFailureClassification(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		err    error
		events []*service.OpsUpstreamErrorEvent
		start  int
		want   bool
	}{
		{name: "明确图片拒绝", err: &service.OpenAIImagesUpstreamError{StatusCode: 400, Code: "invalid_request"}, want: true},
		{name: "图片读取中断", err: &service.OpenAIImagesUpstreamError{StatusCode: 502, Code: service.OpenAIUpstreamStreamReadErrorCode}},
		{name: "图片响应截断", err: &service.OpenAIImagesUpstreamError{StatusCode: 502, Code: service.OpenAIUpstreamStreamTruncatedCode}},
		{name: "HTTP2图片流重置", err: &service.OpenAIImagesUpstreamError{StatusCode: 502, Code: service.OpenAIUpstreamHTTP2StreamErrorCode}},
		{name: "合成502缺乏拒绝证据", err: &service.UpstreamFailoverError{StatusCode: 502}},
		{name: "仅旧账号拒绝", err: errors.New("new request failed"), events: []*service.OpsUpstreamErrorEvent{{Kind: "http_error", UpstreamStatusCode: 400}}, start: 1},
		{name: "本轮上游拒绝", err: errors.New("upstream refused"), events: []*service.OpsUpstreamErrorEvent{{Kind: "http_error", UpstreamStatusCode: 400}}, want: true},
		{name: "网络错误保守待核对", err: &service.UpstreamFailoverError{StatusCode: 502}, events: []*service.OpsUpstreamErrorEvent{{Kind: "request_error", UpstreamStatusCode: 0}}},
		{name: "内部先网络未知后明确拒绝", err: &service.OpenAIImagesUpstreamError{StatusCode: 400, Code: "invalid_request"}, events: []*service.OpsUpstreamErrorEvent{{Kind: "request_error", UpstreamStatusCode: 0}, {Kind: "http_error", UpstreamStatusCode: 400}}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set(service.OpsUpstreamErrorsKey, scenario.events)
			require.Equal(t, scenario.want, asyncImageFailureDefinitelyRejected(c, scenario.err, scenario.start))
		})
	}
}
