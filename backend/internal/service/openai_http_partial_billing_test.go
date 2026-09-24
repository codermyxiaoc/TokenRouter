package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const httpPartialUsageProgress = `{"type":"response.in_progress","response":{"id":"resp_partial","service_tier":"flex","usage":{"input_tokens":11,"output_tokens":7,"input_tokens_details":{"cached_tokens":3}}}}`
const httpPartialUsageFailed = `{"type":"response.failed","response":{"id":"resp_partial","model":"gpt-6-astra","status":"failed","service_tier":"flex","error":{"code":"server_error","message":"upstream interrupted"},"usage":{"input_tokens":11,"output_tokens":7,"input_tokens_details":{"cached_tokens":3}}}}`
const httpPartialNamespaceItem = `{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_partial","type":"function_call","call_id":"call_partial","name":"execute","namespace":"functions","arguments":"{}"}}`

// 使用假上游完整经过 Forward，验证流解析保留的部分结果不会在外层再次被丢弃。
func TestOpenAIHTTPPartialBillingForward(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		for _, tc := range []struct {
			name         string
			events       []string
			readErr      error
			hasNamespace bool
		}{
			{name: "usage_then_eof", events: []string{httpPartialUsageProgress}},
			{name: "usage_then_read_error", events: []string{httpPartialUsageProgress}, readErr: io.ErrUnexpectedEOF},
			{name: "usage_then_canceled_read", events: []string{httpPartialUsageProgress}, readErr: context.Canceled},
			{name: "failed_with_usage", events: []string{httpPartialUsageFailed}},
			{name: "bare_error_with_usage", events: []string{`{"type":"error","usage":{"input_tokens":11,"output_tokens":7,"input_tokens_details":{"cached_tokens":3}},"error":{"code":"server_error","message":"upstream interrupted"}}`}},
			{name: "namespace_then_read_error", events: []string{httpPartialNamespaceItem, httpPartialUsageProgress}, readErr: io.ErrUnexpectedEOF, hasNamespace: true},
		} {
			t.Run(map[bool]string{false: "native", true: "passthrough"}[passthrough]+"/"+tc.name, func(t *testing.T) {
				stream := busyTestSSE(tc.events...)
				resp := busyTestResponse(stream)
				if tc.readErr != nil {
					resp.Body = &openAIStreamReadThenErrorCloser{reader: strings.NewReader(stream), err: tc.readErr}
				}
				result, err, rec, upstream := runHTTPPartialBillingForward(t, passthrough, resp)
				require.Error(t, err)
				var failover *UpstreamFailoverError
				require.False(t, errors.As(err, &failover), "已计费用量不得变成可重放失败")
				require.NotNil(t, result, "部分用量必须交给原 handler 结算")
				require.Equal(t, 11, result.Usage.InputTokens)
				require.Equal(t, 7, result.Usage.OutputTokens)
				require.Equal(t, 3, result.Usage.CacheReadInputTokens)
				require.Equal(t, "gpt-6-astra", result.Model)
				require.Equal(t, "upstream-busy-test", result.RequestID)
				require.Equal(t, "upstream-busy-test", result.UpstreamHeaders.Get("X-Request-Id"))
				require.True(t, result.Stream)
				require.GreaterOrEqual(t, result.Duration, time.Duration(0))
				require.NotNil(t, result.ServiceTier)
				require.Equal(t, "priority", *result.ServiceTier)
				require.NotNil(t, result.ReasoningEffort)
				require.Equal(t, "low", *result.ReasoningEffort)
				require.Len(t, upstream.requests, 1, "已有用量后不能进行 compact 或账号重试")
				if tc.name != "bare_error_with_usage" {
					require.Equal(t, "resp_partial", result.ResponseID)
				}
				if tc.name == "failed_with_usage" {
					require.Equal(t, "flex", result.UpstreamResponseServiceTier)
				} else {
					require.Empty(t, result.UpstreamResponseServiceTier, "前导档位回显不能代替可信终态")
				}
				if tc.hasNamespace {
					found := false
					for _, line := range strings.Split(rec.Body.String(), "\n") {
						if !strings.HasPrefix(line, "data: ") {
							continue
						}
						event := gjson.Parse(strings.TrimPrefix(line, "data: "))
						if event.Get("type").String() == "response.output_item.done" {
							found = true
							require.Equal(t, "functions", event.Get("item.namespace").String())
							require.Equal(t, "call_partial", event.Get("item.call_id").String())
							require.Equal(t, "{}", event.Get("item.arguments").String())
						}
					}
					require.True(t, found, "工具事件与 namespace 不能在失败收尾时丢失")
				}
			})
		}
	}
}

