//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestSmartRoutingResponsesChatHTTPFailureKeepsReplayEvidence 使用真实 Forward 和协议转换，
// 验证 HTTP 503/524 在响应开始前保留原始故障事实，而不是只验证假 handler 写出的 JSON。
func TestSmartRoutingResponsesChatHTTPFailureKeepsReplayEvidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, status := range []int{http.StatusServiceUnavailable, 524} {
		for _, contentType := range []string{"application/json", "text/event-stream"} {
			t.Run(fmt.Sprintf("%d_%s", status, contentType), func(t *testing.T) {
				body := []byte(`{"model":"gpt-6-astra","input":"hello","stream":true}`)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				ctx, attempt := WithSmartRoutingAttempt(context.Background())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
				c.Request.Header.Set("Content-Type", "application/json")
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: status,
					Header:     http.Header{"Content-Type": []string{contentType}, "X-Request-Id": []string{"local-http-503"}},
					Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"upstream maintenance","type":"server_error"}}`)),
				}}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				result, err := svc.Forward(ctx, c, forceChatResponsesFallbackAccount(), body)
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.Equal(t, status, failover.StatusCode)
				require.Nil(t, result, "HTTP 失败不能产生可结算的部分结果")
				require.False(t, c.Writer.Written(), "尚未收到成功流，桥不能提前提交响应头或正文")
				require.Empty(t, recorder.Body.String())
				require.True(t, attempt.CanReplay())
				require.False(t, HasOpsClientBusinessLimited(c))
				require.Equal(t, "/v1/chat/completions", upstream.lastReq.URL.Path)
				require.Equal(t, "gpt-6-astra", gjson.GetBytes(upstream.lastBody, "model").String())
				require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
				events, ok := c.Get(OpsUpstreamErrorsKey)
				require.True(t, ok)
				require.NotEmpty(t, events.([]*OpsUpstreamErrorEvent))
				require.Equal(t, status, events.([]*OpsUpstreamErrorEvent)[0].UpstreamStatusCode)
			})
		}
	}
}
