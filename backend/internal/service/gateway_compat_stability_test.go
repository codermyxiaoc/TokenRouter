//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 把同一 SSE 流拆成细碎网络读取，避免测试只覆盖整块内存响应。
type fragmentedCompatReader struct{ io.Reader }

func (r fragmentedCompatReader) Read(dst []byte) (int, error) {
	if len(dst) > 7 {
		dst = dst[:7]
	}
	return r.Reader.Read(dst)
}

// event:error 是协议级失败事实，代理省略 type 或插入合法 SSE 注释不能吞掉它。
func TestAnthropicCompatStability_ProtocolErrorFrames(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, handle := range compatStreamHandlers() {
		for _, tc := range []struct{ name, frame string }{
			{"missing_payload_type", "event: error\ndata: {\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"},
			{"conflicting_payload_type", "event: error\ndata: {\"type\":\"message_delta\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"},
			{"comment_and_id_before_data", "event: error\n: edge keepalive\nid: err-1\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"},
			{"multiline_json", "event:error\ndata: {\"type\":\"error\",\ndata: \"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"},
			{"invalid_error_json", "event: error\ndata: {invalid-json\n\n"},
			{"empty_error_payload", "event: error\ndata:\n\n"},
			{"crlf", "event: error\r\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\r\n\r\n"},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				stream := anthropicStreamWithoutMessageStop() + tc.frame
				resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(fragmentedCompatReader{strings.NewReader(stream)})}
				result, err := handle(&GatewayService{}, resp, c)
				require.Error(t, err)
				var sseErr *sseStreamErrorEventError
				require.ErrorAs(t, err, &sseErr)
				require.Nil(t, result)
				require.NotContains(t, rec.Body.String(), compatStreamTerminalMarkers[name])
				require.Equal(t, 1, strings.Count(rec.Body.String(), compatStreamFailureMarkers[name]))
			})
		}
	}
}

// Retry-After 的异常数字不能溢出成负数而导致立即重试；合法大值仍受八秒上限约束。
func TestOpenAIStability_RetryAfterMalformedAndHuge(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"NaN", 0}, {"+Inf", 0}, {"-Inf", 0}, {"-1", 0}, {"1e100", openAIRequestScopedRetryMaxDelay}, {"9223372036854775808", openAIRequestScopedRetryMaxDelay}, {"0.25", 250 * time.Millisecond},
	} {
		t.Run(tc.value, func(t *testing.T) {
			require.Equal(t, tc.want, openAIRequestScopedRetryDelay(http.Header{"Retry-After": {tc.value}}, nil))
			wantMS := tc.want
			if tc.value == "0.25" {
				wantMS = 250 * time.Microsecond
			}
			require.Equal(t, wantMS, openAIRequestScopedRetryDelay(http.Header{"Retry-After-Ms": {tc.value}}, nil))
		})
	}
}

// 多行事件不能绕过原有大小限制，连接失败后的已观测输入仍必须保留。
func TestAnthropicCompatStability_MultilineFrameLimit(t *testing.T) {
	for name, handle := range compatStreamHandlers() {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			stream := anthropicStreamTruncatedBeforeMessageDelta() + "event: content_block_delta\n" + strings.Repeat("data: "+strings.Repeat("x", 512)+"\n", 8) + "\n"
			svc := &GatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: 2048}}}
			result, err := handle(svc, &http.Response{Header: http.Header{}, Body: io.NopCloser(fragmentedCompatReader{strings.NewReader(stream)})}, c)
			require.Error(t, err)
			require.NotNil(t, result)
			require.Equal(t, 20, result.Usage.InputTokens)
			require.Contains(t, rec.Body.String(), "response_too_large")
			require.NotContains(t, rec.Body.String(), compatStreamTerminalMarkers[name])
		})
	}
}

// 解析兼容不能丢失正常用量，也不能使省略空行的既有代理请求变成断流。
func TestAnthropicCompatStability_ValidMultilineAndCompactFrames(t *testing.T) {
	for name, handle := range compatStreamHandlers() {
		for _, compact := range []bool{false, true} {
			t.Run(name+map[bool]string{true: "/compact", false: "/multiline"}[compact], func(t *testing.T) {
				stream := anthropicStreamWithMidStreamText()
				if compact {
					stream = strings.ReplaceAll(stream, "\n\n", "\n")
				} else {
					stream = strings.ReplaceAll(stream, `data: {"type":"message_delta",`, "data: {\n: comment\ndata: \"type\":\"message_delta\",")
				}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				result, err := handle(&GatewayService{}, &http.Response{Header: http.Header{}, Body: io.NopCloser(fragmentedCompatReader{strings.NewReader(stream)})}, c)
				require.NoError(t, err)
				require.Equal(t, 20, result.Usage.InputTokens)
				require.Equal(t, 42, result.Usage.OutputTokens)
				require.Contains(t, rec.Body.String(), compatStreamTerminalMarkers[name])
				require.NotContains(t, rec.Body.String(), compatStreamFailureMarkers[name])
			})
		}
	}
}

// 上游保持连接不关闭时，显式错误仍须立即释放读取方，不能留下排水 goroutine。
func TestAnthropicCompatStability_ErrorClosesStalledUpstream(t *testing.T) {
	for name, handle := range compatStreamHandlers() {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			frame := "event: error\n: proxy comment\ndata: {\n" + `data: "error":{"type":"overloaded_error","message":"Overloaded"}}` + "\n\n"
			resp, pw := stallingAnthropicUpstream(t, anthropicStreamTruncatedBeforeMessageDelta()+frame)
			got := runCompatStreamWithDeadline(t, handle, &GatewayService{}, resp, c)
			require.Error(t, got.err)
			_, err := io.WriteString(pw, "event: ping\n")
			require.Error(t, err, "错误结束后读取连接必须已关闭")
			require.NotContains(t, rec.Body.String(), compatStreamTerminalMarkers[name])
		})
	}
}
