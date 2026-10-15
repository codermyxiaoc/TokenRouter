//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 契约：用户参数及输入资源错误不能冒充账号的模型能力故障，仅在消息提到 model 不足以冷却。
// 这些例子使用权威参数位置/错误类型以及明确缺失的输入资源，不依赖客户端提示词回显。
func TestFailoverPolicyAuditNonModel404DoesNotCoolModel(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"image_reference", `{"error":{"type":"invalid_request_error","param":"images[0]","message":"Reference image not found for model gemini-2.5-flash"}}`},
		{"missing_model_parameter", `{"error":{"code":404,"status":"INVALID_ARGUMENT","message":"model parameter not found in request"}}`},
		{"file_reference", `{"error":{"message":"Input file not found while preparing request for model gpt-5.4"}}`},
		{"cache_reference", `{"error":{"message":"Cached content not found for model gemini-2.5-flash"}}`},
		{"model_field_missing", `{"error":{"message":"model not found in request"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.False(t, isUpstreamModelNotFoundError(http.StatusNotFound, []byte(tc.body)), "请求资源不存在不能导致模型级冷却")
		})
	}
}

// 收窄分类后仍兼容官方与聚合站的模型错误格式；401 的兼容路径使用同样的参数边界。
func TestFailoverPolicyAuditModelEvidenceCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"openai_named_model", `{"error":{"type":"invalid_request_error","param":"model","message":"The model 'gpt-5.4' does not exist or you do not have access to it."}}`, true},
		{"quoted_model_unsupported", `{"error":{"message":"Model \"gpt-5.6-terra\" is not supported by any configured account in this group"}}`, true},
		{"google_model_path", `{"error":{"message":"models/gemini-2.5-flash is not found for API version v1beta"}}`, true},
		{"model_missing_past_tense", `{"error":{"message":"The requested model was not found"}}`, true},
		{"backtick_model_missing", "{\"error\":{\"message\":\"The model `gpt-5.4` does not exist\"}}", true},
		{"model_unavailable", `{"error":{"message":"The requested model or endpoint is unavailable."}}`, true},
		{"unknown_provider", `{"error":{"message":"unknown provider for model requested"}}`, true},
		{"input_parameter", `{"error":{"param":"input[0].file_id","message":"unknown model requested"}}`, false},
		{"google_invalid_argument", `{"error":{"status":"INVALID_ARGUMENT","message":"unknown model parameter requested"}}`, false},
		{"missing_model_parameter", `{"error":{"message":"model parameter not found in request"}}`, false},
		{"missing_reference_image", `{"error":{"message":"Reference image not found for model gemini-2.5-flash"}}`, false},
		{"missing_file", `{"error":{"message":"Input file not found while preparing request for model gpt-5.4"}}`, false},
		{"authentication_failure", `{"error":{"type":"authentication_error","message":"unknown model requested"}}`, false},
		{"authoritative_model_code", `{"error":{"code":"model_not_found","message":"missing"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isUpstreamModelNotFoundError(http.StatusNotFound, []byte(tc.body)))
			require.Equal(t, tc.want, isOpenAICompatibleModelNotFoundBody([]byte(tc.body)))
		})
	}
}

