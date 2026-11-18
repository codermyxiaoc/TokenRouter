//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Anthropic→Responses/Chat 桥接路径记录上游 usage.speed，计费阶段据此只降档。
func TestGatewayBridgeObservesUpstreamServiceTier(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-4-8\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":0,\"speed\":\"standard\"}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1,\"speed\":\"standard\"}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	svc := &GatewayService{}
	for _, tc := range []struct {
		name string
		run  func(c *gin.Context, resp *http.Response) (*ForwardResult, error)
	}{
		{"responses buffered", func(c *gin.Context, resp *http.Response) (*ForwardResult, error) {
			return svc.handleResponsesBufferedStreamingResponse(resp, c, "claude-opus-4-8", "claude-opus-4-8", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
		}},
		{"responses stream", func(c *gin.Context, resp *http.Response) (*ForwardResult, error) {
			return svc.handleResponsesStreamingResponse(resp, c, "claude-opus-4-8", "claude-opus-4-8", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(sse))}
			result, err := tc.run(c, resp)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotEmpty(t, result.UpstreamResponseServiceTier)
		})
	}
	_ = context.Background()
}
