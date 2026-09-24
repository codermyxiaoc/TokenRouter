package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 新型号的能力与计费候选共用明确的别名边界，未知型号保持透传。
func TestGPT6SolLunaAliasesDoNotConsumeUnknownVariants(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		t.Run(model, func(t *testing.T) {
			for _, suffix := range []string{"", "-none", "-low", "-medium", "-high", "-xhigh", "-max", "-openai-compact", "-max-openai-compact"} {
				alias := "OPENAI/" + strings.ToUpper(strings.ReplaceAll(model+suffix, "-", "_"))
				require.Equal(t, model, normalizeKnownOpenAICodexModel(alias), alias)
				require.Equal(t, model, normalizeCodexModel(alias), alias)
				require.Contains(t, usageBillingModelCandidates(alias), model)
			}
			for _, suffix := range []string{"-preview", "-pro", "-codex", "-minimal", "-ultra", "-2026-09-22", "-20260922", "-max-extra", "ar"} {
				unknown := model + suffix
				require.Empty(t, normalizeKnownOpenAICodexModel(unknown), unknown)
				require.Equal(t, unknown, normalizeCodexModel(unknown), unknown)
				require.NotContains(t, usageBillingModelCandidates(unknown), model, unknown)
			}
		})
	}
	require.True(t, isOpenAIGPT6SolModel("openai/gpt-6-sol-max"))
	require.False(t, isOpenAIGPT6SolModel("gpt-6-luna"))
	require.True(t, isOpenAIGPT6LunaModel("openai/gpt-6-luna-none"))
	require.False(t, isOpenAIGPT6LunaModel("gpt-6-sol"))
}

// Responses 的新模型工具请求沿用管理员协议策略，并保留 none/max 和调用声明。
func TestGPT6SolLunaResponsesForwardPreservesModelEffortAndTools(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		for _, effort := range []string{"none", "max"} {
			for _, passthrough := range []bool{false, true} {
				name := model + "/" + effort
				if passthrough {
					name += "/passthrough"
				}
				t.Run(name, func(t *testing.T) {
					upstream := &httpUpstreamRecorder{resp: &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"id":"resp_local","output":[],"usage":{"input_tokens":1,"output_tokens":2}}`)),
					}}
					cfg := &config.Config{}
					cfg.Security.URLAllowlist.Enabled = false
					svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
					account := &Account{
						ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
						Credentials: map[string]any{"api_key": "local-test", "base_url": "https://compatible.example.com"},
						Extra:       map[string]any{"openai_passthrough": passthrough, "openai_text_route_mode": "force_responses"},
					}
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
					SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
					body := []byte(`{"model":"` + model + `","reasoning":{"effort":"` + effort + `"},"input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}]}`)
					result, err := svc.Forward(context.Background(), c, account, body)
					require.NoError(t, err)
					require.NotNil(t, result)
					require.NotNil(t, upstream.lastReq)
					require.Equal(t, "/v1/responses", upstream.lastReq.URL.Path)
					require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
					require.Equal(t, effort, gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
					require.Equal(t, "lookup", gjson.GetBytes(upstream.lastBody, "tools.0.name").String())
				})
			}
		}
	}
}

// 新型号的 none 别名转换保留显式关闭推理，旧模型保持历史默认。
func TestGPT6SolLunaNoneAliasSurvivesMessagesBridge(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		req := &apicompat.AnthropicRequest{Model: model + "-none"}
		applyOpenAICompatModelNormalization(req)
		require.Equal(t, model, req.Model)
		require.NotNil(t, req.OutputConfig)
		require.Equal(t, "none", req.OutputConfig.Effort)
	}
	legacy := &apicompat.AnthropicRequest{Model: "gpt-5.4-none"}
	applyOpenAICompatModelNormalization(legacy)
	require.Equal(t, "gpt-5.4", legacy.Model)
	require.Nil(t, legacy.OutputConfig)
}

// JSON 和 WS 对象过滤采用同一边界，不能扩大到未知变体或其它平台。
func TestGPT6SolLunaNoneFilteringOnlyChangesSupportedOpenAIModels(t *testing.T) {
	for _, tc := range []struct {
		platform string
		model    string
		keep     bool
	}{
		{PlatformOpenAI, "gpt-6-sol", true},
		{PlatformOpenAI, "gpt-6-luna", true},
		{PlatformOpenAI, "gpt-6-sol-preview", false},
		{PlatformOpenAI, "gpt-5.4", false},
		{PlatformDeepseek, "gpt-6-sol", false},
	} {
		t.Run(tc.platform+"/"+tc.model, func(t *testing.T) {
			account := &Account{Platform: tc.platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://compatible.example.com"}}
			body := []byte(`{"model":"` + tc.model + `","reasoning":{"effort":"none"}}`)
			filtered, err := filterOpenAIResponsesNoneReasoningEffortForAccount(account, body)
			require.NoError(t, err)
			require.Equal(t, tc.keep, gjson.GetBytes(filtered, "reasoning.effort").Exists())
			obj := map[string]any{"model": tc.model, "reasoning": map[string]any{"effort": "none"}}
			deleteOpenAIResponsesNoneReasoningEffortFromObject(account, obj)
			_, exists := obj["reasoning"]
			require.Equal(t, tc.keep, exists)
		})
	}
}

// 提示缓存会话既稳定又按实际产品分隔，不能把 Sol 与 Luna 串用。
func TestGPT6SolLunaCompatPromptCacheIdentity(t *testing.T) {
	req := &apicompat.ChatCompletionsRequest{Messages: []apicompat.ChatMessage{
		{Role: "user", Content: mustRawJSON(t, `"hello"`)},
	}}
	keys := make(map[string]string)
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		require.True(t, shouldAutoInjectPromptCacheKeyForCompat(model))
		require.False(t, shouldAutoInjectPromptCacheKeyForCompat(model+"-preview"))
		req.Model = model
		keys[model] = deriveCompatPromptCacheKey(req, model)
		require.Equal(t, keys[model], deriveCompatPromptCacheKey(req, "openai/"+model))
	}
	require.NotEqual(t, keys["gpt-6-sol"], keys["gpt-6-luna"])
}
