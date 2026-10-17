//go:build unit

package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	middleware2 "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 真实处理器验证同步/异步 MJ 均先冻结四张，非法 n 与资金不足绝不创建上游任务。
func TestMidjourneyImagesHandlersReserveFourBeforeGeneration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name          string
		async         bool
		n             string
		mapping       bool
		switchFromGPT bool
		backup        bool
		edit          bool
		reserveErr    error
		transport     error
		status        int
		wantStatus    int
		wantEvents    []string
	}{
		{name: "同步四张扣费", n: "1", wantStatus: 200, wantEvents: []string{"reserve", "upstream", "capture"}},
		{name: "异步四张扣费", n: "1", async: true, wantStatus: 200, wantEvents: []string{"reserve", "upstream", "capture"}},
		{name: "账号别名四张扣费", n: "1", mapping: true, wantStatus: 200, wantEvents: []string{"reserve", "upstream", "capture"}},
		{name: "余额不足", n: "1", reserveErr: service.ErrBatchImageInsufficientBalance, wantStatus: 403, wantEvents: []string{"reserve"}},
		{name: "多组参数拒绝", n: "2", wantStatus: 400},
		{name: "小数参数拒绝", n: "1.5", wantStatus: 400},
		{name: "重复张数拒绝", n: `1,"n":2`, wantStatus: 400},
		{name: "重复型号拒绝", n: `1,"model":"MJ-v8.2"`, mapping: true, wantStatus: 400},
		{name: "空张数拒绝", n: "null", wantStatus: 400},
		{name: "字符串张数拒绝", n: `"1"`, wantStatus: 400},
		{name: "流式拒绝", n: `1,"stream":true`, wantStatus: 400},
		{name: "账号别名流式拒绝", n: `1,"stream":true`, mapping: true, wantStatus: 400},
		{name: "重复流式字段拒绝", n: `1,"stream":false,"stream":true`, wantStatus: 400},
		{name: "编辑拒绝", n: `1,"images":[{"image_url":"https://media.example/test.png"}]`, edit: true, wantStatus: 400},
		{name: "账号别名编辑拒绝", n: `1,"images":[{"image_url":"https://media.example/test.png"}]`, edit: true, mapping: true, wantStatus: 400},
		{name: "账号别名不能绕过多组限制", n: "2", mapping: true, wantStatus: 400},
		{name: "账号别名不能绕过小数限制", n: "1.5", mapping: true, wantStatus: 400},
		{name: "上游明确拒绝释放", n: "1", status: 400, wantEvents: []string{"reserve", "upstream", "release"}},
		{name: "上游结果未知不重复请求", n: "1", transport: errors.New("local simulated interrupted response"), wantEvents: []string{"reserve", "upstream", "reconciliation"}},
		{name: "代理504不得换号重发", n: "1", status: 504, backup: true, wantEvents: []string{"reserve", "upstream", "reconciliation"}},
		{name: "上游500不得换号重发", n: "1", status: 500, backup: true, wantEvents: []string{"reserve", "upstream", "reconciliation"}},
		{name: "上游408不得换号重发", n: "1", status: 408, backup: true, wantEvents: []string{"reserve", "upstream", "reconciliation"}},
		{name: "同步切到MJ先补预占", n: "1", mapping: true, switchFromGPT: true, wantStatus: 200, wantEvents: []string{"upstream", "reserve", "upstream", "capture"}},
		{name: "异步不能突破首次冻结张数", n: "1", async: true, mapping: true, switchFromGPT: true, wantStatus: 503, wantEvents: []string{"reserve", "upstream", "release"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			billing := &asyncImageBillingRepoStub{reserveErr: tc.reserveErr}
			upstream := &asyncImageBillingUpstream{repo: billing, transportErr: tc.transport,
				responseBody: `{"data":[{"b64_json":"MQ=="},{"b64_json":"Mg=="},{"b64_json":"Mw=="},{"b64_json":"NA=="}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`}
			if tc.status != 0 {
				upstream.statuses = []int{tc.status}
			}
			cfg := &config.Config{RunMode: config.RunModeStandard}
			cfg.Default.RateMultiplier = 1
			groupID, price := int64(4101), 0.12
			key := &service.APIKey{ID: 4102, UserID: 4103, Status: service.StatusActive, BillingMode: service.APIKeyBillingModeAuto,
				GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, RateMultiplier: 6, AllowImageGeneration: true, ImagePrice1K: &price},
				User: &service.User{ID: 4103, Status: service.StatusActive, Balance: 100}}
			model := "MJ-v8.2"
			if tc.mapping {
				model = "gpt-image-2"
			}
			accounts := openAIImagesFailoverAccountRepo{accounts: []service.Account{
				{ID: 4104, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
					Credentials: map[string]any{"api_key": "local-test", "model_mapping": map[string]any{model: "MJ-v8.2"}}},
			}}
			if tc.switchFromGPT {
				// 首个账号明确拒绝后改选 MJ，检验未冻结与已冻结一张的两种预算边界。
				fallback := accounts.accounts[0]
				fallback.ID++
				accounts.accounts[0].Credentials = map[string]any{"api_key": "local-test", "model_mapping": map[string]any{model: model}}
				accounts.accounts = append(accounts.accounts, fallback)
				upstream.statuses = []int{503, 200}
			}
			if tc.backup {
				fallback := accounts.accounts[0]
				fallback.ID++
				accounts.accounts = append(accounts.accounts, fallback)
				upstream.statuses = []int{tc.status, 200}
			}
			forward := service.NewOpenAIGatewayService(accounts, &routingFocusUsageSink{}, billing, nil, nil, asyncImageBillingRateRepo{}, nil, cfg, nil, nil,
				service.NewBillingService(cfg, nil), nil, nil, upstream, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
			eligibility := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple}, nil)
			t.Cleanup(eligibility.Stop)
			handler := NewOpenAIGatewayHandler(forward, service.NewConcurrencyService(&fakeConcurrencyCache{}), eligibility,
				service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
			handler.maxAccountSwitches = 0
			if tc.switchFromGPT || tc.backup {
				handler.maxAccountSwitches = 1
			}
			ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "midjourney-image-handler-test")
			if tc.async {
				ctx = service.WithAsyncImageExecutionContext(ctx)
			}
			path := "/v1/images/generations"
			if tc.edit {
				path = "/v1/images/edits"
			}
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"`+model+`","prompt":"local test","n":`+tc.n+`,"size":"1024x1024"}`)).WithContext(ctx)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = request
			c.Set(string(middleware2.ContextKeyAPIKey), key)
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: key.UserID})
			handler.Images(c)
			require.Equal(t, tc.wantEvents, billing.events, recorder.Body.String())
			if tc.wantStatus != 0 {
				require.Equal(t, tc.wantStatus, recorder.Code, recorder.Body.String())
			}
			if billing.reserveCommand != nil {
				expected := 0.48
				if tc.switchFromGPT && tc.async {
					expected = 0.12
				}
				require.InDelta(t, expected, billing.reserveCommand.Hold.BaseAmountUSD, 1e-10)
			}
			if billing.captureCommand != nil {
				require.InDelta(t, 0.48, billing.captureCommand.BaseAmountUSD, 1e-10)
				require.InDelta(t, 0.48, billing.captureCommand.BillableAmountUSD, 1e-10)
			}
		})
	}
}
