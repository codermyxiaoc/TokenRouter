//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// smartRoutingStreamReadError 模拟已经读完前导数据后连接异常截断，禁止使用真实上游。
type smartRoutingStreamReadError struct {
	err         error
	beforeError func()
}

func (r smartRoutingStreamReadError) Read([]byte) (int, error) {
	if r.beforeError != nil {
		r.beforeError()
	}
	return 0, r.err
}

type smartRoutingChatStreamOptions struct {
	contentType  string
	readErr      error
	cancelClient bool
	accountSetup func(*Account)
	passthrough  *ErrorPassthroughService
}

// smartRoutingForwardChatStream 使用生产 Forward、真实 Responses→Chat 转换和流扫描。
// HTTPUpstream 只返回内存中的模拟响应，账号凭据不用于任何网络请求。
func smartRoutingForwardChatStream(t *testing.T, data string, readError bool, options ...smartRoutingChatStreamOptions) (*OpenAIForwardResult, error, *gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	body := []byte(`{"model":"gpt-6-astra","stream":true,"input":[{"type":"reasoning","id":"rs_local","encrypted_content":"opaque-local","summary":[{"type":"summary_text","text":"local history"}]},{"role":"user","content":"hello"}]}`)
	reader := io.Reader(strings.NewReader(data))
	contentType := "text/event-stream"
	readErr := error(io.ErrUnexpectedEOF)
	var cancelClient context.CancelFunc
	if len(options) > 0 {
		if options[0].contentType != "" {
			contentType = options[0].contentType
		}
		if options[0].readErr != nil {
			readErr = options[0].readErr
		}
	}
	if readError {
		reader = io.MultiReader(reader, smartRoutingStreamReadError{err: readErr, beforeError: func() {
			if cancelClient != nil {
				cancelClient()
			}
		}})
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{contentType}, "X-Request-Id": []string{"local-chat-stream"}},
		Body:       io.NopCloser(reader),
	}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx, _ := WithSmartRoutingAttempt(context.Background())
	if len(options) > 0 && options[0].cancelClient {
		ctx, cancelClient = context.WithCancel(ctx)
		t.Cleanup(cancelClient)
	}
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	account := forceChatResponsesFallbackAccount()
	if len(options) > 0 {
		if options[0].accountSetup != nil {
			options[0].accountSetup(account)
		}
		BindErrorPassthroughService(c, options[0].passthrough)
	}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	result, err := svc.Forward(ctx, c, account, body)
	require.Equal(t, "/v1/chat/completions", upstream.lastReq.URL.Path)
	return result, err, c, recorder
}

const smartRoutingChatRolePrelude = "data: {\"id\":\"chatcmpl-local\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-6-astra\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n"
const smartRoutingChatContent = "data: {\"id\":\"chatcmpl-local\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-6-astra\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial-output\"}}]}\n\n"
const smartRoutingChat503Error = "event: error\ndata: {\"error\":{\"type\":\"server_error\",\"message\":\"local upstream unavailable\",\"status\":503}}\n\n"

