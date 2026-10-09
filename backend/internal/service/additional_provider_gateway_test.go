//go:build unit

package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestClineEnvelopePreservesAnswerAndUsageAcrossIngresses(t *testing.T) {
	for _, ingress := range cnProtocolIngressCases() {
		for _, success := range []bool{true, false} {
			t.Run(ingress.name+"/"+map[bool]string{true: "success", false: "failure"}[success], func(t *testing.T) {
				body := `{"success":true,"data":{"id":"cc1","object":"chat.completion","model":"deepseek-chat","choices":[{"index":0,"message":{"role":"assistant","content":"ANSWER"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":27}}}`
				if !success {
					body = `{"success":false,"error":{"message":"failed"}}`
				}
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}}
				account := adaptiveProtocolTestAccount(PlatformCline, nil)
				account.Credentials["api_protocol"] = APIProtocolChatCompletions
				account.Credentials["base_url"] = "https://relay.example/v1"
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodPost, ingress.path, strings.NewReader(string(ingress.body)))
				var result *OpenAIForwardResult
				var err error
				switch ingress.name {
				case "chat completions":
					result, err = svc.ForwardAsChatCompletions(t.Context(), c, account, ingress.body, "", "")
				case "messages":
					result, err = svc.ForwardAsAnthropic(t.Context(), c, account, ingress.body, "", "")
				default:
					result, err = svc.Forward(t.Context(), c, account, ingress.body)
				}
				if !success {
					require.Error(t, err)
					require.Nil(t, result)
					return
				}
				require.NoError(t, err)
				require.Contains(t, w.Body.String(), "ANSWER")
				require.Equal(t, 8, result.Usage.InputTokens)
				require.Equal(t, 27, result.Usage.OutputTokens)
				require.NotContains(t, w.Body.String(), `"success":true`)
			})
		}
	}
}

// 新平台三类入站走已选上游协议，别名只能映射一次；失败必须保持尚未向客户端写出。
func TestAdditionalProviderGatewayProtocolMatrix(t *testing.T) {
	for _, provider := range []struct{ platform, protocol, path string }{
		{PlatformCline, APIProtocolChatCompletions, "/relay/v1/chat/completions"},
		{PlatformCommandCode, APIProtocolChatCompletions, "/relay/v1/chat/completions"},
		{PlatformCommandCode, APIProtocolResponses, "/relay/v1/responses"},
		{PlatformCommandCode, APIProtocolAnthropic, "/relay/v1/messages"},
	} {
		for _, ingress := range cnProtocolIngressCases() {
			t.Run(provider.platform+"/"+provider.protocol+"/"+ingress.name, func(t *testing.T) {
				account := adaptiveProtocolTestAccount(provider.platform, map[string]any{})
				account.Credentials["api_protocol"] = provider.protocol
				account.Credentials["base_url"] = "http://relay.example/relay/v1"
				account.Credentials["model_mapping"] = map[string]any{"client-model": "vendor/model", "vendor/model": "must-not-map-twice"}
				body := []byte(strings.ReplaceAll(string(ingress.body), "deepseek-chat", "client-model"))
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"temporarily unavailable"}}`))}}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				c := adaptiveProtocolTestContext(ingress.path, body)
				err := ingress.forward(svc, c, account, body)
				require.Error(t, err)
				var retry *UpstreamFailoverError
				require.True(t, errors.As(err, &retry), "%T %v", err, err)
				require.False(t, c.Writer.Written())
				require.NotNil(t, upstream.lastReq)
				require.Equal(t, provider.path, upstream.lastReq.URL.Path)
				require.Equal(t, "relay.example", upstream.lastReq.URL.Host)
				require.Equal(t, "vendor/model", gjson.GetBytes(upstream.lastBody, "model").String())
			})
		}
	}
}

// Command 的 Responses 适配保留完整历史，不能套用 OpenAI 专属续接缓存与尾部裁剪。
func TestCommandCodeMessagesPreservesCompleteHistory(t *testing.T) {
	messages := make([]map[string]string, 16)
	for index := range messages {
		role := "user"
		if index%2 == 1 {
			role = "assistant"
		}
		messages[index] = map[string]string{"role": role, "content": "history"}
	}
	body, err := json.Marshal(map[string]any{"model": "gpt-test", "max_tokens": 32, "messages": messages})
	require.NoError(t, err)
	account := adaptiveProtocolTestAccount(PlatformCommandCode, nil)
	account.Credentials["api_protocol"] = APIProtocolResponses
	account.Credentials["base_url"] = "https://relay.example/v1"
	account.Extra = map[string]any{"openai_responses_continuation_supported": true}
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"unavailable"}}`))}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	c := adaptiveProtocolTestContext("/v1/messages", body)
	_, err = svc.ForwardAsAnthropic(t.Context(), c, account, body, "session", "")
	require.Error(t, err)
	require.Len(t, gjson.GetBytes(upstream.lastBody, "input").Array(), len(messages))
	require.NotContains(t, string(upstream.lastBody), openAICompatClaudeCodeTodoGuardMarker)
	require.False(t, gjson.GetBytes(upstream.lastBody, "previous_response_id").Exists())
}

