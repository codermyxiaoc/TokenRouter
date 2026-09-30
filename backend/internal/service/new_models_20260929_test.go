//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 覆盖目录、缺失条目及离线三种价源，新模型不得借用上一代缓存价格。
func TestSeptember29ModelPricing(t *testing.T) {
	for source, catalog := range newSeptemberModelPricingSources(t) {
		t.Run(source, func(t *testing.T) {
			billing := NewBillingService(&config.Config{}, catalog)
			for _, model := range []string{"gpt-6.1-sol", "claude-sonnet-5-5"} {
				price, err := billing.GetModelPricing(model)
				require.NoError(t, err)
				require.InDelta(t, 2e-6, price.InputPricePerToken, 1e-15)
				require.InDelta(t, 10e-6, price.OutputPricePerToken, 1e-15)
				require.InDelta(t, 2.5e-6, price.CacheCreationPricePerToken, 1e-15)
				if model == "gpt-6.1-sol" {
					require.InDelta(t, 0.1e-6, price.CacheReadPricePerToken, 1e-15)
					require.Equal(t, 272000, price.LongContextInputThreshold)
					for _, n := range []int{272000, 272001} {
						cost, err := billing.CalculateCostWithServiceTier(model, UsageTokens{InputTokens: n, OutputTokens: 100, CacheReadTokens: 0}, 1, "flex")
						require.NoError(t, err)
						factor := 1.0
						if n > 272000 {
							factor = 2
						}
						require.InDelta(t, float64(n)*2e-6*0.5*factor, cost.InputCost, 1e-12)
					}
				} else {
					require.InDelta(t, 0.2e-6, price.CacheReadPricePerToken, 1e-15)
					require.InDelta(t, 4e-6, price.CacheCreation1hPrice, 1e-15)
					require.Zero(t, price.LongContextInputThreshold)
				}
				zero, err := billing.GetModelPricingWithChannel(model, &ChannelModelPricing{BillingMode: BillingModeToken, InputPrice: float64Ptr(0), OutputPrice: float64Ptr(0), CacheWritePrice: float64Ptr(0), CacheReadPrice: float64Ptr(0)})
				require.NoError(t, err)
				require.Zero(t, zero.InputPricePerToken)
				require.Zero(t, zero.CacheCreationPricePerToken)
			}
		})
	}
}

// 外部价卡的显式零价要与缺失字段区分，Fast 不得自动补回收费价格。
func TestSeptember29GPT61ExplicitCatalogZero(t *testing.T) {
	catalog := &PricingService{}
	data, err := catalog.parsePricingData([]byte(`{"gpt-6.1-sol":{"input_cost_per_token":0.7,"output_cost_per_token":0.8,"cache_creation_input_token_cost":0,"cache_read_input_token_cost":0,"input_cost_per_token_priority":0,"output_cost_per_token_priority":0,"cache_creation_input_token_cost_priority":0,"cache_read_input_token_cost_priority":0,"supports_service_tier":true}}`))
	require.NoError(t, err)
	catalog.pricingData = data
	billing := NewBillingService(&config.Config{}, catalog)
	price, err := billing.GetModelPricing("gpt-6.1-sol")
	require.NoError(t, err)
	require.Zero(t, price.CacheCreationPricePerToken)
	cost, err := billing.CalculateCostWithServiceTier("gpt-6.1-sol", UsageTokens{InputTokens: 100, OutputTokens: 100, CacheCreationTokens: 100, CacheReadTokens: 100}, 1, "priority")
	require.NoError(t, err)
	require.Zero(t, cost.ActualCost)
}

