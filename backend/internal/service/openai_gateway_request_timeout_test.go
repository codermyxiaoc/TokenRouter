package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShouldFailoverOpenAIUpstreamResponse_RequestTimeout(t *testing.T) {
	svc := &OpenAIGatewayService{}
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"adobe_upstream", http.StatusRequestTimeout, `{"error":{"code":"upstream_unavailable","message":"Upstream request failed","type":"adobe_upstream"}}`, true},
		{"model_overloaded", http.StatusRequestTimeout, `{"error":{"message":"Model is overloaded, please retry later","type":"rate_limit_error"}}`, true},
		{"adobe_throttled", http.StatusRequestTimeout, `{"error":{"message":"adobe throttled: status 429 retry-after=21.48s envoy=false {\"error_code\":\"rate_limited\",\"message\":\"Too many requests. Please try again later.\"}","type":"rate_limit_error"}}`, true},
		{"plain_timeout", http.StatusRequestTimeout, `Request Timeout`, true},
		{"bad_gateway", http.StatusBadGateway, `{"error":{"message":"Upstream service temporarily unavailable","type":"upstream_error"}}`, true},
		{"rate_limit", http.StatusTooManyRequests, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`, true},
		{"method_not_allowed", http.StatusMethodNotAllowed, ``, false},
		{"bad_request", http.StatusBadRequest, `{"error":{"message":"Invalid image size","type":"invalid_request_error"}}`, false},
		{"not_found", http.StatusNotFound, `{"error":{"message":"Not found"}}`, false},
		{"unprocessable_entity", http.StatusUnprocessableEntity, `{"error":{"message":"Invalid request"}}`, false},
		{"cyber_policy", http.StatusRequestTimeout, `{"error":{"code":"cyber_policy","message":"blocked"}}`, false},
		{"context_length", http.StatusRequestTimeout, `{"error":{"code":"context_length_exceeded","message":"Maximum context length exceeded"}}`, false},
		{"context_window", http.StatusRequestTimeout, `{"error":{"message":"Input exceeds the context window"}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			require.Equal(t, tt.want, svc.shouldFailoverTextHTTPResponse(context.Background(), &Account{Platform: PlatformOpenAI}, tt.status, extractUpstreamErrorMessage(body), body))
		})
	}
}

// 同一 HTTP 超时分别走文本、专用图片及兼容平台的分类入口。
func TestRequestTimeoutFailoverIsScopedToOpenAIText(t *testing.T) {
	svc := &OpenAIGatewayService{}
	ctx := context.Background()
	for _, platform := range []string{PlatformOpenAI, PlatformGrok, PlatformDeepseek, PlatformOpenCodeGo, PlatformKimi} {
		t.Run(platform, func(t *testing.T) {
			got := svc.shouldFailoverTextHTTPResponse(ctx, &Account{Platform: platform}, http.StatusRequestTimeout, "", nil)
			require.Equal(t, platform == PlatformOpenAI, got)
		})
	}
	imageCtx, _ := WithImageAttemptPolicy(ctx, DefaultPublicImageAttemptTimeout)
	defer CancelImageAttemptPolicy(imageCtx)
	require.False(t, svc.shouldFailoverTextHTTPResponse(imageCtx, &Account{Platform: PlatformOpenAI}, http.StatusRequestTimeout, "", nil))
	require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(http.StatusRequestTimeout, "", nil))
	require.False(t, svc.shouldFailoverGrokUpstreamError(http.StatusRequestTimeout, nil))
}
