//go:build unit

package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 结构校验须保留合法的空回答、工具、拒绝与思考回答，不能只接受可见文本。
func TestClineValidNonTextChoicesAcrossIngresses(t *testing.T) {
	for _, message := range []string{
		`{"role":"assistant","content":""}`,
		`{"role":"assistant","content":null}`,
		`{"role":"assistant"}`,
		`{"role":"assistant","content":[{"type":"text","text":"answer"}]}`,
		`{"role":"assistant","refusal":"cannot answer"}`,
		`{"role":"assistant","reasoning_content":"reasoned"}`,
		`{"role":"assistant","tool_calls":[{"id":"call1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}`,
		`{"role":"assistant","function_call":{"name":"lookup","arguments":"{}"}}`,
	} {
		for _, ingress := range cnProtocolIngressCases() {
			t.Run(ingress.name+"/"+message, func(t *testing.T) {
				response := fmt.Sprintf(`{"success":true,"data":{"id":"valid","model":"deepseek-chat","choices":[{"index":0,"message":%s,"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":27}}}`, message)
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(response))}}
				account := adaptiveProtocolTestAccount(PlatformCline, nil)
				account.Credentials["api_protocol"] = APIProtocolChatCompletions
				account.Credentials["base_url"] = "https://relay.example/v1"
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
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
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, 8, result.Usage.InputTokens)
				require.Equal(t, 27, result.Usage.OutputTokens)
			})
		}
	}
}

// 非空 message 仍需符合协议结构，重复字段不能让不同入口选中不同答案。
func TestClineRejectsInvalidMessageStructures(t *testing.T) {
	for _, choice := range []string{
		`{"message":{}}`, `{"message":[]}`, `{"message":{"content":false}}`,
		`{"message":{"role":false,"content":"x"}}`, `{"message":{"tool_calls":[null]}}`,
		`{"message":{"tool_calls":[{}]}}`, `{"message":{"function_call":{}}}`,
		`{"message":{"content":"ok"},"message":null}`, `{"message":{"content":"ok","Content":false}}`,
		`{"message":{"content":"ok"},"index":-1}`, `{"message":{"content":"ok"},"finish_reason":{}}`,
	} {
		_, err := normalizeClineChatJSON([]byte(`{"choices":[` + choice + `]}`))
		require.Error(t, err, choice)
	}
}

// 保持数字字符串与精确整数指数兼容，但拒绝重复键及超出整数边界的小数。
func TestSystemOneExactUsageCompatibility(t *testing.T) {
	for _, raw := range []string{`42`, `"42"`, `42.0`, `4.2e1`, `" 4.2e1 "`} {
		usage, ok := extractSystemOneUsage([]byte(`{"usage":{"input_tokens":` + raw + `,"output_tokens":0}}`))
		require.True(t, ok, raw)
		require.Equal(t, 42, usage.InputTokens)
	}
	for _, body := range []string{
		`{"usage":{"input_tokens":0,"INPUT_TOKENS":100,"output_tokens":1}}`,
		`{"usage":{"input_tokens":0,"input_\u0074okens":100,"output_tokens":1}}`,
		`{"usage":{"input_tokens":0,"output_tokens":1},"USAGE":{"input_tokens":100,"output_tokens":1}}`,
		`{"usage":{"input_tokens":"1099511627775.99999","output_tokens":1}}`,
		`{"usage":{"input_tokens":1e99999999999,"output_tokens":1}}`,
		`{"usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":1.1}}`,
	} {
		_, ok := extractSystemOneUsage([]byte(body))
		require.False(t, ok, body)
	}
}