// Ultrafast 按最终普通价计算六倍；Fast 渠道倍率与显式零价保持独立。
func TestSeptember29AstraUltrafast(t *testing.T) {
	billing := NewBillingService(&config.Config{}, nil)
	for _, fast := range []float64{0, 3} {
		base := &ModelPricing{InputPricePerToken: 0.7, OutputPricePerToken: 0, CacheReadPricePerToken: 0.2, CacheCreationPricePerToken: 0.3, FastModeMultiplier: &fast}
		price := billing.applyModelSpecificPricingPolicyEx("gpt-6-astra-high", base, false)
		cost := billing.computeTokenBreakdown(price, UsageTokens{InputTokens: 1, OutputTokens: 1, CacheReadTokens: 1, CacheCreationTokens: 1}, 1, "ultrafast", false)
		require.InDelta(t, 4.2, cost.InputCost, 1e-12)
		require.Zero(t, cost.OutputCost)
		require.InDelta(t, 1.2, cost.CacheReadCost, 1e-12)
		require.InDelta(t, 1.8, cost.CacheCreationCost, 1e-12)
		require.Zero(t, base.UltrafastMultiplier)
	}
	for _, observed := range []string{"priority", "fast", "default", "flex"} {
		r := ResolveBillingServiceTier("ultrafast", observed)
		require.True(t, r.Downgraded)
		require.Equal(t, observed, r.Billing)
	}
	tier := "ultrafast"
	result := &OpenAIForwardResult{ServiceTier: &tier, UpstreamResponseServiceTier: "default"}
	require.False(t, ApplyOpenAIServiceTierBillingResolution(&Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, result).Downgraded)
	require.Equal(t, "ultrafast", *result.ServiceTier)
	// 区间单价在所有模型政策之后仍是基础价，不能退回 Astra 官方单价。
	intervalCost, err := billing.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "gpt-6-astra", Tokens: UsageTokens{InputTokens: 100, OutputTokens: 100},
		RateMultiplier: 2, ServiceTier: "ultrafast", Resolver: NewModelPricingResolver(nil, billing),
		Resolved: &ResolvedPricing{Mode: BillingModeToken, Source: PricingSourceChannel,
			BasePricing: &ModelPricing{InputPricePerToken: 99, OutputPricePerToken: 99, FastModeMultiplier: float64Ptr(0)},
			Intervals:   []PricingInterval{{InputPrice: float64Ptr(0.01), OutputPrice: float64Ptr(0)}},
		},
	})
	require.NoError(t, err)
	require.InDelta(t, 6, intervalCost.InputCost, 1e-12)
	require.Zero(t, intervalCost.OutputCost)
	require.InDelta(t, 12, intervalCost.ActualCost, 1e-12)
}

// 别名不能绕过 GPT 6.1 Sol 的最低推理档位，未知产品后缀不归入其价卡。
func TestSeptember29GPT61Reasoning(t *testing.T) {
	require.True(t, shouldAutoInjectPromptCacheKeyForCompat("gpt-6.1-sol"))
	require.False(t, shouldAutoInjectPromptCacheKeyForCompat("gpt-6.1-sol-preview"))
	for _, suffix := range []string{"", "-low", "-xhigh", "-max"} {
		require.Equal(t, "gpt-6.1-sol", normalizeCodexModel("openai/gpt-6.1-sol"+suffix))
	}
	require.Empty(t, normalizeKnownOpenAICodexModel("gpt-6.1-sol-preview"))
	for _, effort := range []string{"none", "minimal"} {
		require.Error(t, validateOpenAIReasoningEffort([]byte(`{"reasoning":{"effort":"`+effort+`"}}`), "alias", "gpt-6.1-sol"))
		require.Error(t, validateOpenAIReasoningEffort([]byte(`{}`), "gpt-6.1-sol-"+effort))
	}
	require.NoError(t, validateOpenAIReasoningEffort([]byte(`{"reasoning":{"effort":"max"}}`), "gpt-6.1-sol"))
}

