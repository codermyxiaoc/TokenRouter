//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 四种入口必须在协议前导事件之前识别带内失败，才能安全切换到另一账号。
func TestGeminiPreOutputFailoverAcrossProtocols(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, protocol := range []string{"native", "messages", "chat", "responses"} {
		for _, stream := range []bool{false, true} {
			t.Run(protocol+map[bool]string{false: "/json", true: "/stream"}[stream], func(t *testing.T) {
				body := `{"error":{"code":503,"status":"UNAVAILABLE","message":"upstream unavailable"}}`
				if stream {
					body = "data: {\"responseId\":\"metadata\"}\n\ndata: " + body + "\n\n"
				}
				svc := newGeminiSignalService("application/json", body)
				c, _ := newGeminiNativeTestContext(t)
				account := geminiSignalTestAccount()
				streamJSON := map[bool]string{false: "false", true: "true"}[stream]
				var err error
				switch protocol {
				case "native":
					action := "generateContent"
					if stream {
						action = "streamGenerateContent"
					}
					_, err = svc.ForwardNative(context.Background(), c, account, "gemini-3.7-flash", action, stream, geminiSignalTestRequest())
				case "messages":
					_, err = svc.Forward(context.Background(), c, account, []byte(`{"model":"gemini-3.7-flash","max_tokens":100,"stream":`+streamJSON+`,"messages":[{"role":"user","content":"hello"}]}`))
				case "chat":
					_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(`{"model":"gemini-3.7-flash","stream":`+streamJSON+`,"messages":[{"role":"user","content":"hello"}]}`))
				case "responses":
					_, err = svc.ForwardAsResponses(context.Background(), c, account, []byte(`{"model":"gemini-3.7-flash","stream":`+streamJSON+`,"input":"hello"}`), nil)
				}
				assertGeminiPreOutputFailover(t, c, err, http.StatusServiceUnavailable)
			})
		}
	}
}

// 仅对确定没有输出/计量/副作用的错误重试；所有保留场景都必须完整回放原始字节。
func TestGeminiPreOutputFailoverSafetyBoundaries(t *testing.T) {
	errorJSON := `{"error":{"code":503,"message":"unavailable","status":"UNAVAILABLE"}}`
	for _, tc := range []struct {
		name, response, request     string
		stream, bound, wantFailover bool
	}{
		{name: "empty", wantFailover: true},
		{name: "error", response: errorJSON, wantFailover: true},
		{name: "model404", response: `{"error":{"code":404,"message":"The requested model or endpoint is unavailable."}}`, wantFailover: true},
		{name: "parameter400", response: `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"invalid contents"}}`},
		{name: "generic404", response: `{"error":{"code":404,"message":"resource missing"}}`},
		{name: "policy", response: `{"promptFeedback":{"blockReason":"SAFETY"}}`},
		{name: "usage", response: `{"usageMetadata":{"promptTokenCount":1},"error":{"code":503,"message":"unavailable"}}`},
		{name: "tool_usage", response: `{"candidates":[],"usageMetadata":{"toolUsePromptTokenCount":2}}`},
		{name: "unknown_usage", response: `{"candidates":[],"usageMetadata":{"vendorTokenCount":2}}`},
		{name: "unknown_output", response: `{"candidates":[],"vendorOutput":{"result":"already generated"}}`},
		{name: "error_with_unknown_output", response: `{"error":{"code":503,"status":"UNAVAILABLE"},"vendorOutput":{"result":"already generated"}}`},
		{name: "error_with_unknown_output_stream", response: "data: {\"error\":{\"code\":503,\"status\":\"UNAVAILABLE\"},\"vendorOutput\":{\"result\":\"already generated\"}}\n\n", stream: true},
		{name: "unknown_output_stream", response: "data: {\"candidates\":[],\"vendorOutput\":{\"result\":\"already generated\"}}\n\n", stream: true},
		{name: "tool_usage_stream", response: "data: {\"candidates\":[],\"usageMetadata\":{\"toolUsePromptTokenCount\":2}}\n\n", stream: true},
		{name: "text", response: `{"candidates":[{"content":{"parts":[{"text":"partial"}]}}],"error":{"code":503}}`},
		{name: "tool", response: `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"transfer"}}]}}],"error":{"code":503}}`},
		{name: "cached", response: errorJSON, bound: true},
		{name: "image", response: errorJSON, request: `{"generationConfig":{"responseModalities":["IMAGE"]}}`},
		{name: "audio", response: errorJSON, request: `{"generationConfig":{"responseModalities":["AUDIO"]}}`},
		{name: "execution", response: errorJSON, request: `{"tools":[{"codeExecution":{}}]}`},
		{name: "search", response: errorJSON, request: `{"tools":[{"googleSearch":{}}]}`},
		{name: "first_frame_error", response: "data: " + errorJSON + "\n\n", stream: true, wantFailover: true},
		{name: "preamble_only", response: "data: {\"responseId\":\"r\"}\n\n", stream: true, wantFailover: true},
		{name: "output_then_error", response: "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}\n\ndata: " + errorJSON + "\n\n", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.bound {
				ctx = WithGeminiCachedContentBound(ctx)
			}
			svc := newGeminiSignalService("application/json", "")
			c, _ := newGeminiNativeTestContext(t)
			resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.response))}
			err := svc.prepareGeminiResponseFailover(ctx, c, geminiSignalTestAccount(), resp, "gemini-3.7-flash", "r", tc.stream, []byte(tc.request))
			if tc.wantFailover {
				var failure *UpstreamFailoverError
				require.ErrorAs(t, err, &failure)
				require.False(t, c.Writer.Written())
				return
			}
			require.NoError(t, err)
			replayed, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, tc.response, string(replayed))
			require.Empty(t, upstreamErrorEventsFromContext(t, c))
		})
	}
}

// 读到半个结果后连接中断，必须保留错误和数据，不允许重新生成造成重复计费。
func TestGeminiPreOutputPartialReadNeverReplaysUpstream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		body := `{"usageMetadata":{"promptTokenCount":12},"candidates":[`
		if stream {
			body = "data: " + body
		}
		svc := newGeminiSignalService("application/json", "")
		c, _ := newGeminiNativeTestContext(t)
		resp := &http.Response{StatusCode: 200, Body: io.NopCloser(geminiSignalUnexpectedEOFReader{strings.NewReader(body)})}
		err := svc.prepareGeminiResponseFailover(context.Background(), c, geminiSignalTestAccount(), resp, "gemini-3.7-flash", "r", stream, nil)
		require.NoError(t, err)
		replayed, err := io.ReadAll(resp.Body)
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
		require.Equal(t, body, string(replayed))
		require.Empty(t, upstreamErrorEventsFromContext(t, c))
	}
}

// 客户端取消后不得再把当前失败转成换号信号。
func TestGeminiPreOutputCanceledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc := newGeminiSignalService("application/json", "")
	c, _ := newGeminiNativeTestContext(t)
	resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}
	err := svc.prepareGeminiResponseFailover(ctx, c, geminiSignalTestAccount(), resp, "gemini-3.7-flash", "r", false, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, upstreamErrorEventsFromContext(t, c))
}
