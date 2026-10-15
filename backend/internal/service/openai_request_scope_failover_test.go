//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// 使用实际错误处理路径验证请求终态标记，避免服务层停止后仍被智能路由再次提交。
func TestOpenAIRequestScopedClientErrorsAcrossProtocols(t *testing.T) {
	for _, endpoint := range []string{"responses", "chat", "messages", "passthrough"} {
		for _, tc := range []struct {
			name, body string
			status     int
		}{
			{"schema", openAIInvalidFunctionParametersBody, 400},
			{"json_schema", `{"error":{"type":"invalid_request_error","code":"invalid_json_schema","message":"Invalid JSON schema"}}`, 400},
			{"property", `{"error":{"type":"invalid_request_error","code":"property_name_above_max_length","message":"property name too long"}}`, 400},
			{"body_limit", `{"error":{"message":"payload too large"}}`, 413},
		} {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				c, rec := newOpenAIUpstreamClientErrorTestContext()
				repo := &modelNotFoundAccountRepoStub{}
				svc := &OpenAIGatewayService{rateLimitService: &RateLimitService{accountRepo: repo}}
				account := newOpenAIUpstreamClientErrorTestAccount()
				account.Type = AccountTypeAPIKey
				account.Credentials = map[string]any{"pool_mode": true, "pool_mode_retry_status_codes": []any{float64(400), float64(413)}}
				resp := newOpenAIUpstreamClientErrorResponse(tc.status, tc.body)
				var err error
				switch endpoint {
				case "responses":
					_, err = svc.handleErrorResponse(context.Background(), resp, c, account, []byte(`{"model":"gpt-5.6-terra"}`))
				case "chat":
					_, err = svc.handleCompatErrorResponse(resp, c, account, writeChatCompletionsError, writeChatCompletionsErrorBody)
				case "messages":
					_, err = svc.handleCompatErrorResponse(resp, c, account, writeAnthropicError, writeAnthropicErrorBody)
				case "passthrough":
					err = svc.handleErrorResponsePassthrough(context.Background(), resp, c, account, []byte(`{"model":"gpt-5.6-terra"}`), []byte(tc.body))
				}
				require.Error(t, err)
				var failure *UpstreamFailoverError
				require.False(t, errors.As(err, &failure))
				require.True(t, IsOpsRequestScopedError(c))
				require.Equal(t, tc.status, rec.Code)
				require.Zero(t, repo.tempCalls)
				require.Empty(t, repo.modelRateLimitCalls)
				require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
			})
		}
	}
}

// 相同单词出现在回显请求中不能改变上游故障分类；可恢复的普通400仍交给池重试。
func TestOpenAIClientSchemaClassificationUsesAuthoritativeCode(t *testing.T) {
	for _, body := range []string{
		`{"error":{"type":"server_error","code":"invalid_json_schema"}}`,
		`{"request":{"error":{"type":"invalid_request_error","code":"invalid_json_schema"}},"error":{"type":"invalid_request_error","message":"Invalid JSON schema"}}`,
		`{"error":{"type":"invalid_request_error","message":"temporary failure","code":"upstream_failure"}}`,
	} {
		require.False(t, isOpenAIClientInvalidRequestError(http.StatusBadRequest, "", []byte(body)))
	}
	c, _ := newOpenAIUpstreamClientErrorTestContext()
	account := newOpenAIUpstreamClientErrorTestAccount()
	account.Type = AccountTypeAPIKey
	account.Credentials = map[string]any{"pool_mode": true, "pool_mode_retry_status_codes": []any{float64(400)}}
	svc := &OpenAIGatewayService{}
	_, err := svc.handleErrorResponse(context.Background(), newOpenAIUpstreamClientErrorResponse(400, `{"error":{"type":"invalid_request_error","message":"temporary upstream failure"}}`), c, account, nil)
	var failure *UpstreamFailoverError
	require.ErrorAs(t, err, &failure)
	require.True(t, failure.RetryableOnSameAccount)
	require.False(t, IsOpsRequestScopedError(c))
}

// 从公开转发入口经过真实上游错误分类后，只发送一次请求，保留供智能路由读取的终态标记。
func TestOpenAIForwardClientSchemaErrorDoesNotRetry(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: newOpenAIUpstreamClientErrorResponse(http.StatusBadRequest, openAIInvalidFunctionParametersBody)}
	svc := newOpenAIRejectedFieldTestService(upstream)
	account := newOpenAIRejectedFieldTestAccount()
	account.Credentials["pool_mode"] = true
	account.Credentials["pool_mode_retry_status_codes"] = []any{float64(http.StatusBadRequest)}
	body := []byte(`{"model":"gpt-5.6-terra","input":"hello","stream":false}`)
	c := newOpenAIRejectedFieldTestContext(body)
	result, err := svc.Forward(context.Background(), c, account, body)
	require.Nil(t, result)
	require.Error(t, err)
	var failure *UpstreamFailoverError
	require.False(t, errors.As(err, &failure))
	require.Len(t, upstream.requests, 1)
	require.Equal(t, http.StatusBadRequest, c.Writer.Status())
	require.True(t, IsOpsRequestScopedError(c))
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

// 默认不保存错误体时，真实转发入口也必须提供模型缺失的稳定原因供智能路由使用。
func TestOpenAIForwardModelMissingRetainsReasonWithoutBodyLogging(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: newOpenAIUpstreamClientErrorResponse(http.StatusNotFound, `{"error":{"code":"model_not_found","message":"The requested model does not exist"}}`)}
	svc := newOpenAIRejectedFieldTestService(upstream)
	account := newOpenAIRejectedFieldTestAccount()
	account.Credentials["pool_mode"] = true
	account.Credentials["pool_mode_retry_status_codes"] = []any{float64(http.StatusNotFound)}
	body := []byte(`{"model":"gpt-5.6-terra","input":"hello","stream":false}`)
	c := newOpenAIRejectedFieldTestContext(body)
	result, err := svc.Forward(context.Background(), c, account, body)
	require.Nil(t, result)
	var failure *UpstreamFailoverError
	require.ErrorAs(t, err, &failure)
	require.False(t, failure.RetryableOnSameAccount)
	require.Len(t, upstream.requests, 1)
	require.False(t, c.Writer.Written())
	events := upstreamErrorEventsFromContext(t, c)
	require.NotEmpty(t, events)
	require.Equal(t, "model_not_found", events[0].Reason)
	require.Empty(t, events[0].UpstreamResponseBody)
}