// native 历史签名、重试和 Bedrock 兼容转换必须一致使用 Sonnet 5.5 能力。
func TestSeptember29SonnetNativeCompatibility(t *testing.T) {
	valid := []byte(`{"model":"alias","thinking":{"type":"between_tools"},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"ok","signature":"valid"},{"type":"text","text":"hi"}]}]}`)
	require.Equal(t, valid, FilterThinkingBlocks(valid, "claude-sonnet-5-5"))
	for _, retry := range []func([]byte, ...string) []byte{FilterThinkingBlocksForRetry, FilterSignatureSensitiveBlocksForRetry} {
		require.Equal(t, "between_tools", gjson.GetBytes(retry(valid, "claude-sonnet-5-5"), "thinking.type").String())
	}
	invalid := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","signature":"valid"},{"type":"thinking","thinking":"invalid"},{"type":"text","text":"hi"}]}]}`)
	filtered := FilterThinkingBlocks(invalid, "claude-sonnet-5-5")
	require.Equal(t, 1, int(gjson.GetBytes(filtered, "messages.0.content.#").Int()))
	toolset := []byte(`{"tools":[{"type":"computer_toolset_20260801"}]}`)
	require.Equal(t, "other-beta", filterSonnet55ToolsetBeta(claude.BetaFineGrainedToolStreaming+",other-beta", toolset, "claude-sonnet-5-5"))
	body := []byte(`{"thinking":{"type":"enabled","budget_tokens":1000},"output_config":{"effort":"high","format":{"type":"json_schema","schema":{"type":"object"}}},"messages":[{"role":"user","content":"hello"}]}`)
	got, err := PrepareBedrockRequestBodyWithTokens(body, "global.anthropic.claude-sonnet-5-5", nil, true)
	require.NoError(t, err)
	require.Equal(t, "adaptive", gjson.GetBytes(got, "thinking.type").String())
	require.Equal(t, "high", gjson.GetBytes(got, "output_config.effort").String())
	require.False(t, gjson.GetBytes(got, "output_config.format").Exists())
	require.Contains(t, gjson.GetBytes(got, "messages.0.content.1.text").String(), "object")
}

// 原生 Messages 与计数入口必须在任何上游传输前拒绝映射型号的不兼容参数。
func TestSeptember29SonnetNativeValidationBeforeForward(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"alias": "claude-sonnet-5-5"}}}
	for _, countTokens := range []bool{false, true} {
		parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"alias","temperature":0.5,"messages":[{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
		require.NoError(t, err)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		svc := &GatewayService{}
		if countTokens {
			err = svc.ForwardCountTokens(context.Background(), c, account, parsed)
		} else {
			_, err = svc.Forward(context.Background(), c, account, parsed)
		}
		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "non-default temperature")
	}
}

// 用模拟上游检查真实协议桥，确保别名决定能力且不触发真实模型请求。
func TestSeptember29SonnetMappedForwarding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, protocol := range []string{"responses", "chat/completions"} {
		for _, effort := range []string{"none", "max"} {
			t.Run(protocol+"/"+effort, func(t *testing.T) {
				body := []byte(`{"model":"public-alias","input":"hello","reasoning":{"effort":"` + effort + `"}}`)
				if protocol == "chat/completions" {
					body = []byte(`{"model":"public-alias","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"` + effort + `"}`)
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+protocol, nil)
				upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"capture"}}`))}}
				cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
				svc := &GatewayService{cfg: cfg, responseHeaderFilter: compileResponseHeaderFilter(cfg), httpUpstream: upstream}
				account := &Account{ID: 502, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key", "model_mapping": map[string]any{"public-alias": "claude-sonnet-5-5"}}}
				if protocol == "responses" {
					_, _ = svc.ForwardAsResponses(context.Background(), c, account, body, nil)
				} else {
					_, _ = svc.ForwardAsChatCompletions(context.Background(), c, account, body, nil)
				}
				require.NotEmpty(t, upstream.lastBody)
				require.Equal(t, "claude-sonnet-5-5", gjson.GetBytes(upstream.lastBody, "model").String())
				want := "adaptive"
				if effort == "none" {
					want = "between_tools"
				}
				require.Equal(t, want, gjson.GetBytes(upstream.lastBody, "thinking.type").String())
			})
		}
	}
}

// GPT 6.1 经过账号映射和真实 Responses 转发；非法档位在传输前拒绝。
func TestSeptember29GPT61MappedForwarding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, effort := range []string{"none", "minimal", "max"} {
		t.Run(effort, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"resp_local","output":[],"usage":{"input_tokens":1,"output_tokens":2}}`))}}
			cfg := &config.Config{}
			cfg.Security.URLAllowlist.Enabled = false
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key", "base_url": "https://compatible.example.com", "model_mapping": map[string]any{"public-alias": "gpt-6.1-sol"}}, Extra: map[string]any{"openai_text_route_mode": "force_responses"}}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"public-alias","input":"hello","reasoning":{"effort":"`+effort+`"}}`))
			if effort != "max" {
				require.Error(t, err)
				require.Equal(t, 400, w.Code)
				require.Nil(t, upstream.lastReq)
			} else {
				require.NoError(t, err)
				require.Equal(t, "gpt-6.1-sol", gjson.GetBytes(upstream.lastBody, "model").String())
				require.Equal(t, "max", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
			}
		})
	}
}
