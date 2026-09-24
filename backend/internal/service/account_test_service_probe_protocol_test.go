//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/openai_compat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAccountTestService_SelectedProbeCNProtocolOnlyCallsSelectedEndpoint(t *testing.T) {
	for _, platform := range []string{PlatformKimi, PlatformDeepseek, PlatformMiniMax, PlatformZhipu} {
		for _, protocol := range []string{APIProtocolChatCompletions, APIProtocolAnthropic, APIProtocolResponses} {
			if platform == PlatformZhipu && protocol == APIProtocolResponses {
				continue
			}
			t.Run(platform+"/"+protocol, func(t *testing.T) {
				account := adaptiveCNAccountTestAccount(9301, platform)
				response, wantURL := adaptiveCNChatTestResponse(), "http://chat.example/v1/chat/completions"
				switch protocol {
				case APIProtocolAnthropic:
					response, wantURL = adaptiveCNAnthropicTestResponse(), "http://anthropic.example/v1/messages"
				case APIProtocolResponses:
					response, wantURL = adaptiveCNResponsesTestResponse(), "http://responses.example/v1/responses"
					if platform == PlatformDeepseek {
						wantURL = "http://responses.example/responses"
					}
				}
				svc, upstream := adaptiveCNAccountTestService(account, response)
				result, err := svc.RunTestBackgroundWithPromptAndUserAgentAndProtocol(context.Background(), account.ID, "model-a", "hi", "channel-probe/1.0", protocol)
				require.NoError(t, err)
				require.Equal(t, "success", result.Status, result.ErrorMessage)
				require.Len(t, upstream.requests, 1)
				require.Equal(t, wantURL, upstream.requests[0].URL.String())
				require.Equal(t, "channel-probe/1.0", upstream.requests[0].Header.Get("User-Agent"))
				require.Equal(t, APIProtocolAdaptive, account.Credentials["api_protocol"])
				require.NotContains(t, account.Credentials, "base_url")
				require.Nil(t, account.Extra)
				if protocol == APIProtocolResponses {
					require.False(t, gjson.GetBytes(upstream.lastBody, "store").Bool())
					require.False(t, gjson.GetBytes(upstream.lastBody, "instructions").Exists())
				}
			})
		}
	}
}

func TestAccountTestService_SelectedProbeDoesNotMarkForbiddenProtocolAsAccountError(t *testing.T) {
	account := adaptiveCNAccountTestAccount(9302, PlatformKimi)
	svc, upstream := adaptiveCNAccountTestService(account,
		newJSONResponse(http.StatusForbidden, `{"error":{"message":"This group does not allow /v1/messages dispatch"}}`))
	result, err := svc.RunTestBackgroundWithPromptAndUserAgentAndProtocol(context.Background(), account.ID, "kimi-k3", "hi", "", APIProtocolAnthropic)
	require.NoError(t, err)
	require.Equal(t, "failed", result.Status)
	require.Contains(t, result.ErrorMessage, "does not allow /v1/messages dispatch")
	require.Len(t, upstream.requests, 1)
	require.Zero(t, svc.accountRepo.(*openAIAccountTestRepo).setErrorID)
}

func TestAccountTestService_SelectedProbeAutoRetainsAdaptiveDiagnosis(t *testing.T) {
	for _, protocol := range []string{"", GroupAvailabilityProbeProtocolAuto} {
		t.Run(protocol, func(t *testing.T) {
			account := adaptiveCNAccountTestAccount(9303, PlatformKimi)
			svc, upstream := adaptiveCNAccountTestService(account,
				adaptiveCNChatTestResponse(),
				newJSONResponse(http.StatusForbidden, `{"error":{"message":"Messages disabled"}}`))
			result, err := svc.RunTestBackgroundWithPromptAndUserAgentAndProtocol(context.Background(), account.ID, "kimi-k3", "hi", "probe/2", protocol)
			require.NoError(t, err)
			require.Equal(t, "failed", result.Status)
			require.Len(t, upstream.requests, 2)
			require.Contains(t, result.ErrorMessage, "Adaptive Anthropic endpoint returned 403")
			for _, request := range upstream.requests {
				require.Equal(t, "probe/2", request.Header.Get("User-Agent"))
			}
		})
	}
}

func TestAccountTestService_SelectedProbeOpenAIOverridesOnlyRequestCopy(t *testing.T) {
	for _, protocol := range []string{APIProtocolChatCompletions, APIProtocolResponses} {
		t.Run(protocol, func(t *testing.T) {
			account := Account{
				ID: 9304, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
				Credentials: map[string]any{"api_key": "probe-key", "base_url": "https://relay.example/proxy/v1"},
				Extra:       map[string]any{openai_compat.ExtraKeyTextRouteMode: string(openai_compat.TextRouteModeForceChatCompletions)},
			}
			response, wantPath := adaptiveCNResponsesTestResponse(), "/proxy/v1/responses"
			if protocol == APIProtocolChatCompletions {
				account.Extra[openai_compat.ExtraKeyTextRouteMode] = string(openai_compat.TextRouteModeForceResponses)
				response, wantPath = adaptiveCNChatTestResponse(), "/proxy/v1/chat/completions"
			}
			originalMode := account.Extra[openai_compat.ExtraKeyTextRouteMode]
			upstream := &httpUpstreamRecorder{resp: response}
			svc := newOpenAIAutomaticProbeTestService([]Account{account}, upstream, nil, nil, rawChatCompletionsTestConfig())
			// 即使模型名带 image，显式选择文本协议仍不能误发付费图片请求。
			result, err := svc.RunTestBackgroundWithPromptAndUserAgentAndProtocol(context.Background(), account.ID, "gpt-image-2", "hi", "probe/3", protocol)
			require.NoError(t, err)
			require.Equal(t, "success", result.Status, result.ErrorMessage)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, wantPath, upstream.requests[0].URL.Path)
			require.Equal(t, originalMode, account.Extra[openai_compat.ExtraKeyTextRouteMode])
			require.Equal(t, "probe/3", upstream.requests[0].Header.Get("User-Agent"))
		})
	}
}

