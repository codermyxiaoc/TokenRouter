//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

const geminiInvalidContentsError = `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"* GenerateContentRequest.contents: contents is not specified"}}`

// 只依据权威错误状态和参数路径判定，模型、凭据、项目和签名修复仍走既有恢复链路。
func TestGeminiClientParameterErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    bool
	}{
		{"* GenerateContentRequest.contents: contents is not specified", true},
		{"* GenerateContentRequest.tools[0].function_declarations[0].parameters: should be non-empty", true},
		{"Invalid value at 'generation_config.temperature' (TYPE_FLOAT)", true},
		{"Invalid JSON payload received. Unknown name \"extra\": Cannot find field.", true},
		{"The requested model is not supported", false},
		{"Invalid project resource name: projects/bad", false},
		{"API key not valid. Please pass a valid API key.", false},
		{"GenerateContentRequest.contents: thought_signature is required", false},
		{"invalid generation request", false},
	} {
		t.Run(tc.message, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"error": map[string]any{"status": "INVALID_ARGUMENT", "message": tc.message}})
			require.NoError(t, err)
			require.Equal(t, tc.want, isGeminiClientInvalidRequestError(400, body))
			require.False(t, isGeminiClientInvalidRequestError(503, body))
		})
	}
	require.False(t, isGeminiClientInvalidRequestError(400, []byte(`{"error":{"status":"UNAVAILABLE","message":"GenerateContentRequest.contents: required"}}`)))
	require.False(t, isGeminiClientInvalidRequestError(400, []byte(`{"request":{"error":{"status":"INVALID_ARGUMENT","message":"GenerateContentRequest.contents: required"}},"error":{"status":"INVALID_ARGUMENT","message":"upstream failed"}}`)))
}

// 四个公开入口都需要标记用户参数终态，不能由池配置或智能路由继续提交或冷却健康账号。
func TestGeminiClientParameterErrorAcrossProtocols(t *testing.T) {
	for _, protocol := range []string{"native", "messages", "chat", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			svc := newGeminiSignalService("application/json", geminiInvalidContentsError)
			svc.httpUpstream.(*geminiCompatHTTPUpstreamStub).response.StatusCode = http.StatusBadRequest
			account := geminiSignalTestAccount()
			account.Credentials["pool_mode"] = true
			account.Credentials["pool_mode_retry_status_codes"] = []any{float64(400)}
			account.Credentials["custom_error_codes_enabled"] = true
			account.Credentials["custom_error_codes"] = []any{float64(400)}
			c, rec := newGeminiNativeTestContext(t)
			var err error
			switch protocol {
			case "native":
				_, err = svc.ForwardNative(context.Background(), c, account, "gemini-3.7-flash", "generateContent", false, geminiSignalTestRequest())
			case "messages":
				_, err = svc.Forward(context.Background(), c, account, []byte(`{"model":"gemini-3.7-flash","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`))
			case "chat":
				_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(`{"model":"gemini-3.7-flash","messages":[{"role":"user","content":"hello"}]}`))
			case "responses":
				_, err = svc.ForwardAsResponses(context.Background(), c, account, []byte(`{"model":"gemini-3.7-flash","input":"hello"}`), nil)
			}
			require.Error(t, err)
			var failure *UpstreamFailoverError
			require.False(t, errors.As(err, &failure))
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.True(t, IsOpsRequestScopedError(c))
			repo := svc.accountRepo.(*errorPolicyRepoStub)
			require.Zero(t, repo.tempCalls)
			require.Zero(t, repo.setErrCalls)
			require.Empty(t, repo.modelRateLimitCalls)
		})
	}
}

// HTTP 200 内携带的明确用户参数错误同样不能计入账号健康失败。
func TestGeminiClientParameterInBandErrorIsRequestScoped(t *testing.T) {
	svc := newGeminiSignalService("application/json", geminiInvalidContentsError)
	c, _ := newGeminiNativeTestContext(t)
	_, err := svc.ForwardNative(context.Background(), c, geminiSignalTestAccount(), "gemini-3.7-flash", "generateContent", false, geminiSignalTestRequest())
	require.NoError(t, err)
	require.True(t, IsOpsRequestScopedError(c))
	streamErrors := GetOpsStreamErrors(c)
	require.NotEmpty(t, streamErrors)
	for _, event := range streamErrors {
		require.True(t, event.RequestScoped)
		require.False(t, event.CountTowardsSLA)
	}
}