// 无业务输出前的流内错误与截断必须保留可切换错误，不能伪造空成功或产生可结算结果。
func TestSmartRoutingChatStreamFailureBeforeOutputRetainsFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name        string
		stream      string
		readError   bool
		status      int
		contentType string
		readErr     error
	}{
		{name: "http_200_error_503", stream: smartRoutingChat503Error, status: 503},
		{name: "role_error_done", stream: smartRoutingChatRolePrelude + smartRoutingChat503Error + "data: [DONE]\n\n", status: 503},
		{name: "http_200_rate_limit", stream: "data: {\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"local rate limit\"}}\n\n", status: 429},
		{name: "http_200_timeout_524", stream: "data: {\"error\":{\"type\":\"server_error\",\"status_code\":524,\"message\":\"local edge timeout\"}}\n\n", status: 524},
		{name: "top_level_status_code_524", stream: "data: {\"status_code\":524,\"error\":{\"type\":\"server_error\",\"message\":\"local edge timeout\"}}\n\n", status: 524},
		{name: "top_level_status_503", stream: "data: {\"status\":503,\"error\":{\"type\":\"server_error\",\"message\":\"local unavailable\"}}\n\n", status: 503},
		{name: "http_200_json_error", stream: "{\n\"error\":{\"type\":\"server_error\",\"status\":503,\"message\":\"local unavailable\"}\n}", status: 503, contentType: "application/json"},
		{name: "empty_eof", status: 502},
		{name: "done_only", stream: "data: [DONE]\n\n", status: 502},
		{name: "role_only_eof", stream: smartRoutingChatRolePrelude, status: 502},
		{name: "unexpected_eof_before_chunk", readError: true, status: 502},
		{name: "unexpected_eof_after_role", stream: smartRoutingChatRolePrelude, readError: true, status: 502},
		{name: "active_client_upstream_deadline", stream: smartRoutingChatRolePrelude, readError: true, readErr: context.DeadlineExceeded, status: 502},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err, c, recorder := smartRoutingForwardChatStream(t, test.stream, test.readError, smartRoutingChatStreamOptions{contentType: test.contentType, readErr: test.readErr})
			var failure *UpstreamFailoverError
			if assert.ErrorAs(t, err, &failure, "未输出业务内容的失败必须交回原有账号/分组重试") {
				assert.Equal(t, test.status, failure.StatusCode)
			}
			assert.Nil(t, result, "失败前无实际输出，不应提交 0/0 用量并关闭智能重放")
			assert.False(t, c.Writer.Written(), "role-only 前导不能提前锁定失败账号的响应")
			assert.NotContains(t, recorder.Body.String(), "response.completed")
			assert.NotContains(t, recorder.Body.String(), "data: [DONE]")
			smartRoutingAssertChatStreamOps(t, c, test.status)
		})
	}
}

// 合法空完成、客户端取消和请求参数拒绝不能被瞬时上游错误重试规则吞并。
func TestSmartRoutingChatStreamTerminalAndClientErrorBoundaries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Run("finish_reason_without_done", func(t *testing.T) {
		result, err, _, recorder := smartRoutingForwardChatStream(t, smartRoutingChatRolePrelude+"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n", false)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Contains(t, recorder.Body.String(), "response.completed")
	})
	t.Run("invalid_request_is_400", func(t *testing.T) {
		result, err, c, recorder := smartRoutingForwardChatStream(t, "data: {\"error\":{\"type\":\"invalid_request_error\",\"message\":\"unknown parameter local_field\"}}\n\n", false)
		require.Error(t, err)
		var failure *UpstreamFailoverError
		require.False(t, errors.As(err, &failure))
		require.Nil(t, result)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		require.Contains(t, recorder.Body.String(), "invalid_request_error")
		smartRoutingAssertChatStreamOps(t, c, http.StatusBadRequest)
	})
	t.Run("usage_without_text_forbids_replay", func(t *testing.T) {
		usage := "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":0,\"total_tokens\":9}}\n\n"
		result, err, c, recorder := smartRoutingForwardChatStream(t, usage+smartRoutingChat503Error, false)
		require.Error(t, err)
		var failure *UpstreamFailoverError
		require.False(t, errors.As(err, &failure))
		require.NotNil(t, result)
		require.Equal(t, 9, result.Usage.InputTokens)
		require.Contains(t, recorder.Body.String(), "response.failed")
		require.NotContains(t, recorder.Body.String(), "response.completed")
		smartRoutingAssertChatStreamOps(t, c, 503)
	})
	for _, cancelErr := range []error{context.Canceled} {
		t.Run(cancelErr.Error(), func(t *testing.T) {
			result, err, c, recorder := smartRoutingForwardChatStream(t, smartRoutingChatRolePrelude, true, smartRoutingChatStreamOptions{readErr: cancelErr, cancelClient: true})
			require.ErrorIs(t, err, cancelErr)
			var failure *UpstreamFailoverError
			require.False(t, errors.As(err, &failure))
			require.Nil(t, result)
			require.Empty(t, recorder.Body.String())
			_, hasOpsError := c.Get(OpsUpstreamErrorsKey)
			require.False(t, hasOpsError, "客户端取消不能触发分组冷却")
		})
	}
}