func runHTTPPartialBillingForward(t *testing.T, passthrough bool, resp *http.Response, imageRequest ...bool) (*OpenAIForwardResult, error, *httptest.ResponseRecorder, *httpUpstreamRecorder) {
	t.Helper()
	body := []byte(`{"model":"gpt-6-astra","stream":true,"instructions":"test","input":"hello","reasoning":{"effort":"low"},"service_tier":"priority","tools":[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"execute","description":"test","parameters":{"type":"object","properties":{}}}]}]}`)
	if len(imageRequest) > 0 && imageRequest[0] {
		body = []byte(`{"model":"gpt-6-astra","stream":true,"input":"draw","tools":[{"type":"image_generation","model":"gpt-image-2","size":"1024x1024"}]}`)
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	upstream := &httpUpstreamRecorder{resp: resp}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, toolCorrector: NewCodexToolCorrector()}
	account := &Account{ID: 8842, Name: "partial-billing", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "test-token", "base_url": "https://api.example.test"},
		Extra:       map[string]any{"openai_passthrough": passthrough, "openai_responses_supported": true}, Status: StatusActive, Schedulable: true}
	result, err := svc.Forward(context.Background(), c, account, body)
	return result, err, rec, upstream
}

// 完整图片已返回时也应保留媒体计费信息，即使上游不提供 token usage。
func TestOpenAIHTTPPartialBillingImageWithoutUsage(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "passthrough"}[passthrough], func(t *testing.T) {
			stream := busyTestSSE(`{"type":"response.output_item.done","item":{"id":"img_partial","type":"image_generation_call","status":"completed","result":"aGVsbG8=","size":"1024x1024"}}`)
			resp := busyTestResponse(stream)
			resp.Body = &openAIStreamReadThenErrorCloser{reader: strings.NewReader(stream), err: io.ErrUnexpectedEOF}
			result, err, _, upstream := runHTTPPartialBillingForward(t, passthrough, resp, true)
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover))
			require.NotNil(t, result)
			require.Equal(t, 1, result.ImageCount)
			require.NotEmpty(t, result.BillingModel)
			require.Equal(t, []string{"1024x1024"}, result.ImageOutputSizes)
			require.Len(t, upstream.requests, 1)
		})
	}
}

// 没有业务输出且没有上游消耗时，原有故障转移仍然可用。
func TestOpenAIHTTPPartialBillingEmptyAttemptStillFailsOver(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, readErr := range []error{nil, io.ErrUnexpectedEOF} {
			t.Run(map[bool]string{false: "native", true: "passthrough"}[passthrough]+"/"+map[bool]string{false: "eof", true: "read_error"}[readErr != nil], func(t *testing.T) {
				stream := busyTestSSE(`{"type":"response.in_progress","response":{"id":"resp_partial","usage":{"input_tokens":0,"output_tokens":0}}}`)
				resp := busyTestResponse(stream)
				if readErr != nil {
					resp.Body = &openAIStreamReadThenErrorCloser{reader: strings.NewReader(stream), err: readErr}
				}
				result, err, rec, upstream := runHTTPPartialBillingForward(t, passthrough, resp)
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.Nil(t, result)
				require.Empty(t, rec.Body.String())
				require.Len(t, upstream.requests, 1)
			})
		}
	}
}

// 首输出定时器不因纯用量前导解除；到期应失败并保留账务数据，不能重放。
func TestOpenAIHTTPPartialBillingFirstOutputTimeout(t *testing.T) {
	c, rec, svc, account := busyTestContext()
	svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
	body := newOpenAICompatBlockingReadCloser([]byte(busyTestSSE(httpPartialUsageProgress)))
	defer body.Close()
	resp := busyTestResponse("")
	resp.Body = body
	result, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now().Add(-750*time.Millisecond), "gpt-6-astra", "gpt-6-astra")
	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.False(t, errors.As(err, &failover))
	require.NotNil(t, result)
	require.Equal(t, 11, result.usage.InputTokens)
	require.Equal(t, 7, result.usage.OutputTokens)
	require.Contains(t, rec.Body.String(), "stream_timeout")
	require.Nil(t, result.firstTokenMs, "只有用量不能伪造 TTFT")
	select {
	case <-body.closed:
	default:
		t.Fatal("超时必须关闭上游流")
	}
}

// 触发扫描器限制时保留先前用量，避免超长前导成为重复计费入口。
func TestOpenAIHTTPPartialBillingScannerLimit(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "passthrough"}[passthrough], func(t *testing.T) {
			c, _, svc, account := busyTestContext()
			svc.cfg.Gateway.MaxLineSize = 64 * 1024
			stream := busyTestSSE(httpPartialUsageProgress) + "data: " + strings.Repeat("x", 128*1024)
			usage, err := runBusyTestStream(svc, c, account, passthrough, busyTestResponse(stream))
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover))
			require.Equal(t, 11, usage.InputTokens)
		})
	}
}
