//go:build unit

package service

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/openai_compat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 新入口独立验证端点选择，避免仅覆盖渠道探测而漏掉管理员手动测试。
func TestAccountTestService_EndpointCNSelectionUsesOneConfiguredProtocol(t *testing.T) {
	for _, platform := range []string{PlatformKimi, PlatformDeepseek, PlatformMiniMax, PlatformZhipu} {
		for _, endpoint := range []string{APIProtocolChatCompletions, APIProtocolResponses, APIProtocolAnthropic} {
			if platform == PlatformZhipu && endpoint == APIProtocolResponses {
				continue
			}
			t.Run(platform+"/"+endpoint, func(t *testing.T) {
				account := adaptiveCNAccountTestAccount(9701, platform)
				account.Credentials["model_mapping"] = map[string]any{"visible-model": "real-model"}
				account.Credentials["header_override_enabled"] = true
				account.Credentials["header_overrides"] = map[string]any{"X-Test-Tenant": "tenant-a"}
				before, err := json.Marshal(account)
				require.NoError(t, err)
				response, wantURL := accountEndpointResponse(endpoint), "http://chat.example/v1/chat/completions"
				switch endpoint {
				case APIProtocolResponses:
					wantURL = "http://responses.example/v1/responses"
					if platform == PlatformDeepseek {
						wantURL = "http://responses.example/responses"
					}
				case APIProtocolAnthropic:
					wantURL = "http://anthropic.example/v1/messages"
				}
				svc, upstream := adaptiveCNAccountTestService(account, response)
				c, recorder := newTestContext()
				err = svc.TestAccountConnectionWithOptions(c, account.ID, "visible-model", "unique endpoint prompt", AccountTestTypeText, AccountTestModeDefault, endpoint, AccountTestOptions{})
				require.NoError(t, err)
				require.Len(t, upstream.requests, 1)
				require.Equal(t, wantURL, upstream.lastReq.URL.String())
				require.Equal(t, "real-model", gjson.GetBytes(upstream.lastBody, "model").String())
				require.Contains(t, string(upstream.lastBody), "unique endpoint prompt")
				require.Equal(t, "tenant-a", getHeaderRaw(upstream.lastReq.Header, "x-test-tenant"))
				assertAccountEndpointAuthentication(t, upstream.lastReq, endpoint, "sk-adaptive-test")
				require.Contains(t, recorder.Body.String(), `"success":true`)
				require.Equal(t, 1, strings.Count(recorder.Body.String(), `"type":"test_complete"`))
				after, err := json.Marshal(account)
				require.NoError(t, err)
				require.JSONEq(t, string(before), string(after), "单次端点选择不得改变原账号及嵌套协议地址")
			})
		}
	}
}

func TestAccountTestService_EndpointCompatibleAPIKeysPreservePrefixAndTextType(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformAnthropic} {
		for _, endpoint := range []string{APIProtocolChatCompletions, APIProtocolResponses, APIProtocolAnthropic, "messages"} {
			t.Run(platform+"/"+endpoint, func(t *testing.T) {
				account := &Account{
					ID: 9702, Platform: platform, Type: AccountTypeAPIKey, Concurrency: 1,
					Credentials: map[string]any{"api_key": "test-compatible", "base_url": "https://relay.example/prefix/v1"},
					Extra:       map[string]any{openai_compat.ExtraKeyTextRouteMode: string(openai_compat.TextRouteModeForceChatCompletions)},
				}
				canonical := endpoint
				if canonical == "messages" {
					canonical = APIProtocolAnthropic
				}
				wantPath := "/prefix/v1/responses"
				if canonical == APIProtocolAnthropic {
					wantPath = "/prefix/v1/messages"
				} else if canonical == APIProtocolChatCompletions {
					wantPath = "/prefix/v1/chat/completions"
				}
				svc, upstream := adaptiveCNAccountTestService(account, accountEndpointResponse(canonical))
				c, recorder := newTestContext()
				// 图片名称不应绕过显式文字测试或偷偷改成昂贵的生图请求。
				err := svc.TestAccountConnectionWithOptions(c, account.ID, "gpt-image-2", "text only", AccountTestTypeText, AccountTestModeDefault, endpoint, AccountTestOptions{})
				require.NoError(t, err)
				require.Len(t, upstream.requests, 1)
				require.Equal(t, wantPath, upstream.lastReq.URL.Path)
				require.Equal(t, "gpt-image-2", gjson.GetBytes(upstream.lastBody, "model").String())
				require.Contains(t, string(upstream.lastBody), "text only")
				assertAccountEndpointAuthentication(t, upstream.lastReq, canonical, "test-compatible")
				require.Contains(t, recorder.Body.String(), `"success":true`)
				require.Equal(t, string(openai_compat.TextRouteModeForceChatCompletions), account.Extra[openai_compat.ExtraKeyTextRouteMode])
				require.NotContains(t, account.Credentials, "api_protocol")
			})
		}
	}
}

