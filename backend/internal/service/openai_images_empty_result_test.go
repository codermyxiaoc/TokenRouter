//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestOpenAIImagesAPIKeyEmptySuccessDoesNotInventUsage 防止请求 n 在没有实际图片时变成扣费数量。
func TestOpenAIImagesAPIKeyEmptySuccessDoesNotInventUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, responseBody := range []string{`{}`, `{"data":[]}`, `{"data":[{}]}`, `not-json`} {
		t.Run(responseBody, func(t *testing.T) {
			body := []byte(`{"model":"gpt-image-2","prompt":"local test","n":3}`)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, openAIImagesGenerationsEndpoint, strings.NewReader(string(body)))
			c.Request.Header.Set("Content-Type", "application/json")
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK,
				Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(responseBody))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "local-test", "base_url": "https://compatible.example/v1"}}
			result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
			require.Nil(t, result)
			code, _, readFailure := OpenAIUpstreamStreamReadErrorDetails(err)
			require.True(t, readFailure)
			require.Equal(t, OpenAIUpstreamStreamTruncatedCode, code)
			require.False(t, c.Writer.Written(), "空结果不能先提交成功响应")
		})
	}
}
