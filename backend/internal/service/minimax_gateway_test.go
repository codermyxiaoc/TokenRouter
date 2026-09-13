//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// MiniMax 的三种客户端协议应使用各自的端点，账号模式和模型映射不能改变平台身份。
func TestMiniMaxGatewayAdaptiveProtocols(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{AccountModePayG, AccountModeCoding} {
		for _, ingress := range cnProtocolIngressCases() {
			t.Run(mode+"/"+ingress.name, func(t *testing.T) {
				account := adaptiveProtocolTestAccount(PlatformMiniMax, map[string]any{
					APIProtocolChatCompletions: "http://chat.example/minimax/v1",
					APIProtocolAnthropic:       "http://anthropic.example/minimax/anthropic",
					APIProtocolResponses:       "http://responses.example/minimax/v1",
				})
				account.Credentials["account_mode"] = mode
				account.Credentials["model_mapping"] = map[string]any{"client-model": "MiniMax-M2.7"}
				body := []byte(strings.ReplaceAll(string(ingress.body), "deepseek-chat", "client-model"))
				upstream := &httpUpstreamRecorder{err: errors.New("仅捕获上游请求")}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				err := ingress.forward(svc, adaptiveProtocolTestContext(ingress.path, body), account, body)
				require.Error(t, err)
				require.NotNil(t, upstream.lastReq)
				wantURL := map[string]string{
					"chat completions": "http://chat.example/minimax/v1/chat/completions",
					"messages":         "http://anthropic.example/minimax/anthropic/v1/messages",
					"responses":        "http://responses.example/minimax/v1/responses",
				}[ingress.name]
				require.Equal(t, wantURL, upstream.lastReq.URL.String())
				require.Equal(t, "MiniMax-M2.7", gjson.GetBytes(upstream.lastBody, "model").String())
				if ingress.name == "messages" {
					require.Equal(t, "sk-test", getHeaderRaw(upstream.lastReq.Header, "x-api-key"))
					require.Empty(t, upstream.lastReq.Header.Get("Authorization"))
				} else {
					require.Equal(t, "Bearer sk-test", upstream.lastReq.Header.Get("Authorization"))
				}
				require.Equal(t, PlatformMiniMax, NormalizeOpenAICompatiblePlatform(account.Platform))
			})
		}
	}
}

// 管理员固定协议时，Responses 客户端沿既有转换器发送给相应 MiniMax 端点。
func TestMiniMaxGatewayFixedProtocolResponsesBridge(t *testing.T) {
	for _, protocol := range []string{APIProtocolChatCompletions, APIProtocolAnthropic, APIProtocolResponses} {
		t.Run(protocol, func(t *testing.T) {
			account := adaptiveProtocolTestAccount(PlatformMiniMax, nil)
			account.Credentials["api_protocol"] = protocol
			baseURL := "http://relay.example/minimax/v1"
			wantPath := "/minimax/v1/responses"
			wantInput := "input"
			if protocol == APIProtocolChatCompletions {
				wantPath, wantInput = "/minimax/v1/chat/completions", "messages"
			} else if protocol == APIProtocolAnthropic {
				baseURL = "http://relay.example/minimax/anthropic"
				wantPath, wantInput = "/minimax/anthropic/v1/messages", "messages"
			}
			account.Credentials["base_url"] = baseURL
			body := []byte(`{"model":"MiniMax-M2.7","input":"hello","max_output_tokens":64,"stream":false}`)
			upstream := &httpUpstreamRecorder{err: errors.New("仅捕获上游请求")}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			_, err := svc.Forward(context.Background(), adaptiveProtocolTestContext("/v1/responses", body), account, body)
			require.Error(t, err)
			require.NotNil(t, upstream.lastReq)
			require.Equal(t, wantPath, upstream.lastReq.URL.Path)
			require.True(t, gjson.GetBytes(upstream.lastBody, wantInput).Exists())
			require.Equal(t, "MiniMax-M2.7", gjson.GetBytes(upstream.lastBody, "model").String())
			if protocol == APIProtocolResponses {
				// 原生 Responses 必须保留调用者设置的生成上限。
				require.Equal(t, int64(64), gjson.GetBytes(upstream.lastBody, "max_output_tokens").Int())
			}
		})
	}
}