func TestAccountTestService_EndpointOpenCodeExplicitSelectionWinsModelRouting(t *testing.T) {
	for _, endpoint := range []string{APIProtocolChatCompletions, APIProtocolResponses, APIProtocolAnthropic} {
		t.Run(endpoint, func(t *testing.T) {
			account := openCodeGoTestAccount(9703)
			baseURLs := account.Credentials["api_base_urls"].(map[string]any)
			baseURLs[endpoint] = "https://selected.example/relay/v1"
			// 对目录默认选 Messages 或 Responses 的模型，手动选择仍必须命中所选协议。
			model := "minimax-m3"
			if endpoint == APIProtocolAnthropic {
				model = "grok-4.6"
			}
			svc, upstream := adaptiveCNAccountTestService(account, accountEndpointResponse(endpoint))
			c, recorder := newTestContext()
			err := svc.TestAccountConnectionWithOptions(c, account.ID, model, "chosen endpoint", AccountTestTypeText, AccountTestModeDefault, endpoint, AccountTestOptions{})
			require.NoError(t, err)
			require.Len(t, upstream.requests, 1)
			wantSuffix := "/responses"
			if endpoint == APIProtocolChatCompletions {
				wantSuffix = "/chat/completions"
			} else if endpoint == APIProtocolAnthropic {
				wantSuffix = "/messages"
			}
			require.Equal(t, "https://selected.example/relay/v1"+wantSuffix, upstream.lastReq.URL.String())
			require.NotEmpty(t, upstream.lastReq.Header.Get("X-OpenCode-Session"))
			require.Contains(t, recorder.Body.String(), `"success":true`)
			require.Equal(t, APIProtocolAdaptive, account.Credentials["api_protocol"])
			require.Equal(t, "https://opencode.ai/zen/go/v1", account.Credentials["base_url"])
		})
	}
}

func TestAccountTestService_EndpointAutoKeepsAdaptiveDiagnosis(t *testing.T) {
	for _, endpoint := range []string{"", "auto"} {
		t.Run(endpoint, func(t *testing.T) {
			account := adaptiveCNAccountTestAccount(9704, PlatformMiniMax)
			svc, upstream := adaptiveCNAccountTestService(account, adaptiveCNChatTestResponse(), adaptiveCNAnthropicTestResponse(), adaptiveCNResponsesTestResponse())
			c, recorder := newTestContext()
			err := svc.TestAccountConnectionWithOptions(c, account.ID, "MiniMax-M2.7", "auto prompt", AccountTestTypeText, AccountTestModeDefault, endpoint, AccountTestOptions{})
			require.NoError(t, err)
			require.Len(t, upstream.requests, 3)
			require.Contains(t, recorder.Body.String(), `"success":true`)
			require.Equal(t, 1, strings.Count(recorder.Body.String(), `"type":"test_complete"`))
		})
	}
}

func TestAccountTestService_EndpointAutoPreservesLegacyImageCalls(t *testing.T) {
	for _, tt := range []struct{ name, model, mode string }{
		{"legacy mode image", "custom-image-alias", "image"},
		{"legacy model inference", "gpt-image-2", AccountTestModeDefault},
	} {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{ID: 9710, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{"api_key": "legacy-image-test", "base_url": "https://image.example/proxy/v1"}}
			svc, upstream := adaptiveCNAccountTestService(account, newJSONResponse(http.StatusOK, `{"data":[{"b64_json":"aGVsbG8="}]}`))
			c, recorder := newTestContext()
			// 旧客户端不携带 test_type，新入口仍须保留旧 mode 和图片模型名的识别。
			err := svc.TestAccountConnectionWithOptions(c, account.ID, tt.model, "legacy picture prompt", "", tt.mode, "auto", AccountTestOptions{})
			require.NoError(t, err)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, "/proxy/v1/images/generations", upstream.lastReq.URL.Path)
			require.Equal(t, tt.model, gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, "legacy picture prompt", gjson.GetBytes(upstream.lastBody, "prompt").String())
			require.Contains(t, recorder.Body.String(), `"type":"image"`)
			require.Contains(t, recorder.Body.String(), `"success":true`)
		})
	}
}