func TestAdditionalProviderAccountConnectionProbes(t *testing.T) {
	for _, provider := range []struct{ platform, protocol, suffix string }{
		{PlatformCline, APIProtocolChatCompletions, "/chat/completions"},
		{PlatformCommandCode, APIProtocolChatCompletions, "/chat/completions"},
		{PlatformCommandCode, APIProtocolResponses, "/responses"},
		{PlatformCommandCode, APIProtocolAnthropic, "/messages"},
	} {
		t.Run(provider.platform+"/"+provider.protocol, func(t *testing.T) {
			account := openCodeGoTestAccount(811)
			account.Platform = provider.platform
			account.Credentials = map[string]any{"api_key": "test-key", "api_protocol": provider.protocol, "base_url": "https://relay.example/v1", "model_mapping": map[string]any{"client": "vendor/model"}}
			response := adaptiveCNChatTestResponse()
			if provider.protocol == APIProtocolResponses {
				response = adaptiveCNResponsesTestResponse()
			}
			if provider.protocol == APIProtocolAnthropic {
				response = adaptiveCNAnthropicTestResponse()
			}
			svc, upstream := adaptiveCNAccountTestService(account, response)
			c, recorder := newTestContext()
			require.NoError(t, svc.TestAccountConnection(c, account.ID, "client", "hi", AccountTestModeDefault))
			require.Len(t, upstream.requests, 1)
			require.Equal(t, "https://relay.example/v1"+provider.suffix, upstream.requests[0].URL.String())
			require.Equal(t, "vendor/model", gjson.GetBytes(upstream.lastBody, "model").String())
			require.Contains(t, recorder.Body.String(), `"type":"test_complete"`)
			// 渠道的显式协议探针与管理员测试必须使用同一地址，且不改写原账号。
			svc, upstream = adaptiveCNAccountTestService(account, adaptiveCNChatTestResponse())
			result, err := svc.RunTestBackgroundWithPromptAndUserAgentAndProtocol(t.Context(), account.ID, "client", "hi", "", APIProtocolChatCompletions)
			require.NoError(t, err)
			require.Equal(t, "success", result.Status)
			require.Equal(t, "https://relay.example/v1/chat/completions", upstream.requests[0].URL.String())
			require.Equal(t, provider.protocol, account.GetAPIProtocol())
		})
	}
}

func TestAdditionalProviderTestErrorRedactionAndCorrelation(t *testing.T) {
	account := &Account{ID: 99, Platform: PlatformCline, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "arbitrary-provider-secret"}}
	c, recorder := newTestContext()
	bindAccountTestLogContext(c, account.ID, "model", "default", account)
	id := accountTestLogID(c)
	bindAccountTestLogContext(c, account.ID, "model", "default", account)
	err := (&AccountTestService{}).sendErrorAndEnd(c, "upstream echoed arbitrary-provider-secret "+strings.Repeat("x", 5000))
	require.NotContains(t, err.Error(), "arbitrary-provider-secret")
	require.Contains(t, recorder.Body.String(), id)
	require.Less(t, len(err.Error()), 4110)
	require.Equal(t, id, accountTestLogID(c))
}
