package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestForwardOpenCodeSystemOnePreservesStructuredRequestAndUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"jev-1.13","state":"classify","questions":{"is_urgent":{"type":"noul","instructions":"Is this urgent?"}}}`)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"jev-rid"}},
		Body:       io.NopCloser(bytes.NewBufferString(`{"model":"jev-1.13","answers":{"is_urgent":{"type":"noul","value":false}},"usage":{"input_tokens":12,"output_tokens":1}}`)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 7, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"account_mode": AccountModeZen, "api_protocol": APIProtocolAdaptive, "api_key": "sk-jev",
	}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/systemone", bytes.NewReader(body))

	result, err := svc.ForwardOpenCodeSystemOne(context.Background(), c, account, body)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "jev-rid", result.RequestID)
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Equal(t, 1, result.Usage.OutputTokens)
	require.Equal(t, "/v1/systemone", result.UpstreamEndpoint)
	require.Equal(t, "/v1/systemone", GetActualOpenAIUpstreamEndpoint(c))
	require.Equal(t, body, upstream.lastBody)
	require.Equal(t, "Bearer sk-jev", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "https://opencode.ai/zen/v1/systemone", upstream.lastReq.URL.String())
}

func TestForwardOpenCodeSystemOneReturnsFailoverBeforeWritingRetryableError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"temporarily unavailable"}}`)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 8, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"account_mode": AccountModeZen, "api_protocol": APIProtocolAdaptive, "api_key": "sk-jev",
	}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/systemone", bytes.NewReader([]byte(`{"model":"jev-1.13","state":"x","questions":{"q":{"type":"noul","instructions":"?"}}}`)))

	_, err := svc.ForwardOpenCodeSystemOne(context.Background(), c, account, []byte(`{"model":"jev-1.13","state":"x","questions":{"q":{"type":"noul","instructions":"?"}}}`))

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusServiceUnavailable, failoverErr.StatusCode)
	require.Empty(t, recorder.Body.String(), "可重试错误必须在输出前返回给 handler 以便切换账号")
}