// MiniMax 的流式与非流式成功响应均需保留现有用量结算入口。
func TestMiniMaxGatewayMessagesUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			account := adaptiveProtocolTestAccount(PlatformMiniMax, map[string]any{
				APIProtocolAnthropic: "http://relay.example/anthropic",
			})
			response := nativeAnthropicBufferedResponse()
			if stream {
				response = nativeAnthropicStreamResponse()
			}
			upstream := &httpUpstreamRecorder{resp: response}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			body := []byte(fmt.Sprintf(`{"model":"MiniMax-M2.7","messages":[{"role":"user","content":"hi"}],"max_tokens":64,"stream":%t}`, stream))
			result, err := svc.ForwardAsAnthropic(context.Background(), adaptiveProtocolTestContext("/v1/messages", body), account, body, "", "")
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 93, result.Usage.InputTokens)
			require.Equal(t, 16, result.Usage.OutputTokens)
		})
	}
}

// MiniMax 原生与转换后的 Anthropic 请求都应复用既有的 adaptive 思考兼容规则。
func TestMiniMaxGatewayAnthropicThinkingAcrossIngresses(t *testing.T) {
	for _, ingress := range cnProtocolIngressCases() {
		t.Run(ingress.name, func(t *testing.T) {
			account := adaptiveProtocolTestAccount(PlatformMiniMax, nil)
			account.Credentials["api_protocol"] = APIProtocolAnthropic
			account.Credentials["base_url"] = "http://relay.example/anthropic"
			body := []byte(strings.ReplaceAll(string(ingress.body), "deepseek-chat", "MiniMax-M2.7"))
			field, value := "reasoning.effort", "high"
			switch ingress.name {
			case "messages":
				field, value = "thinking.type", "enabled"
			case "chat completions":
				field = "reasoning_effort"
			}
			body, err := sjson.SetBytes(body, field, value)
			require.NoError(t, err)
			upstream := &httpUpstreamRecorder{err: errors.New("仅捕获思考协议请求")}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			require.Error(t, ingress.forward(svc, adaptiveProtocolTestContext(ingress.path, body), account, body))
			require.NotNil(t, upstream.lastReq)
			require.Equal(t, "/anthropic/v1/messages", upstream.lastReq.URL.Path)
			require.Equal(t, "adaptive", gjson.GetBytes(upstream.lastBody, "thinking.type").String())
		})
	}
}

// 空模型连通性测试必须使用 MiniMax 模型，且自适应模式一次验证三个原生端点。
func TestMiniMaxAccountProbeUsesProviderModel(t *testing.T) {
	account := adaptiveCNAccountTestAccount(730, PlatformMiniMax)
	svc, upstream := adaptiveCNAccountTestService(account,
		adaptiveCNChatTestResponse(), adaptiveCNAnthropicTestResponse(), adaptiveCNResponsesTestResponse())
	c, recorder := newTestContext()
	require.NoError(t, svc.TestAccountConnection(c, account.ID, "", "hi", AccountTestModeDefault))
	require.Len(t, upstream.requests, 3)
	for _, body := range upstream.bodies {
		require.Equal(t, "MiniMax-M2.7", gjson.GetBytes(body, "model").String())
	}
	require.Contains(t, recorder.Body.String(), `"type":"test_complete"`)
	require.Equal(t, http.StatusOK, recorder.Code)
}

// 智能路由的模型目录与平台资格沿用 MiniMax 身份，不能把无关模型或媒体路由选入。
func TestMiniMaxSmartRoutingCatalogAndEndpoints(t *testing.T) {
	account := &Account{Platform: PlatformMiniMax, Type: AccountTypeAPIKey}
	require.True(t, smartRoutingAccountCatalogContains(account, "MiniMax-M2.7"))
	require.False(t, smartRoutingAccountCatalogContains(account, "gpt-6-astra"))
	require.True(t, smartRoutingGroupEndpointEligible(PlatformMiniMax, "/v1/responses/input_tokens"))
	require.False(t, smartRoutingGroupEndpointEligible(PlatformMiniMax, "/v1/images/generations"))
	require.False(t, smartRoutingGroupEndpointEligible(PlatformMiniMax, "/v1/embeddings"))
}
