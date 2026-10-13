//go:build unit

package service

import (
	"bytes"
	"context"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 显式强制 Responses 的账号不能因输入类型错误悄悄改走 Chat；默认保留协议直接使用 Chat。
func TestStructuredInputErrorPreservesConfiguredProtocol(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"preserve_client_protocol", "force_responses"} {
		t.Run(mode, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_type","param":"input","message":"Expected a string for input, but got an object instead."}}`))}}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.example.com", "api_key": "test"}, Extra: map[string]any{"openai_text_route_mode": mode, "openai_responses_probe_status": "supported"}}
			_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-5.4")
			require.Error(t, err)
			require.Len(t, upstream.requests, 1)
			wantPath := "/v1/chat/completions"
			if mode == "force_responses" {
				wantPath = "/v1/responses"
			}
			require.Equal(t, wantPath, upstream.lastReq.URL.Path)
		})
	}
}

// 错误体达到读取上限时必须标记不完整，且下游重读只能看到同一段有界内容。
func TestReadOpenAIUpstreamErrorCompleteness(t *testing.T) {
	limit := int(openAIUpstreamErrorBodyReadLimitForConfig(nil))
	for _, size := range []int{limit - 1, limit, limit + 1} {
		resp := &http.Response{Body: io.NopCloser(strings.NewReader(strings.Repeat("x", size)))}
		body, _, complete := (&OpenAIGatewayService{}).readOpenAIUpstreamErrorWithCompleteness(resp)
		require.Equal(t, size <= limit, complete)
		require.Len(t, body, min(size, limit))
		replayed, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, body, replayed)
	}
}
