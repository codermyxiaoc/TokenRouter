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

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 上游排水可能在客户端取消之后收到显式错误；取消本身不能抹去这个独立的失败事实。
type chatDrainCancelReader struct {
	io.Reader
	cancel context.CancelFunc
}

func (r chatDrainCancelReader) Read(p []byte) (int, error) {
	r.cancel()
	return r.Reader.Read(p)
}

func TestSmartRoutingChatDrainExplicitErrorAfterClientCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, protocol := range []string{"responses", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			ctx, _ := WithSmartRoutingAttempt(context.Background())
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+protocol, nil).WithContext(ctx)
			resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(io.MultiReader(
				strings.NewReader(smartRoutingChatRolePrelude+smartRoutingChatContent),
				chatDrainCancelReader{Reader: strings.NewReader(smartRoutingChat503Error), cancel: cancel},
			))}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
			account := forceChatResponsesFallbackAccount()
			var result *OpenAIForwardResult
			var err error
			if protocol == "responses" {
				result, err = svc.streamChatCompletionsAsResponses(c, resp, "gpt-6-astra", nil, nil, false, nil, "gpt-6-astra", "gpt-6-astra", nil, nil, time.Now(), account)
			} else {
				result, err = svc.streamChatCompletionsAsAnthropic(c, resp, "gpt-6-astra", "gpt-6-astra", "gpt-6-astra", nil, nil, time.Now(), account)
			}
			require.Error(t, err)
			require.NotNil(t, result)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover), "客户端取消后不能继续重试")
			require.Contains(t, recorder.Body.String(), "partial-output")
			require.NotContains(t, recorder.Body.String(), "response.completed")
			require.NotContains(t, recorder.Body.String(), "message_stop")
			smartRoutingAssertChatStreamOps(t, c, 503)
		})
	}
}