func TestAccountTestService_EndpointOpenCodeSystemOne(t *testing.T) {
	account := openCodeGoTestAccount(9708)
	account.Credentials["account_mode"] = AccountModeZen
	account.Credentials["api_base_urls"].(map[string]any)[APIProtocolSystemOne] = "https://jev.example/zen/v1"
	svc, upstream := adaptiveCNAccountTestService(account, newJSONResponse(http.StatusOK, `{"answers":{"test":null}}`))
	svc.openAIGatewayService = &OpenAIGatewayService{cfg: svc.cfg}
	c, recorder := newTestContext()
	err := svc.TestAccountConnectionWithOptions(c, account.ID, "jev-1.13", "jev test", AccountTestTypeText, AccountTestModeDefault, APIProtocolSystemOne, AccountTestOptions{})
	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "https://jev.example/zen/v1/systemone", upstream.lastReq.URL.String())
	require.Equal(t, "application/json", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, "jev test", gjson.GetBytes(upstream.lastBody, "state").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "questions.test").Exists())
	require.NotContains(t, string(upstream.lastBody), `"messages"`)
	require.Contains(t, recorder.Body.String(), `"success":true`)
	// Jev 的 JSON 协议不可伪装成文本流；拒绝时不能向上游额外发请求。
	c, _ = newTestContext()
	err = svc.TestAccountConnectionWithOptions(c, account.ID, "jev-1.13", "hi", AccountTestTypeText, AccountTestModeDefault, APIProtocolResponses, AccountTestOptions{})
	require.Error(t, err)
	require.Len(t, upstream.requests, 1)
}

func TestAccountTestService_EndpointFailureStaysVisibleWithoutPoisoningAccount(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			account := adaptiveCNAccountTestAccount(9705, PlatformKimi)
			resp := newJSONResponse(status, `{"error":{"message":"selected messages endpoint unavailable"}}`)
			svc, upstream := adaptiveCNAccountTestService(account, resp)
			c, recorder := newTestContext()
			err := svc.TestAccountConnectionWithOptions(c, account.ID, "kimi-k2.5", "hi", AccountTestTypeText, AccountTestModeDefault, APIProtocolAnthropic, AccountTestOptions{})
			require.Error(t, err)
			require.Len(t, upstream.requests, 1)
			require.Contains(t, recorder.Body.String(), "selected messages endpoint unavailable")
			require.NotContains(t, recorder.Body.String(), `"success":true`)
			require.Zero(t, svc.accountRepo.(*openAIAccountTestRepo).setErrorID)
		})
	}
}

func TestAccountTestService_EndpointRejectsUnsupportedBeforeUpstream(t *testing.T) {
	tests := []struct{ name, platform, accountType, endpoint, mode string }{
		{"unknown endpoint", PlatformOpenAI, AccountTypeAPIKey, "typo", ""},
		{"openai oauth chat", PlatformOpenAI, AccountTypeOAuth, APIProtocolChatCompletions, ""},
		{"openai oauth messages", PlatformOpenAI, AccountTypeOAuth, APIProtocolAnthropic, ""},
		{"anthropic oauth responses", PlatformAnthropic, AccountTypeOAuth, APIProtocolResponses, ""},
		{"anthropic setup chat", PlatformAnthropic, AccountTypeSetupToken, APIProtocolChatCompletions, ""},
		{"zhipu responses", PlatformZhipu, AccountTypeAPIKey, APIProtocolResponses, ""},
		{"gemini chat", PlatformGemini, AccountTypeAPIKey, APIProtocolChatCompletions, ""},
		{"grok messages", PlatformGrok, AccountTypeAPIKey, APIProtocolAnthropic, ""},
		{"systemone wrong platform", PlatformOpenAI, AccountTypeAPIKey, APIProtocolSystemOne, ""},
		{"compact chat", PlatformOpenAI, AccountTypeAPIKey, APIProtocolChatCompletions, AccountTestModeCompact},
		{"legacy compact messages", PlatformOpenAI, AccountTypeAPIKey, APIProtocolAnthropic, AccountTestModeLegacyCompact},
		{"anthropic compact responses", PlatformAnthropic, AccountTypeAPIKey, APIProtocolResponses, AccountTestModeCompact},
		{"kimi legacy compact responses", PlatformKimi, AccountTypeAPIKey, APIProtocolResponses, AccountTestModeLegacyCompact},
		{"grok compact responses", PlatformGrok, AccountTypeAPIKey, APIProtocolResponses, AccountTestModeCompact},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := adaptiveCNAccountTestAccount(9706, tt.platform)
			account.Type = tt.accountType
			account.Credentials["access_token"] = "test-oauth"
			svc, upstream := adaptiveCNAccountTestService(account)
			c, recorder := newTestContext()
			err := svc.TestAccountConnectionWithOptions(c, account.ID, "test-model", "hi", AccountTestTypeText, tt.mode, tt.endpoint, AccountTestOptions{})
			require.Error(t, err)
			require.Empty(t, upstream.requests)
			require.Contains(t, recorder.Body.String(), `"type":"error"`)
			require.NotContains(t, recorder.Body.String(), `"success":true`)
		})
	}
}