// 没有显式规则时保持这次修复的默认行为：池账号模型缺失立即换号，不在同账号重复请求。
func TestFailoverPolicyAuditPoolModel404WithoutRuleStillSwitches(t *testing.T) {
	for _, entry := range []string{"common", "openai", "gemini", "openai_without_persistence", "gemini_without_persistence"} {
		t.Run(entry, func(t *testing.T) {
			repo := &modelNotFoundAccountRepoStub{}
			rateLimit := &RateLimitService{accountRepo: repo}
			account := openAIModelNotFoundTempAccount()
			account.Credentials = map[string]any{"pool_mode": true}
			model := "gpt-5.4"
			body := []byte(`{"error":{"code":"model_not_found","message":"model not found"}}`)
			var decision UpstreamErrorDecision
			switch entry {
			case "common":
				decision = rateLimit.ApplyUpstreamError(context.Background(), account, http.StatusNotFound, nil, body, model)
			case "openai", "openai_without_persistence":
				gateway := &OpenAIGatewayService{}
				if entry == "openai" {
					gateway.rateLimitService = rateLimit
				}
				decision = gateway.applyOpenAIAccountUpstreamError(context.Background(), account, http.StatusNotFound, nil, body, model)
			case "gemini", "gemini_without_persistence":
				account.Platform = PlatformGemini
				model = "gemini-2.5-flash"
				gemini := &GeminiMessagesCompatService{}
				if entry == "gemini" {
					gemini.rateLimitService = rateLimit
				}
				decision = gemini.applyGeminiUpstreamErrorPolicy(context.Background(), account, http.StatusNotFound, nil, body, model)
			}
			require.True(t, decision.ShouldFailover(account, http.StatusNotFound, false))
			require.False(t, decision.RetryableOnSameAccount(account, http.StatusNotFound))
			require.Zero(t, repo.tempCalls)
			if entry == "common" || entry == "openai" || entry == "gemini" {
				require.Len(t, repo.modelRateLimitCalls, 1)
				require.Equal(t, model, repo.modelRateLimitCalls[0].scope)
				require.WithinDuration(t, time.Now().Add(time.Minute), repo.modelRateLimitCalls[0].resetAt, 3*time.Second)
			}
		})
	}
}

// 既有 CheckErrorPolicy 契约明确：池模式的管理员临时规则优先于默认规则。
// HEAD 旧版的真实三个策略入口保留 10 分钟配置；新的 60 秒模型兜底不能覆盖显式配置。
func TestFailoverPolicyAuditPoolModel404PreservesExplicitCooldown(t *testing.T) {
	for _, entry := range []string{"common", "openai", "gemini"} {
		t.Run(entry, func(t *testing.T) {
			repo := &modelNotFoundAccountRepoStub{}
			rateLimit := &RateLimitService{accountRepo: repo}
			account := openAIModelNotFoundTempAccount()
			account.Credentials["pool_mode"] = true
			account.Credentials["pool_mode_retry_status_codes"] = []any{float64(404)}
			model := "gpt-5.4"
			if entry == "gemini" {
				account.Platform = PlatformGemini
				model = "gemini-2.5-flash"
			}
			body := []byte(`{"error":{"message":"model not found"}}`)
			var decision UpstreamErrorDecision
			switch entry {
			case "common":
				decision = rateLimit.ApplyUpstreamError(context.Background(), account, http.StatusNotFound, nil, body, model)
			case "openai":
				gateway := &OpenAIGatewayService{rateLimitService: rateLimit}
				decision = gateway.applyOpenAIAccountUpstreamError(context.Background(), account, http.StatusNotFound, nil, body, model)
			case "gemini":
				gemini := &GeminiMessagesCompatService{rateLimitService: rateLimit}
				decision = gemini.applyGeminiUpstreamErrorPolicy(context.Background(), account, http.StatusNotFound, nil, body, model)
			}
			require.Len(t, repo.modelRateLimitCalls, 1)
			call := repo.modelRateLimitCalls[0]
			t.Logf("FAILOVER_POLICY_AUDIT entry=%s policy=%d cooldown_seconds=%.0f reason=%s", entry, decision.Policy, time.Until(call.resetAt).Seconds(), call.reason)
			require.Equal(t, ErrorPolicyTempUnscheduled, decision.Policy)
			require.WithinDuration(t, time.Now().Add(10*time.Minute), call.resetAt, 3*time.Second)
			require.Equal(t, model, call.scope)
			require.Zero(t, repo.tempCalls)
			require.True(t, decision.ShouldFailover(account, http.StatusNotFound, false))
			require.False(t, decision.RetryableOnSameAccount(account, http.StatusNotFound))
		})
	}
}
