//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 记录真实转发路径送给模拟上游的请求，区分模型改写、仅选组和直接访问。
func TestClaude55ThinkingForRoutedMessagesAndCountTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const sonnet, opus, haiku = "claude-sonnet-5-5", "claude-opus-5-5", "claude-haiku-5-5"
	for _, tc := range []struct {
		name, inputModel, accountTarget, beforeChannel, keySource string
		thinking, wantType, wantModel                             string
		reject                                                    bool
	}{
		{"账号映射到Opus", "review", opus, "", "", `{"type":"enabled","budget_tokens":2048}`, "adaptive", opus, false},
		{"渠道映射到Sonnet", sonnet, "", "review", "", `{"type":"disabled"}`, "between_tools", sonnet, false},
		{"Key映射到Haiku", haiku, "", "", "review", `{"type":"enabled","budget_tokens":64}`, "adaptive", haiku, false},
		{"复合Key仅剥前缀", sonnet, "", "", "", `{"type":"enabled","budget_tokens":512}`, "", "", true},
		{"直接Sonnet仍校验", sonnet, "", "", "", `{"type":"disabled"}`, "", "", true},
		{"仅规范化百万上下文选择器", sonnet + "[1m]", "", "", "", `{"type":"disabled"}`, "", "", true},
		{"Key别名带选择器映射回同一模型", sonnet, "", "", sonnet + "[1m]", `{"type":"disabled"}`, "", "", true},
		{"带选择器的别名映射到Sonnet", "review[1m]", sonnet, "", "", `{"type":"disabled"}`, "between_tools", sonnet, false},
		{"途经别名回到原模型", "internal", sonnet, "", sonnet, `{"type":"disabled"}`, "", "", true},
		{"直接Opus交给上游校验", opus, "", "", "", `{"type":"enabled","budget_tokens":512}`, "enabled", opus, false},
		{"不同上游型号保留模式", "review", "claude-sonnet-4-6", "", "", `{"type":"enabled","budget_tokens":512}`, "enabled", "claude-sonnet-4-6", false},
		{"映射后保留Sonnet其他约束", "review", sonnet, "", "", `{"type":"disabled","display":"summarized"}`, "", "", true},
	} {
		for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
			t.Run(tc.name+path, func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, path, nil)
				ctx := c.Request.Context()
				if tc.keySource != "" {
					ctx = WithAPIKeyModelRedirectTrace(ctx, NewAPIKeyModelRedirectTrace("Group/"+tc.keySource, tc.keySource, tc.inputModel))
				}
				if tc.name == "复合Key仅剥前缀" {
					ctx = context.WithValue(ctx, ctxkey.ClientModel, "Group/"+tc.inputModel)
				}
				if tc.beforeChannel != "" {
					ctx = WithGatewaySchedulingModel(ctx, tc.beforeChannel)
				}
				c.Request = c.Request.WithContext(ctx)
				account := newAnthropicAPIKeyAccountForTest()
				if tc.accountTarget != "" {
					account.Credentials["model_mapping"] = map[string]any{normalizeClaudeCodeLongContextModel(tc.inputModel): tc.accountTarget}
				}
				payload := fmt.Sprintf(`{"model":%q,"max_tokens":4096,"thinking":%s,"messages":[{"role":"user","content":"hi"}]}`, tc.inputModel, tc.thinking)
				parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(payload)), PlatformAnthropic)
				require.NoError(t, err)
				response := `{"id":"test","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`
				if path == "/v1/messages/count_tokens" {
					response = `{"input_tokens":1}`
				}
				upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}}
				svc := newForwardPartialUsageServiceForTest(upstream)
				if path == "/v1/messages" {
					_, err = svc.Forward(ctx, c, account, parsed)
				} else {
					err = svc.ForwardCountTokens(ctx, c, account, parsed)
				}
				if tc.reject {
					require.Error(t, err)
					require.Equal(t, 400, rec.Code)
					require.Nil(t, upstream.lastReq)
					return
				}
				require.NoError(t, err)
				require.NotNil(t, upstream.lastReq)
				require.Equal(t, tc.wantModel, gjson.GetBytes(upstream.lastBody, "model").String())
				require.Equal(t, tc.wantType, gjson.GetBytes(upstream.lastBody, "thinking.type").String())
				require.Equal(t, tc.wantType == "enabled", gjson.GetBytes(upstream.lastBody, "thinking.budget_tokens").Exists())
			})
		}
	}
}

// 多协议账号选择原生 Messages 时也按本轮真实映射判断，原始请求仍可用于后续尝试。
func TestClaude55ThinkingForNativeMessagesBridge(t *testing.T) {
	const model = "claude-sonnet-5-5"
	body := []byte(`{"model":"claude-sonnet-5-5","max_tokens":64,"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]}`)
	for _, source := range []string{"client-alias", model} {
		t.Run(source, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: nativeAnthropicBufferedResponse()}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			ctx := WithGatewaySchedulingModel(context.Background(), source)
			_, err := svc.ForwardAsAnthropic(ctx, adaptiveProtocolTestContext("/v1/messages", body), nativeAnthropicTestAccount(), body, "", "")
			if source == model {
				require.Error(t, err)
				require.Nil(t, upstream.lastReq)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "between_tools", gjson.GetBytes(upstream.lastBody, "thinking.type").String())
			require.Equal(t, "disabled", gjson.GetBytes(body, "thinking.type").String())
		})
	}
}

// Bedrock 的模型映射仍受原有 CC 参数开关约束，转发准备不能自行放宽该入口。
func TestClaude55ThinkingForBedrockPreservesCompatGate(t *testing.T) {
	const target = "us.anthropic.claude-sonnet-5-5"
	account := &Account{
		Platform: PlatformAnthropic, Type: AccountTypeBedrock,
		Credentials: map[string]any{"model_mapping": map[string]any{"alias": target}},
	}
	body := []byte(`{"model":"alias","thinking":{"type":"disabled"}}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)
	require.ErrorContains(t, prepareRoutedClaudeRequest(context.Background(), account, parsed), "requires adaptive thinking")
	require.Equal(t, "disabled", gjson.GetBytes(parsed.Body.Bytes(), "thinking.type").String())

	// 模拟开关启用后已执行的 Bedrock 参数转换。
	require.NoError(t, parsed.ReplaceBody(sanitizeBedrockThinking(body, target)))
	require.NoError(t, prepareRoutedClaudeRequest(context.Background(), account, parsed))
	require.Equal(t, "between_tools", gjson.GetBytes(parsed.Body.Bytes(), "thinking.type").String())
}
