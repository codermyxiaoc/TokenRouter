//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/model"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 新平台不能把 OpenAI 兼容续接缓存、12 条尾部裁剪和 Todo 指令带入用户会话。
func TestOpenCodeMessagesResponsesPreserveFullHistory(t *testing.T) {
	for _, mode := range []string{AccountModeZen, AccountModeGo} {
		for _, continuation := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/continuation=%v", mode, continuation), func(t *testing.T) {
				messages := make([]map[string]string, 16)
				for i := range messages {
					role := "user"
					if i%2 == 1 {
						role = "assistant"
					}
					messages[i] = map[string]string{"role": role, "content": fmt.Sprintf("history-%02d", i)}
				}
				body, err := json.Marshal(map[string]any{"model": "gpt-5.6-luna", "max_tokens": 32, "messages": messages, "metadata": map[string]string{"user_id": "original-session"}})
				require.NoError(t, err)
				account := adaptiveProtocolTestAccount(PlatformOpenCodeGo, nil)
				account.Credentials["account_mode"] = mode
				account.Extra = map[string]any{"openai_responses_continuation_supported": continuation}
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"unavailable"}}`))}}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				c := adaptiveProtocolTestContext("/v1/messages", body)
				c.Set("api_key", &APIKey{ID: 5})
				svc.bindOpenAICompatSessionResponseID(context.Background(), c, account, "original-session", "prior-openai-response")
				_, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "original-session", "")
				require.Error(t, err)
				require.Len(t, gjson.GetBytes(upstream.lastBody, "input").Array(), 16)
				for i := range messages {
					require.Contains(t, string(upstream.lastBody), fmt.Sprintf("history-%02d", i))
				}
				require.NotContains(t, string(upstream.lastBody), openAICompatClaudeCodeTodoGuardMarker)
				require.False(t, gjson.GetBytes(upstream.lastBody, "previous_response_id").Exists())
				require.Equal(t, isolateOpenAISessionID(5, "original-session"), upstream.lastReq.Header.Get(openCodeSessionHeader))
			})
		}
	}
}

// 三种入站的原生 Anthropic 分流都保留已匹配 UA/TLS；既有 CN 路径不改变传输策略。
func TestOpenCodeNativeAnthropicPreservesTLSRouting(t *testing.T) {
	for _, platform := range []string{PlatformOpenCodeGo, PlatformMiniMax} {
		for _, ingress := range cnProtocolIngressCases() {
			t.Run(platform+"/"+ingress.name, func(t *testing.T) {
				const incomingUA = "test-opencode-client/1.0"
				const routedUA = "test-upstream-client/2.0"
				account := adaptiveProtocolTestAccount(platform, nil)
				account.Credentials["api_protocol"] = APIProtocolAnthropic
				account.Extra = map[string]any{"enable_tls_fingerprint": true, "tls_fingerprint_router_id": int64(9)}
				router := &model.TLSFingerprintRouter{ID: 9, Enabled: true, Rules: []model.TLSFingerprintRouterRule{{Name: "native", Enabled: true, MatchType: model.TLSRouterMatchExact, Pattern: incomingUA, TLSFingerprintProfileID: 0, UpstreamUserAgent: routedUA}}}
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"unavailable"}}`))}}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream, tlsFPProfileService: &TLSFingerprintProfileService{}, tlsFPRouterService: &TLSFingerprintRouterService{localCache: map[int64]*cachedTLSFingerprintRouter{9: newCachedTLSFingerprintRouter(router)}}}
				body := []byte(strings.ReplaceAll(string(ingress.body), "deepseek-chat", "minimax-m3"))
				c := adaptiveProtocolTestContext(ingress.path, body)
				c.Request.Header.Set("User-Agent", incomingUA)
				match := svc.MatchOpenAITLSFingerprintRouterForRequest(c, account)
				require.True(t, match.Matched)
				var err error
				switch ingress.path {
				case "/v1/messages":
					_, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "", match)
				case "/v1/chat/completions":
					_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "", match)
				default:
					_, err = svc.Forward(context.Background(), c, account, body)
				}
				require.Error(t, err)
				require.True(t, strings.HasSuffix(upstream.lastReq.URL.Path, "/v1/messages"))
				if platform == PlatformOpenCodeGo {
					require.NotNil(t, upstream.lastTLSProfile)
					require.Equal(t, "Built-in Default (Node.js 24.x)", upstream.lastTLSProfile.Name)
					require.Equal(t, routedUA, upstream.lastReq.Header.Get("User-Agent"))
				} else {
					require.Nil(t, upstream.lastTLSProfile)
					require.NotEqual(t, routedUA, upstream.lastReq.Header.Get("User-Agent"))
				}
			})
		}
	}
}