// 账号显式错误策略和错误透传配置必须继续覆盖默认切号与错误包装规则。
func TestSmartRoutingChatStreamAccountPolicyAndPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name         string
		status       int
		allowed      int
		wantFailover bool
	}{
		{name: "custom_excludes_503", status: 503, allowed: 524},
		{name: "custom_includes_422", status: 422, allowed: 422, wantFailover: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := fmt.Sprintf("data: {\"error\":{\"status\":%d,\"type\":\"invalid_request_error\",\"message\":\"upstream local rejection\"}}\n\n", test.status)
			if test.status == 503 {
				payload = smartRoutingChat503Error
			}
			result, err, c, recorder := smartRoutingForwardChatStream(t, payload, false, smartRoutingChatStreamOptions{accountSetup: func(account *Account) {
				account.Credentials["custom_error_codes_enabled"] = true
				account.Credentials["custom_error_codes"] = []any{float64(test.allowed)}
			}})
			require.Error(t, err)
			require.Nil(t, result)
			var failure *UpstreamFailoverError
			require.Equal(t, test.wantFailover, errors.As(err, &failure))
			if test.wantFailover {
				require.Equal(t, test.status, failure.StatusCode)
				require.False(t, c.Writer.Written())
			} else {
				require.Equal(t, http.StatusInternalServerError, recorder.Code)
				require.Contains(t, recorder.Body.String(), "Upstream gateway error")
			}
			smartRoutingAssertChatStreamOps(t, c, test.status)
		})
	}
	t.Run("invalid_request_passthrough_rule", func(t *testing.T) {
		policy := &ErrorPassthroughService{}
		policy.setLocalCache([]*model.ErrorPassthroughRule{newNonFailoverPassthroughRule(400, "invalid schema", http.StatusTeapot, "本地规则改写")})
		result, err, c, recorder := smartRoutingForwardChatStream(t, "data: {\"error\":{\"type\":\"invalid_request_error\",\"message\":\"invalid schema local_field\"}}\n\n", false, smartRoutingChatStreamOptions{passthrough: policy})
		require.Error(t, err)
		require.Nil(t, result)
		require.Equal(t, http.StatusTeapot, recorder.Code)
		require.Contains(t, recorder.Body.String(), "本地规则改写")
		smartRoutingAssertChatStreamOps(t, c, 400)
	})
}

// 已有业务输出不能重放，但错误必须保留在流和 Ops 中，已观测用量仍交给原有补记逻辑。
func TestSmartRoutingChatStreamFailureAfterOutputIsVisibleAndNotReplayable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	usage := "data: {\"id\":\"chatcmpl-local\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-6-astra\",\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":2,\"total_tokens\":13}}\n\n"
	for _, test := range []struct {
		name      string
		tail      string
		readError bool
		status    int
	}{
		{name: "error_then_done", tail: smartRoutingChat503Error + "data: [DONE]\n\n", status: 503},
		{name: "unexpected_eof", readError: true, status: 502},
		{name: "missing_terminal_eof", status: 502},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err, c, recorder := smartRoutingForwardChatStream(t, smartRoutingChatRolePrelude+smartRoutingChatContent+usage+test.tail, test.readError)
			assert.Error(t, err)
			var failure *UpstreamFailoverError
			assert.False(t, errors.As(err, &failure), "已有文本不能再切账号或分组")
			if assert.NotNil(t, result) {
				assert.Equal(t, 11, result.Usage.InputTokens)
				assert.Equal(t, 2, result.Usage.OutputTokens)
			}
			assert.Contains(t, recorder.Body.String(), "partial-output")
			assert.Contains(t, recorder.Body.String(), "response.failed", "客户端必须看到错误终态，不能只看到连接结束")
			assert.NotContains(t, recorder.Body.String(), "response.completed")
			smartRoutingAssertChatStreamOps(t, c, test.status)
		})
	}
}

// smartRoutingAssertChatStreamOps 验证真实错误事实，避免客户端 HTTP 200 掩盖错误看板。
func smartRoutingAssertChatStreamOps(t *testing.T, c *gin.Context, status int) {
	t.Helper()
	assert.Equal(t, status, c.GetInt(OpsUpstreamStatusCodeKey))
	value, ok := c.Get(OpsUpstreamErrorsKey)
	if assert.True(t, ok, "流内错误必须进入 Ops 事件") {
		events, valid := value.([]*OpsUpstreamErrorEvent)
		if assert.True(t, valid) && assert.NotEmpty(t, events) {
			assert.Equal(t, status, events[len(events)-1].UpstreamStatusCode)
			assert.Equal(t, int64(101), events[len(events)-1].AccountID)
		}
	}
}
