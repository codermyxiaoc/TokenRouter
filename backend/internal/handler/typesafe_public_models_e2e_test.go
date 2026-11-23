//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/handler/dto"
	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type systemOneMarketplaceGroups struct {
	service.GroupRepository
	group service.Group
}

func (r systemOneMarketplaceGroups) ListActive(context.Context) ([]service.Group, error) {
	return []service.Group{r.group}, nil
}

type systemOneMarketplaceAccounts struct {
	*grokCredentialHandlerRepo
}

func (r systemOneMarketplaceAccounts) ListSchedulable(ctx context.Context) ([]service.Account, error) {
	return r.ListSchedulableByPlatform(ctx, service.PlatformTypeSafe)
}

// 用同一组账号核对未知型号的 404，不依赖生产库的可用性诊断查询。
func (r systemOneMarketplaceAccounts) ListModelAvailabilityCandidates(ctx context.Context, groupID *int64, platforms []string, _ bool) ([]service.Account, error) {
	if groupID == nil || *groupID != 31 || len(platforms) != 1 || platforms[0] != service.PlatformTypeSafe {
		return nil, nil
	}
	return r.ListSchedulable(ctx)
}

// 将实际返回的广场价目与用户请求、上游收到的型号和结算命令逐项核对；只有持久存储与供应商使用替身。
func TestTypeSafeMarketplacePricesMatchSystemOneForwardAndBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	models := []string{"jev-1.13", "jev-1.14", "gemini-decision-1", "gpt-6-luna-decision"}
	for _, tc := range []struct {
		name      string
		public    []string
		whitelist []string
		mapLatest bool
	}{
		{"whitelist-only", models, models, false},
		{"with-legacy-alias", append(append([]string(nil), models...), "jev-latest"), models, true},
		{"legacy-default", []string{"jev-latest"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := make(chan string, len(tc.public)+1)
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/systemone" {
					t.Errorf("意外的上游请求 %s %s", r.Method, r.URL.Path)
				}
				body, err := io.ReadAll(r.Body)
				var request struct {
					Model string `json:"model"`
				}
				if err != nil || json.Unmarshal(body, &request) != nil {
					t.Errorf("上游请求体无效: %s", body)
					http.Error(w, "bad request", 400)
					return
				}
				observed <- request.Model
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("x-request-id", fmt.Sprintf("fixture-%d", calls.Add(1)))
				_, _ = fmt.Fprintf(w, `{"answers":{"urgency":{"noul":0}},"usage":{"input_tokens":%d,"output_tokens":%d}}`, 100+len(request.Model), 10+len(request.Model))
			}))
			t.Cleanup(up.Close)
			group := service.Group{ID: 31, Name: "决策模型", Platform: service.PlatformTypeSafe, Status: service.StatusActive, Hydrated: true, RateMultiplier: 1, ActiveAccountCount: 1}
			for i, id := range tc.public {
				// 每个公开型号有不同的输入和输出测试价；一律套用 jev-latest 价格会使此断言失败。
				input, output := float64(i+1)*0.00001, float64(i+1)*0.00003
				group.ModelPricing = append(group.ModelPricing, service.ChannelModelPricing{Models: []string{id}, BillingMode: service.BillingModeToken, InputPrice: &input, OutputPrice: &output})
			}
			credential := map[string]any{"api_key": "local-test-key", "base_url": up.URL}
			if tc.whitelist != nil {
				credential["model_whitelist"] = tc.whitelist
			}
			if tc.mapLatest {
				// 原有公开别名与同名可请求的白名单目标可以同时存在，各自走自己的分组价。
				credential["model_mapping"] = map[string]any{"jev-latest": "jev-1.14", "team-*": "jev-1.14"}
			}
			account := service.Account{ID: 41, Platform: service.PlatformTypeSafe, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 2, GroupIDs: []int64{group.ID}, Credentials: credential}
			accounts := systemOneMarketplaceAccounts{&grokCredentialHandlerRepo{accounts: []service.Account{account}}}
			cfg := &config.Config{RunMode: config.RunModeStandard, Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{AllowInsecureHTTP: true}}}
			user := &service.User{ID: 51, Status: service.StatusActive, Balance: 10}
			key := &service.APIKey{ID: 61, UserID: user.ID, User: user, GroupID: &group.ID, Group: &group, Status: service.StatusActive, BillingMode: service.APIKeyBillingModeBalance}
			userRepo := &cachedBillingUserRepo{balance: user.Balance}
			balance := service.NewBillingCacheService(nil, userRepo, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(balance.Stop)
			logs, bills := &routingFocusUsageSink{}, &routingFocusBillingSink{}
			billing := service.NewBillingService(cfg, nil)
			resolver := service.NewModelPricingResolver(nil, billing)
			transport := &encryptedRoutingLocalUpstream{allowed: map[string]bool{up.URL: true}}
			catalog := service.NewGatewayService(accounts, nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, billing, nil, nil, nil, transport, nil, nil, nil, nil, nil, nil, nil, nil, resolver, nil, nil)
			market := NewModelMarketplaceHandler(service.NewModelMarketplaceService(systemOneMarketplaceGroups{group: group}, nil, catalog, billing, nil, nil, cfg), nil)
			forward := service.NewOpenAIGatewayService(accounts, logs, bills, userRepo, nil, nil, nil, cfg, nil, nil, billing, nil, balance, transport, nil, &service.DeferredService{}, nil, nil, resolver, nil, nil, nil, nil)
			takeSlot := func(context.Context, int64, int, string) (bool, error) { return true, nil }
			concurrency := service.NewConcurrencyService(&concurrencyCacheMock{acquireUserSlotFn: takeSlot, acquireAccountSlotFn: takeSlot})
			gateway := NewOpenAIGatewayHandler(forward, concurrency, balance, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
			r := gin.New()
			r.GET("/api/v1/marketplace/models", market.ListPublic)
			r.POST("/v1/systemone", func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), key)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: user.ID, Concurrency: 2})
				c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, &group))
				gateway.SystemOne(c)
			})
			list := httptest.NewRecorder()
			r.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/marketplace/models", nil))
			require.Equal(t, http.StatusOK, list.Code, list.Body.String())
			var response struct {
				Data []dto.ModelMarketplaceGroup `json:"data"`
			}
			require.NoError(t, json.Unmarshal(list.Body.Bytes(), &response))
			require.Len(t, response.Data, 1)
			models := make(map[string]dto.ModelMarketplacePricing)
			for _, m := range response.Data[0].Models {
				models[m.ID] = m.Pricing
			}
			require.Len(t, response.Data[0].Models, len(tc.public))
			request := func(model string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(fmt.Sprintf(`{"model":%q,"state":"sample","questions":{"urgency":{"type":"noul","instructions":"classify"}}}`, model)))
				req.Header.Set("Content-Type", "application/json")
				out := httptest.NewRecorder()
				r.ServeHTTP(out, req)
				return out
			}
			for i, model := range tc.public {
				price, ok := models[model]
				require.True(t, ok, model)
				require.Equal(t, "priced", price.PriceStatus)
				require.InDelta(t, *group.ModelPricing[i].InputPrice, price.InputPricePerToken, 1e-12)
				require.InDelta(t, *group.ModelPricing[i].OutputPrice, price.OutputPricePerToken, 1e-12)
				out := request(model)
				require.Equal(t, http.StatusOK, out.Code, "%s: %s", model, out.Body.String())
				require.Len(t, observed, 1)
				target := model
				if tc.mapLatest && model == "jev-latest" {
					target = "jev-1.14"
				}
				require.Equal(t, target, <-observed)
				require.Len(t, logs.logs, i+1)
				require.Len(t, bills.commands, i+1)
				log := logs.logs[i]
				input, output := 100+len(target), 10+len(target)
				require.Equal(t, input, log.InputTokens)
				require.Equal(t, output, log.OutputTokens)
				want := float64(input)*price.InputPricePerToken + float64(output)*price.OutputPricePerToken
				require.Equal(t, model, log.Model)
				require.Equal(t, model, log.RequestedModel)
				require.NotNil(t, log.UpstreamModel)
				require.Equal(t, target, *log.UpstreamModel)
				require.InDelta(t, float64(input)*price.InputPricePerToken, log.InputCost, 1e-12)
				require.InDelta(t, float64(output)*price.OutputPricePerToken, log.OutputCost, 1e-12)
				require.InDelta(t, want, log.ActualCost, 1e-12)
				require.InDelta(t, want, bills.commands[i].BillableAmountUSD, 1e-12)
				t.Logf("广场 %s，出站 %s，输入/输出 %d/%d，广场计价 %.8f，结算 %.8f", model, target, input, output, want, bills.commands[i].BillableAmountUSD)
			}
			// 即使通配规则能把它改写到可用账号，也不能调用从未公开的名称并产生费用。
			rejected := request("team-unlisted")
			require.Equal(t, http.StatusNotFound, rejected.Code, rejected.Body.String())
			require.Empty(t, observed)
			require.Len(t, logs.logs, len(tc.public))
			require.Len(t, bills.commands, len(tc.public))
		})
	}
}