func TestAccountTestService_SelectedProbeRejectsUnsupportedBeforeHTTP(t *testing.T) {
	tests := []struct {
		name     string
		platform string
		typeName string
		protocol string
		model    string
		wantErr  string
	}{
		{"unknown", PlatformKimi, AccountTypeAPIKey, "typo", "kimi-k3", "protocol is unsupported"},
		{"zhipu responses", PlatformZhipu, AccountTypeAPIKey, APIProtocolResponses, "glm-5", "not supported for platform"},
		{"gemini chat", PlatformGemini, AccountTypeAPIKey, APIProtocolChatCompletions, "gemini-3.8-flash", "not supported for platform"},
		{"openai oauth chat", PlatformOpenAI, AccountTypeOAuth, APIProtocolChatCompletions, "gpt-6-astra", "only supported for OpenAI API Key"},
		{"jev", PlatformOpenCodeGo, AccountTypeAPIKey, APIProtocolResponses, "jev-1.13", "select auto"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := adaptiveCNAccountTestAccount(9305, tt.platform)
			account.Type = tt.typeName
			svc, upstream := adaptiveCNAccountTestService(account)
			result, err := svc.RunTestBackgroundWithPromptAndUserAgentAndProtocol(context.Background(), account.ID, tt.model, "hi", "", tt.protocol)
			require.NoError(t, err)
			require.Equal(t, "failed", result.Status)
			require.Contains(t, result.ErrorMessage, tt.wantErr)
			require.Empty(t, upstream.requests)
		})
	}
}

func TestAccountTestService_SelectedProbeOpenCodePreservesSelectedBaseURL(t *testing.T) {
	account := openCodeGoTestAccount(9306)
	account.Credentials["api_base_urls"].(map[string]any)[APIProtocolResponses] = "https://responses.example/relay/v1"
	svc, upstream := adaptiveCNAccountTestService(account, adaptiveCNResponsesTestResponse())
	result, err := svc.RunTestBackgroundWithPromptAndUserAgentAndProtocol(context.Background(), account.ID, "glm-5.3", "hi", "opencode-probe/1", APIProtocolResponses)
	require.NoError(t, err)
	require.Equal(t, "success", result.Status, result.ErrorMessage)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "https://responses.example/relay/v1/responses", upstream.requests[0].URL.String())
	require.NotEmpty(t, upstream.requests[0].Header.Get("X-OpenCode-Session"))
	require.Equal(t, APIProtocolAdaptive, account.Credentials["api_protocol"])
	require.Equal(t, "https://opencode.ai/zen/go/v1", account.Credentials["base_url"])
}

func TestAccountTestService_SelectedProbeNativePlatforms(t *testing.T) {
	tests := []struct {
		platform string
		protocol string
		model    string
		baseURL  string
		wantPath string
		response func() *http.Response
	}{
		{PlatformAnthropic, APIProtocolAnthropic, "claude-sonnet-4-6", "https://relay.example", "/v1/messages", adaptiveCNAnthropicTestResponse},
		{PlatformGrok, APIProtocolResponses, "grok-4.6", "https://relay.example", "/v1/responses", adaptiveCNResponsesTestResponse},
		{PlatformGemini, GroupAvailabilityProbeProtocolGemini, "gemini-3.8-flash", "https://relay.example", "/v1beta/models/gemini-3.8-flash:streamGenerateContent", func() *http.Response {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"gemini ok\"}]},\"finishReason\":\"STOP\"}]}\n\n")),
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.platform, func(t *testing.T) {
			account := &Account{
				ID: 9307, Platform: tt.platform, Type: AccountTypeAPIKey, Concurrency: 1,
				Credentials: map[string]any{"api_key": "probe-key", "base_url": tt.baseURL},
			}
			svc, upstream := adaptiveCNAccountTestService(account, tt.response())
			result, err := svc.RunTestBackgroundWithPromptAndUserAgentAndProtocol(context.Background(), account.ID, tt.model, "hi", "probe/native", tt.protocol)
			require.NoError(t, err)
			require.Equal(t, "success", result.Status, result.ErrorMessage)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, tt.wantPath, upstream.requests[0].URL.Path)
			require.Equal(t, "probe/native", upstream.requests[0].Header.Get("User-Agent"))
		})
	}
}
