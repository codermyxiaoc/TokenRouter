//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 从真实图片执行器入口读取响应，避免仅测分类函数时遗漏 code/type 在调用链中被丢弃的问题。
func TestCreativeImageHTTPFailuresPreserveStructuredReason(t *testing.T) {
	for _, provider := range []struct{ platform, model string }{
		{PlatformOpenAI, "gpt-image-2"},
		{PlatformGrok, "grok-imagine-image-2.0"},
		{PlatformGemini, "gemini-3.1-flash-image-preview"},
	} {
		for _, tc := range []struct {
			name, body, code string
			status           int
		}{
			{"审核代码", `{"error":{"code":"content_policy_violation","message":"Request rejected private-secret"}}`, "PROVIDER_CONTENT_BLOCKED", 400},
			{"配额类型", `{"error":{"type":"insufficient_quota","message":"Try again private-secret"}}`, "PROVIDER_QUOTA_EXHAUSTED", 429},
			{"模型代码", `{"error":{"code":"model_not_found","message":"Unavailable private-secret"}}`, "PROVIDER_MODEL_UNAVAILABLE", 404},
			{"机器代码优先", `{"error":{"code":"model_not_found","message":"Request prompt: NSFW private-secret"}}`, "PROVIDER_MODEL_UNAVAILABLE", 400},
			{"未知类型回退明确消息", `{"error":{"type":"invalid_request_error","message":"Blocked by safety policy private-secret"}}`, "PROVIDER_CONTENT_BLOCKED", 400},
			{"审核服务故障", `{"error":{"message":"moderation service unavailable private-secret"}}`, "PROVIDER_UNAVAILABLE", 503},
			{"审核服务超时", `{"error":{"message":"safety service timed out private-secret"}}`, "PROVIDER_TIMEOUT", 504},
			{"五百优先服务故障", `{"error":{"code":"content_policy_violation","message":"private-secret"}}`, "PROVIDER_UNAVAILABLE", 500},
			{"忽略普通业务字段", `{"prompt":"content_policy_violation","error":{"message":"private-secret"}}`, "PROVIDER_INVALID_PARAMS", 400},
		} {
			t.Run(provider.platform+"/"+tc.name, func(t *testing.T) {
				cfg := &config.Config{}
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: tc.status, Header: http.Header{},
					Body: io.NopCloser(strings.NewReader(tc.body)),
				}}
				executor := &CreativeExecutor{cfg: cfg, gateway: &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}}
				_, err := executor.Execute(context.Background(), CreativeRun{
					Operation: CreativeOperationGenerate, RequestedOutputCount: 1, ImageSize: "1K", AspectRatio: "1:1",
				}, CreativeRunPayload{Prompt: "local fixture"}, &CreativeExecution{
					Account:       &Account{ID: 1, Type: AccountTypeAPIKey, Platform: provider.platform, Credentials: map[string]any{"api_key": "local-key"}},
					UpstreamModel: provider.model,
				})
				require.Error(t, err)
				require.Len(t, upstream.requests, 1)
				code, message := creativeExecuteErrorParts(err)
				require.Equal(t, tc.code, code)
				require.Equal(t, creativeProviderFailureMessage(tc.code), message)
				require.NotContains(t, err.Error(), "private-secret")
				require.NotContains(t, message, "private-secret")
				require.Equal(t, tc.status == 429 || tc.status >= 500, IsRetryableCreativeError(err))
			})
		}
	}
}

// 有界提取只使用错误字段；超长和非法响应按 HTTP 状态分类，不带出原包。
func TestCreativeProviderResponseDiagnosticLimits(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`<html>content_policy_violation</html>`),
		[]byte(`{"prompt":"content_policy_violation","url":"https://private.invalid/insufficient_quota"}`),
		[]byte(`{"error":{"code":"` + strings.Repeat("content_policy", 1024) + `"}}`),
		[]byte(`{"error":{"message":"` + strings.Repeat("x", 1<<20) + `"}}`),
	} {
		require.Empty(t, creativeProviderResponseDiagnostic(raw))
		err := creativeHTTPResponseError(http.StatusBadRequest, raw)
		require.Equal(t, "PROVIDER_INVALID_PARAMS", err.Code)
		require.NotContains(t, err.Error(), "private.invalid")
	}
	require.Empty(t, creativeProviderDiagnosticCode("moderation service unavailable"))
	require.Equal(t, "PROVIDER_TIMEOUT", creativeProviderFailureCode(504, "safety service timed out"))
	require.Equal(t, "PROVIDER_UNAVAILABLE", creativeProviderFailureCode(503, "moderation service unavailable"))
}