// 即使底层传输错误直接包含请求 URL 或密钥，也只能记录固定的错误类型。
func TestModelCatalogFailureLogsExcludeErrorCredentials(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	account := adaptiveProtocolTestAccount(PlatformCommandCode, nil)
	account.ID = time.Now().UnixNano()
	account.Credentials["api_protocol"] = APIProtocolAdaptive
	account.Credentials["base_url"] = "https://catalog-user:password-secret@relay.example/private-path-secret?api_key=query-secret#fragment-secret"
	upstream := &httpUpstreamRecorder{err: &url.Error{Op: "Get", URL: account.GetCredential("base_url"), Err: errors.New("Authorization: Bearer error-secret")}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	require.Nil(t, svc.modelCatalogProtocols(t.Context(), account, "m"))
	for _, secret := range []string{"password-secret", "query-secret", "private-path-secret", "fragment-secret", "error-secret", "catalog-user"} {
		require.NotContains(t, logs.String(), secret)
	}
	require.Contains(t, logs.String(), "model_protocol_catalog_refresh_failed")
	require.Contains(t, logs.String(), "relay.example")
}

// 每轮与会话模型都在改写前拒绝重复键，正常省略模型的会话续接仍可使用。
func TestWSPassthroughRejectsAmbiguousModelFields(t *testing.T) {
	for _, body := range []string{
		`{"type":"response.create","model":"a","model":"b"}`,
		`{"type":"noop","type":"session.update","session":{"model":"a","model":"b"}}`,
		`{"type":"noop","ty\u0070e":"session.update","session":{"model":"a"}}`,
		`{"type":"noop","Type":"session.update","session":{"model":"a"}}`,
		`{"type":"noop","session":{"model":"a","model":"b"}}`,
		`{"type":"response.create","model":"a","Model":"b"}`,
		`{"type":"session.update","session":{"model":"a","mo\u0064el":"b"}}`,
		`{"type":"session.update","session":{"model":"a"},"session":{"model":"b"}}`,
	} {
		require.Error(t, validateOpenAIWSPassthroughModelFields([]byte(body)), body)
	}
	require.NoError(t, validateOpenAIWSPassthroughModelFields([]byte(`{"type":"response.create","input":[]}`)))
	require.NoError(t, validateOpenAIWSPassthroughModelFields([]byte(`{"type":"session.update","session":{"model":"m","instructions":"hello"}}`)))
	require.NoError(t, validateOpenAIWSPassthroughModelFields([]byte(`{"type":"session.update","session":{"instructions":"hello"}}`)))
}

// 实际适配器必须以失败回调交付可信部分用量，且坏计量不能触发可重放错误。
func TestWSPassthroughInvalidUsageReportsFailedTurn(t *testing.T) {
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.in_progress","response":{"id":"partial_usage","usage":{"input_tokens":7,"output_tokens":2}}}`)
	upstream.Send(`{"type":"response.completed","response":{"id":"partial_usage","usage":{"input_tokens":99,"output_tokens":5,"input_tokens_details":{"image_tokens":"invalid"}}}}`)
	captures := make(chan OpenAIWSTurnCapture, 2)
	server, serverErrors := startPassthroughLifecycleServerWithHooks(t, t.Context(),
		newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream), passthroughLifecycleAccount(),
		func(*gin.Context) *OpenAIWSIngressHooks {
			return &OpenAIWSIngressHooks{AfterTurn: func(capture OpenAIWSTurnCapture) { captures <- capture }}
		})
	defer server.Close()
	client := dialPassthroughLifecycleClient(t, server)
	defer client.CloseNow()
	for {
		payload, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
		if err != nil {
			require.Equal(t, coderws.StatusInternalError, coderws.CloseStatus(err))
			break
		}
		require.NotContains(t, string(payload), "response.completed")
	}
	select {
	case err := <-serverErrors:
		require.Error(t, err)
		require.False(t, isOpenAIWSIngressTurnRetryable(err))
	case <-time.After(3 * time.Second):
		t.Fatal("坏计量未结束转发")
	}
	select {
	case capture := <-captures:
		require.Error(t, capture.Err)
		require.NotNil(t, capture.Result)
		require.Equal(t, "partial_usage", capture.Result.RequestID)
		require.Equal(t, "response.failed", capture.Result.UpstreamTerminalEvent)
		require.Equal(t, 7, capture.Result.Usage.InputTokens)
		require.Equal(t, 2, capture.Result.Usage.OutputTokens)
		require.Zero(t, capture.Result.Usage.ImageInputTokens)
	case <-time.After(3 * time.Second):
		t.Fatal("坏计量未生成失败回调")
	}
	require.Empty(t, captures)
}
