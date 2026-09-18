//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 真实三个入口覆盖 JSON/SSE：无输出时留给智能路由，已输出或计量后只记录并明确失败。
func TestOpenCodeNativeAnthropicTerminalFailureMatrix(t *testing.T) {
	for _, ingress := range cnProtocolIngressCases() {
		for _, stream := range []bool{false, true} {
			for _, phase := range []string{"prelude_error", "usage_error", "text_error", "text_eof", "json_error", "json_usage_error", "complete"} {
				t.Run(fmt.Sprintf("%s/stream=%v/%s", ingress.name, stream, phase), func(t *testing.T) {
					var request map[string]any
					require.NoError(t, json.Unmarshal(ingress.body, &request))
					request["model"], request["stream"] = "qwen3.8-max", stream
					body, err := json.Marshal(request)
					require.NoError(t, err)
					contentType := "text/event-stream"
					response := openCodeAnthropicTestResponse(phase)
					if strings.HasPrefix(phase, "json_") {
						contentType = "application/json"
						jsonPhase := "prelude_error"
						if phase == "json_usage_error" {
							jsonPhase = "usage_error"
						}
						response = openCodeAnthropicTestJSON(jsonPhase)
					}
					if ingress.path == "/v1/messages" && !stream {
						contentType = "application/json"
						if !strings.HasPrefix(phase, "json_") {
							response = openCodeAnthropicTestJSON(phase)
						}
					}
					upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}, "X-Request-Id": {"native-req"}}, Body: io.NopCloser(strings.NewReader(response))}}
					svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
					account := adaptiveProtocolTestAccount(PlatformOpenCodeGo, nil)
					c, recorder := newOpenAIImagesTestContext(t, body)
					c.Request.URL.Path = ingress.path
					var result *OpenAIForwardResult
					switch ingress.path {
					case "/v1/messages":
						result, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
					case "/v1/chat/completions":
						result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
					default:
						result, err = svc.Forward(context.Background(), c, account, body)
					}
					var failover *UpstreamFailoverError
					if phase == "complete" {
						require.NoError(t, err)
						require.NotNil(t, result)
						require.Equal(t, 10, result.Usage.InputTokens)
						require.Equal(t, 7, result.Usage.OutputTokens)
						require.Contains(t, recorder.Body.String(), "half answer")
					} else if phase == "prelude_error" || phase == "json_error" {
						require.True(t, errors.As(err, &failover), "%T: %v", err, err)
						require.Equal(t, 503, failover.StatusCode)
						require.Nil(t, result)
						require.False(t, c.Writer.Written())
					} else {
						require.Error(t, err)
						require.NotErrorAs(t, err, &failover)
						require.NotNil(t, result)
						require.Contains(t, recorder.Body.String(), "error")
						require.NotContains(t, recorder.Body.String(), "response.completed")
						require.NotContains(t, recorder.Body.String(), `"finish_reason":"stop"`)
						require.NotContains(t, recorder.Body.String(), `"type":"message_stop"`)
						if phase == "usage_error" || phase == "json_usage_error" {
							require.Equal(t, 10, result.Usage.InputTokens)
						}
					}
					if phase != "complete" {
						events, ok := c.Get(OpsUpstreamErrorsKey)
						require.True(t, ok, "后台必须保留真实错误")
						serialized, marshalErr := json.Marshal(events)
						require.NoError(t, marshalErr)
						wantStatus := int64(503)
						if phase == "text_eof" {
							wantStatus = 502
						}
						require.Equal(t, wantStatus, gjson.GetBytes(serialized, "0.upstream_status_code").Int())
					}
				})
			}
		}
	}
}

// 客户端写出失败不会停止上游排水，末尾用量仍被记录，且不重新执行请求。
func TestOpenCodeNativeAnthropicDisconnectDrainsUsage(t *testing.T) {
	body := []byte(`{"model":"qwen3.8-max","input":"hello","stream":true}`)
	c, _ := newOpenAIImagesTestContext(t, body)
	c.Request.URL.Path = "/v1/responses"
	c.Writer = &failingOpenAIImageWriter{ResponseWriter: c.Writer, failAfter: 1}
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(openCodeAnthropicTestResponse("complete")))}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	result, err := svc.Forward(context.Background(), c, adaptiveProtocolTestAccount(PlatformOpenCodeGo, nil), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.ClientDisconnect)
	require.Equal(t, 7, result.Usage.OutputTokens)
}

func openCodeAnthropicTestResponse(phase string) string {
	inputTokens := 0
	if phase == "usage_error" || phase == "complete" {
		inputTokens = 10
	}
	frames := []string{fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"qwen3.8-max","content":[],"usage":{"input_tokens":%d,"output_tokens":0}}}`, inputTokens)}
	if strings.HasPrefix(phase, "text") || phase == "complete" {
		frames = append(frames, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"half answer"}}`)
	}
	if phase == "complete" {
		frames = append(frames, `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`, `{"type":"message_stop"}`)
	} else if phase != "text_eof" {
		frames = append(frames, `{"type":"error","error":{"type":"overloaded_error","status":503,"message":"temporarily unavailable"}}`)
	}
	var stream strings.Builder
	for _, frame := range frames {
		fmt.Fprintf(&stream, "event:%s\ndata:%s\n\n", gjson.Get(frame, "type").String(), frame)
	}
	return stream.String()
}

func openCodeAnthropicTestJSON(phase string) string {
	if phase == "complete" {
		return `{"id":"msg_1","type":"message","role":"assistant","model":"qwen3.8-max","content":[{"type":"text","text":"half answer"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":7}}`
	}
	if phase == "text_eof" {
		return `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"half answer"}]}`
	}
	if phase == "usage_error" {
		return `{"type":"error","error":{"type":"overloaded_error","status":503,"message":"temporarily unavailable"},"usage":{"input_tokens":10}}`
	}
	if phase == "text_error" {
		return `{"type":"error","error":{"type":"overloaded_error","status":503,"message":"temporarily unavailable"},"content":[{"type":"text","text":"half answer"}]}`
	}
	return `{"type":"error","error":{"type":"overloaded_error","status":503,"message":"temporarily unavailable"}}`
}
