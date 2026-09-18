//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 三种客户端均根据映射后模型选协议，且不能重复应用映射或提前写出可恢复错误。
func TestOpenCodeGatewayProtocolMatrixAndRecoverableErrors(t *testing.T) {
	for _, mode := range []string{AccountModeZen, AccountModeGo} {
		for _, model := range []string{"gpt-5.6-luna", "qwen3.8-max", "glm-5.3"} {
			for _, ingress := range cnProtocolIngressCases() {
				t.Run(mode+"/"+model+"/"+ingress.name, func(t *testing.T) {
					account := adaptiveProtocolTestAccount(PlatformOpenCodeGo, map[string]any{
						APIProtocolChatCompletions: "http://chat.example/relay/v1",
						APIProtocolResponses:       "http://responses.example/relay/v1",
						APIProtocolAnthropic:       "http://anthropic.example/relay/v1",
					})
					account.Credentials["account_mode"] = mode
					account.Credentials["model_mapping"] = map[string]any{"client-model": model, model: "must-not-map-twice"}
					body := []byte(strings.ReplaceAll(string(ingress.body), "deepseek-chat", "client-model"))
					upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"temporarily unavailable"}}`))}}
					svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
					c := adaptiveProtocolTestContext(ingress.path, body)
					err := ingress.forward(svc, c, account, body)
					require.Error(t, err)
					var failover *UpstreamFailoverError
					require.True(t, errors.As(err, &failover), "%T: %v", err, err)
					require.False(t, c.Writer.Written())
					require.NotNil(t, upstream.lastReq)
					wantHost, wantEndpoint := "chat.example", "/v1/chat/completions"
					if strings.HasPrefix(model, "gpt-") {
						wantHost, wantEndpoint = "responses.example", "/v1/responses"
					}
					if strings.HasPrefix(model, "qwen") {
						wantHost, wantEndpoint = "anthropic.example", "/v1/messages"
					}
					require.Equal(t, wantHost, upstream.lastReq.URL.Host)
					require.Equal(t, "/relay"+wantEndpoint, upstream.lastReq.URL.Path)
					require.True(t, strings.HasSuffix(GetActualOpenAIUpstreamEndpoint(c), wantEndpoint), GetActualOpenAIUpstreamEndpoint(c))
					require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
					if mode == AccountModeGo {
						require.NotEmpty(t, upstream.lastReq.Header.Get(openCodeSessionHeader))
					}
				})
			}
		}
	}
}

// 同会话跨请求稳定、跨 API Key 隔离，协议转换不得覆盖原始会话线索。
func TestOpenCodeSessionTenantIsolationAndOriginalBody(t *testing.T) {
	account := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey}
	values := make([]string, 0, 3)
	for _, keyID := range []int64{1, 1, 2} {
		c := newOpenCodeSessionTestContext(t, "")
		c.Set("api_key", &APIKey{ID: keyID})
		rememberOpenCodeInboundBody(c, []byte(`{"prompt_cache_key":"shared-session"}`))
		rememberOpenCodeInboundBody(c, []byte(`{"prompt_cache_key":"converted-session"}`))
		headers := http.Header{}
		applyOpenCodeSessionHeader(c, account, "https://relay.example/v1/responses", headers)
		values = append(values, headers.Get(openCodeSessionHeader))
		require.Equal(t, isolateOpenAISessionID(keyID, "shared-session"), headers.Get(openCodeSessionHeader))
	}
	require.Equal(t, values[0], values[1])
	require.NotEqual(t, values[0], values[2])
	c := newOpenCodeSessionTestContext(t, "")
	first, second := http.Header{}, http.Header{}
	applyOpenCodeSessionHeader(c, account, "https://relay.example/v1/responses", first)
	applyOpenCodeSessionHeader(c, account, "https://relay.example/v1/messages", second)
	require.NotEmpty(t, first.Get(openCodeSessionHeader))
	require.Equal(t, first.Get(openCodeSessionHeader), second.Get(openCodeSessionHeader))
}

// 新平台只加入文本协议，智能路由继续要求有限模型目录，媒体与空模型保持排除。
func TestOpenCodeSmartRoutingAndAccountValidation(t *testing.T) {
	account := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	require.NoError(t, normalizeCNProviderCredentials(account, true))
	require.Equal(t, AccountModeGo, account.GetOpenCodeAccountMode())
	require.True(t, account.IsOpenAICompatible())
	require.True(t, smartRoutingAccountCatalogContains(account, "glm-5.3"))
	for _, model := range []string{"", "unknown-model", "gpt-image-2"} {
		require.False(t, smartRoutingAccountCatalogContains(account, model))
	}
	for _, endpoint := range []string{"/v1/images/generations", "/v1/videos", "/v1/embeddings"} {
		require.False(t, smartRoutingGroupEndpointEligible(PlatformOpenCodeGo, endpoint))
	}
	require.True(t, smartRoutingAccountEndpointEligible(context.Background(), account, "glm-5.3", "/v1/responses"))
	account.Type = AccountTypeOAuth
	require.Error(t, normalizeCNProviderCredentials(account, true))
	account.Type = AccountTypeAPIKey
	account.Credentials["protocol_rules"] = []any{map[string]any{"pattern": "gpt-\n*", "protocol": APIProtocolResponses}}
	require.Error(t, normalizeCNProviderCredentials(account, true))
}

// Zen 真实 Claude 可以使用系统默认价，映射为其它家族的 Claude 别名不能误收费。
func TestOpenCodeBillingNativeClaudeAndAliases(t *testing.T) {
	ctx := context.Background()
	billing := NewBillingService(&config.Config{}, nil)
	svc := &OpenAIGatewayService{billingService: billing, resolver: NewModelPricingResolver(nil, billing)}
	account := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{"account_mode": AccountModeZen}}
	key := &APIKey{Group: &Group{ID: 1, Platform: PlatformOpenCodeGo}}
	models := svc.filterCNProviderBillingModelCandidates(ctx, account, key, []string{"claude-sonnet-4-5"}, "claude-sonnet-4-5")
	require.Equal(t, []string{"claude-sonnet-4-5"}, models)
	cost, err := svc.calculateOpenAIRecordUsageCost(ctx, nil, key, models, 1, 1, 1, 1, UsageTokens{InputTokens: 1000, OutputTokens: 100}, "")
	require.NoError(t, err)
	require.Greater(t, cost.ActualCost, 0.0)
	models = svc.filterCNProviderBillingModelCandidates(ctx, account, key, []string{"claude-sonnet-4-5", "glm-5.3"}, "glm-5.3")
	require.Equal(t, []string{"glm-5.3"}, models)
}
