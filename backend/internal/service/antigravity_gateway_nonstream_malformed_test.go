package service

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 非流式聚合遇到 MALFORMED_FUNCTION_CALL 时返回 502 malformed_function_call，正常工具调用与文本不受影响。
func TestAntigravityMalformedFunctionCallNonStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		frames string
		status int
	}{
		{"signature only", `data: {"response":{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL","content":{"parts":[{"thoughtSignature":"test-signature"}]}}]}}` + "\n\n", http.StatusBadGateway},
		{"error after parts", `data: {"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read","args":{}}}]}}]}}` + "\n\n" + `data: {"response":{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL"}]}}` + "\n\n", http.StatusBadGateway},
		{"error before usage", `data: {"response":{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL"}]}}` + "\n\n" + `data: {"response":{"usageMetadata":{"promptTokenCount":1}}}` + "\n\n", http.StatusBadGateway},
		{"normal tool", `data: {"response":{"candidates":[{"finishReason":"STOP","content":{"parts":[{"functionCall":{"name":"read","args":{}}}]}}]}}` + "\n\n", http.StatusOK},
		{"normal text", `data: {"response":{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"hello"}]}}]}}` + "\n\n", http.StatusOK},
	} {
		for _, protocol := range []string{"responses", "chat"} {
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, nil)
				c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/responses", nil)
				resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tc.frames))}
				// 本站 Responses 处理多一个客户端工具映射参数，这里用空映射包装成统一签名。
				handle := func(c *gin.Context, resp *http.Response, started time.Time, model string) (*antigravityStreamResult, error) {
					return svc.handleResponsesNonStreamingFromAntigravity(c, resp, started, model, apicompat.ResponsesClientToolMapping{})
				}
				if protocol == "chat" {
					handle = svc.handleChatCompletionsNonStreamingFromAntigravity
				}
				_, err := handle(c, resp, time.Now(), "test-model")
				require.Equal(t, tc.status, recorder.Code)
				if tc.status == http.StatusBadGateway {
					require.Error(t, err)
					require.Contains(t, recorder.Body.String(), `"type":"malformed_function_call"`)
					require.Contains(t, recorder.Body.String(), "MALFORMED_FUNCTION_CALL")
					require.NotContains(t, recorder.Body.String(), `"status":"completed"`)
				} else {
					require.NoError(t, err)
					if protocol == "responses" {
						require.Contains(t, recorder.Body.String(), `"status":"completed"`)
					} else {
						require.Contains(t, recorder.Body.String(), `"choices"`)
					}
				}
			})
		}
	}
}
