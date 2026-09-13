//go:build unit

package middleware

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSmartRoutingWriterProtocolPreludesAndErrorDone(t *testing.T) {
	for name, prelude := range map[string]string{
		"chat_role":       "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n",
		"anthropic_start": "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"failed-id\",\"content\":[]}}\n\nevent: content_block_start\ndata: {\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			router := newFailoverRouter(t, smartRoutingTestKey(), &failoverResolver{}, false, func(c *gin.Context) {
				calls++
				c.Header("Content-Type", "text/event-stream")
				if calls == 1 {
					// 分块边界刻意落在帧分隔符中间，不能提前泄露首组前导。
					for _, part := range []string{prelude[:len(prelude)-1], prelude[len(prelude)-1:]} {
						_, _ = c.Writer.WriteString(part)
						c.Writer.Flush()
					}
					service.SetOpsUpstreamError(c, 502, "failed-first", "")
					_, _ = c.Writer.WriteString("data: {\"error\":{\"message\":\"failed-first\"}}\n\ndata: [DONE]\n\n")
					return
				}
				_, _ = c.Writer.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"success\"}}]}\n\ndata: [DONE]\n\n")
			}, nil)
			response := callFailoverRouter(router, "/v1/chat/completions", `{"model":"target","stream":true}`)
			require.Equal(t, 2, calls)
			require.Contains(t, response.Body.String(), "success")
			require.NotContains(t, response.Body.String(), "failed-first")
			require.NotContains(t, response.Body.String(), "failed-id")
		})
	}
}

func TestSmartRoutingWriterCommittedStreamCooldownRequiresTerminalFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			key := smartRoutingTestKey()
			key.SmartRoutingCooldownSeconds = 60
			resolver := &failoverResolver{}
			calls := 0
			router := newFailoverRouter(t, key, resolver, false, func(c *gin.Context) {
				calls++
				// 组内换号恢复过一次 502，本身不能证明最终流失败。
				service.SetOpsUpstreamError(c, 502, "recovered-account-error", "")
				c.Header("Content-Type", "text/event-stream")
				_, _ = c.Writer.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"visible-output\"}}]}\n\n")
				c.Writer.Flush()
				if failed {
					service.SetOpsUpstreamError(c, 524, "terminal-error", "")
					_, _ = c.Writer.WriteString("event: error\r\ndata: {\"type\":\"error\",\"message\":\"terminal-error\"}\r\n\r")
					_, _ = c.Writer.WriteString("\ndata: [DONE]\r\n\r\n")
				} else {
					_, _ = c.Writer.WriteString("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				}
			}, nil)
			response := callFailoverRouter(router, "/v1/chat/completions", `{"model":"target","stream":true}`)
			require.Equal(t, 1, calls, "真实业务输出后不得跨组")
			require.Contains(t, response.Body.String(), "visible-output")
			if failed {
				require.Len(t, resolver.cooling, 1)
				require.Contains(t, response.Body.String(), "terminal-error")
			} else {
				require.Empty(t, resolver.cooling)
			}
		})
	}
}

func TestSmartRoutingWriterToolAndInlineContentCommit(t *testing.T) {
	for _, frame := range []string{
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call_1\"}]}}]}\n\n",
		"event: message_start\ndata: {\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"hello\"}]}}\n\n",
		"event: content_block_start\ndata: {\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\"}}\n\n",
	} {
		response := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(response)
		writer := newSmartRoutingAttemptWriter(ctx.Writer)
		writer.Header().Set("Content-Type", "text/event-stream")
		_, err := writer.WriteString(frame)
		require.NoError(t, err)
		require.True(t, writer.committed)
		require.Equal(t, frame, response.Body.String())
	}
}

type smartRoutingFailedClientWriter struct {
	gin.ResponseWriter
	err error
}

func (w *smartRoutingFailedClientWriter) Write([]byte) (int, error) { return 0, w.err }

func TestSmartRoutingWriterPropagatesFirstClientWriteError(t *testing.T) {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	clientErr := errors.New("client disconnected")
	writer := newSmartRoutingAttemptWriter(&smartRoutingFailedClientWriter{ResponseWriter: ctx.Writer, err: clientErr})
	writer.Header().Set("Content-Type", "application/json")
	_, err := writer.WriteString(`{"output":"success"}`)
	require.ErrorIs(t, err, clientErr)
	require.True(t, writer.committed)
	require.ErrorIs(t, writer.commit(), clientErr)
}

func TestSmartRoutingWriterLargeFrameKeepsBoundedTerminalObservation(t *testing.T) {
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	writer := newSmartRoutingAttemptWriter(ctx.Writer)
	writer.Header().Set("Content-Type", "text/event-stream")
	body := "data: " + strings.Repeat("x", smartRoutingPendingLimit*2) + "\n\nevent: error\ndata: {\"type\":\"error\"}\n\n"
	_, err := writer.WriteString(body)
	require.NoError(t, err)
	require.True(t, writer.committed)
	require.True(t, writer.failedResponse())
	require.LessOrEqual(t, len(writer.ssePartial), smartRoutingPendingLimit)
	require.LessOrEqual(t, writer.pending.Len(), smartRoutingPendingLimit)
	require.Equal(t, body, response.Body.String())
}
