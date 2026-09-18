//go:build unit

package service

import (
	"context"
	"errors"
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

// nativeStreamDisconnectWriter 在已有文本成功写出后模拟客户端断开，不使用真实网络。
type nativeStreamDisconnectWriter struct {
	gin.ResponseWriter
	cancel context.CancelFunc
	failed bool
}

func (w *nativeStreamDisconnectWriter) Write(body []byte) (int, error) {
	if w.failed || strings.Contains(string(body), "disconnect-marker") {
		w.failed = true
		w.cancel()
		return 0, io.ErrClosedPipe
	}
	return w.ResponseWriter.Write(body)
}

func (w *nativeStreamDisconnectWriter) WriteString(body string) (int, error) {
	return w.Write([]byte(body))
}

// 客户端断开后仍读到的显式上游失败必须保留 Ops 事实，不能依赖已关闭的下游捕获错误帧。
func TestOpenAINativeStreamDisconnectedClientRetainsUpstreamFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const prefix = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial-output\"}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"disconnect-marker\"}\n\n"
	const failed = "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_local\",\"status\":\"failed\",\"error\":{\"type\":\"server_error\",\"status\":503,\"message\":\"local unavailable\"}}}\n\n"
	const bare = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"status\":503,\"message\":\"local unavailable\"}}\n\n"
	const completed = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_local\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":3}}}\n\n"
	for _, passthrough := range []bool{false, true} {
		mode := "native"
		if passthrough {
			mode = "passthrough"
		}
		for _, test := range []struct {
			name          string
			tail          string
			wantFailure   bool
			wantRecovered bool
		}{
			{name: "failed_after_disconnect", tail: failed, wantFailure: true},
			{name: "bare_then_failed_once", tail: bare + failed, wantFailure: true},
			{name: "bare_then_eof_once", tail: bare, wantFailure: true},
			{name: "client_disconnect_with_successful_upstream", tail: completed},
			{name: "bare_error_recovered_by_successful_terminal", tail: bare + completed, wantRecovered: true},
		} {
			t.Run(mode+"/"+test.name, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				writer := &nativeStreamDisconnectWriter{ResponseWriter: c.Writer, cancel: cancel}
				c.Writer = writer
				account := &Account{ID: 19, Name: "local-account", Platform: PlatformOpenAI, Type: AccountTypeOAuth}
				svc := &OpenAIGatewayService{
					cfg:           &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
					toolCorrector: NewCodexToolCorrector(),
				}
				resp := &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"local-upstream"}},
					Body:       io.NopCloser(strings.NewReader(prefix + test.tail)),
				}
				var err error
				if passthrough {
					_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, account, time.Now(), "gpt-6-astra", "gpt-6-astra")
				} else {
					_, err = svc.handleStreamingResponse(ctx, resp, c, account, time.Now(), "gpt-6-astra", "gpt-6-astra")
				}
				require.True(t, writer.failed)
				require.ErrorIs(t, ctx.Err(), context.Canceled)
				require.Contains(t, recorder.Body.String(), "partial-output")
				require.NotContains(t, recorder.Body.String(), "response.failed", "关闭后的客户端不能接收到后续错误帧")
				var failover *UpstreamFailoverError
				require.False(t, errors.As(err, &failover), "已输出业务内容不能重放本次请求")
				if !test.wantFailure {
					require.NoError(t, err)
					require.Empty(t, GetOpsStreamErrors(c))
					if test.wantRecovered {
						// 成功终态不抹去已观测到的上游错误，但不能把请求标为失败。
						require.Equal(t, http.StatusServiceUnavailable, c.GetInt(OpsUpstreamStatusCodeKey))
						events := busyTestUpstreamEvents(c)
						require.Len(t, events, 1)
						require.Equal(t, "stream_error_recovered", events[0].Kind)
						require.Equal(t, http.StatusServiceUnavailable, events[0].UpstreamStatusCode)
					} else {
						require.Zero(t, c.GetInt(OpsUpstreamStatusCodeKey), "纯客户端断开不能伪造上游 503")
						require.Empty(t, busyTestUpstreamEvents(c), "没有上游错误帧时不得生成错误事实")
					}
					return
				}
				require.Error(t, err)
				require.Equal(t, http.StatusServiceUnavailable, c.GetInt(OpsUpstreamStatusCodeKey))
				value, exists := c.Get(OpsUpstreamErrorsKey)
				require.True(t, exists)
				events, valid := value.([]*OpsUpstreamErrorEvent)
				require.True(t, valid)
				require.Len(t, events, 1, "裸错误与失败终态只记录一次上游错误")
				require.Equal(t, http.StatusServiceUnavailable, events[0].UpstreamStatusCode)
				require.Len(t, GetOpsStreamErrors(c), 1, "HTTP 200 已提交的失败须标记为请求失败")
			})
		}
	}
}