func TestAccountTestService_EndpointGeminiNativeSuccess(t *testing.T) {
	account := &Account{ID: 9707, Platform: PlatformGemini, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{"api_key": "test-gemini", "base_url": "https://relay.example/proxy"}}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"native gemini ok\"}]},\"finishReason\":\"STOP\"}]}\n\n"))}
	svc, upstream := adaptiveCNAccountTestService(account, resp)
	c, recorder := newTestContext()
	err := svc.TestAccountConnectionWithOptions(c, account.ID, "gemini-3.8-flash", "hello", AccountTestTypeText, AccountTestModeDefault, "gemini", AccountTestOptions{})
	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "/proxy/v1beta/models/gemini-3.8-flash:streamGenerateContent", upstream.lastReq.URL.Path)
	require.Contains(t, recorder.Body.String(), "native gemini ok")
	require.Contains(t, recorder.Body.String(), `"success":true`)
}

func TestAccountTestService_EndpointOAuthKeepsNativeAuthentication(t *testing.T) {
	for _, tt := range []struct{ platform, accountType, endpoint, wantPath string }{
		{PlatformOpenAI, AccountTypeOAuth, APIProtocolResponses, "/backend-api/codex/responses"},
		{PlatformAnthropic, AccountTypeOAuth, APIProtocolAnthropic, "/v1/messages"},
		{PlatformAnthropic, AccountTypeSetupToken, APIProtocolAnthropic, "/v1/messages"},
	} {
		t.Run(tt.platform+"/"+tt.accountType, func(t *testing.T) {
			account := &Account{ID: 9709, Platform: tt.platform, Type: tt.accountType, Concurrency: 1, Credentials: map[string]any{"access_token": "native-oauth-test"}}
			svc, upstream := adaptiveCNAccountTestService(account, accountEndpointResponse(tt.endpoint))
			c, recorder := newTestContext()
			err := svc.TestAccountConnectionWithOptions(c, account.ID, "native-model", "native prompt", AccountTestTypeText, AccountTestModeDefault, tt.endpoint, AccountTestOptions{})
			require.NoError(t, err)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, tt.wantPath, upstream.lastReq.URL.Path)
			require.Equal(t, "Bearer native-oauth-test", upstream.lastReq.Header.Get("Authorization"))
			require.Contains(t, string(upstream.lastBody), "native prompt")
			require.Contains(t, recorder.Body.String(), `"success":true`)
		})
	}
}

// 按协议准备真实格式的流，防止用统一假响应掩盖请求/响应解析器混用。
func accountEndpointResponse(endpoint string) *http.Response {
	switch endpoint {
	case APIProtocolAnthropic:
		return adaptiveCNAnthropicTestResponse()
	case APIProtocolChatCompletions:
		return adaptiveCNChatTestResponse()
	default:
		return adaptiveCNResponsesTestResponse()
	}
}

func assertAccountEndpointAuthentication(t *testing.T, request *http.Request, endpoint, apiKey string) {
	t.Helper()
	if endpoint == APIProtocolAnthropic {
		require.Equal(t, apiKey, getHeaderRaw(request.Header, "x-api-key"))
		require.Equal(t, "2023-06-01", request.Header.Get("anthropic-version"))
	} else {
		require.Equal(t, "Bearer "+apiKey, request.Header.Get("Authorization"))
	}
}
