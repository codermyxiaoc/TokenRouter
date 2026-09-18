package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 预检入口也必须区分实际 HTTP 失败与本地拒绝，且错误策略改写不能丢失上游证据。
func TestSmartRoutingTokenHTTPErrorEvidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"count_tokens", "count_tokens_passthrough", "input_tokens", "embeddings"} {
		for _, status := range []int{400, 401, 403, 404, 408, 422} {
			for _, customPolicy := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/custom_%t", endpoint, status, customPolicy), func(t *testing.T) {
					ctx, _ := WithSmartRoutingAttempt(context.Background())
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil).WithContext(ctx)
					errorBody := `{"error":{"message":"upstream rejected request","type":"invalid_request_error"}}`
					if status == http.StatusNotFound {
						errorBody = `{"error":{"message":"Not found: /v1/messages/count_tokens","type":"not_found_error"}}`
					}
					response := &http.Response{
						StatusCode: status, Header: http.Header{"X-Request-Id": []string{"token-failure"}},
						Body: io.NopCloser(strings.NewReader(errorBody)),
					}
					account := newAnthropicAPIKeyAccountForTest()
					account.Extra["anthropic_passthrough"] = endpoint == "count_tokens_passthrough"
					if customPolicy {
						account.Credentials["custom_error_codes_enabled"] = true
						account.Credentials["custom_error_codes"] = []any{float64(429)}
					}
					cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
					if endpoint == "embeddings" {
						account.Platform = PlatformOpenAI
						account.Credentials["base_url"] = "https://api.openai.com"
						svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &anthropicHTTPUpstreamRecorder{resp: response}}
						_, err := svc.ForwardEmbeddings(ctx, c, account, []byte(`{"model":"text-embedding-3-small","input":"hello"}`), "")
						require.Error(t, err)
					} else if endpoint == "input_tokens" {
						account.Platform = PlatformOpenAI
						svc := &OpenAIGatewayService{cfg: cfg}
						body, err := io.ReadAll(response.Body)
						require.NoError(t, err)
						require.Error(t, svc.handleResponsesInputTokensUpstreamError(ctx, c, account, &openAIInputTokensCountPrepared{UpstreamModel: "gpt-5.4"}, response, body))
					} else {
						svc := &GatewayService{cfg: cfg, httpUpstream: &anthropicHTTPUpstreamRecorder{resp: response}, settingService: NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{}}, cfg)}
						_ = svc.ForwardCountTokens(ctx, c, account, &ParsedRequest{Model: "claude-sonnet-4-5", Body: NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`))})
					}
					value, exists := c.Get(OpsUpstreamErrorsKey)
					require.True(t, exists)
					events, ok := value.([]*OpsUpstreamErrorEvent)
					require.True(t, ok)
					require.Len(t, events, 1)
					require.Equal(t, status, events[0].UpstreamStatusCode)
					require.Contains(t, []string{"http_error", "failover"}, events[0].Kind)
					require.Equal(t, account.ID, events[0].AccountID)
					require.Equal(t, "token-failure", events[0].UpstreamRequestID)
				})
			}
		}
	}
}

// 真实网络失败留有 request_error；本地参数拒绝不能借用同样的失败标记。
func TestSmartRoutingTokenNetworkAndLocalErrorEvidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"count_tokens", "input_tokens"} {
		for _, local := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/local_%t", endpoint, local), func(t *testing.T) {
				ctx, _ := WithSmartRoutingAttempt(context.Background())
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil).WithContext(ctx)
				upstream := &anthropicHTTPUpstreamRecorder{err: errors.New("connection reset")}
				cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
				account := newAnthropicAPIKeyAccountForTest()
				account.Extra["anthropic_passthrough"] = false
				if endpoint == "input_tokens" {
					account.Platform = PlatformOpenAI
					account.Credentials["base_url"] = "https://api.openai.com"
					body := []byte(`{"model":"gpt-5.4","input":"hello"}`)
					if local {
						body = []byte(`{`)
					}
					svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
					require.Error(t, svc.ForwardResponsesInputTokens(ctx, c, account, body))
				} else {
					parsed := &ParsedRequest{Model: "claude-sonnet-4-5", Body: NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`))}
					if local {
						parsed = nil
					}
					svc := &GatewayService{cfg: cfg, httpUpstream: upstream}
					require.Error(t, svc.ForwardCountTokens(ctx, c, account, parsed))
				}
				value, exists := c.Get(OpsUpstreamErrorsKey)
				if local {
					require.False(t, exists)
					require.Nil(t, upstream.lastReq)
					return
				}
				require.True(t, exists)
				events := value.([]*OpsUpstreamErrorEvent)
				require.Len(t, events, 1)
				require.Equal(t, "request_error", events[0].Kind)
				require.Zero(t, events[0].UpstreamStatusCode)
				require.NotNil(t, upstream.lastReq)
			})
		}
	}
}

// tokenHTTPErrorBodyReader 模拟响应头已收到、错误正文尚未到达即断开的上游连接。
type tokenHTTPErrorBodyReader struct{}

func (tokenHTTPErrorBodyReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// HTTP 错误体为空或读取中断时仍保留实际状态，且智能标记不改变 service 的对客响应和账号重试。
func TestSmartRoutingTokenHTTPErrorBodyFailureEvidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"count_tokens", "count_tokens_passthrough", "input_tokens", "embeddings"} {
		for _, status := range []int{400, 403, 404, 408, 422} {
			for _, bodyFails := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/read_failure_%t", endpoint, status, bodyFails), func(t *testing.T) {
					type outcome struct {
						status   int
						body     string
						written  bool
						failover bool
					}
					var normal outcome
					for _, smart := range []bool{false, true} {
						ctx := context.Background()
						if smart {
							ctx, _ = WithSmartRoutingAttempt(ctx)
						}
						recorder := httptest.NewRecorder()
						c, _ := gin.CreateTestContext(recorder)
						c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+endpoint, nil).WithContext(ctx)
						var errorReader io.Reader = strings.NewReader("")
						if bodyFails {
							errorReader = tokenHTTPErrorBodyReader{}
						}
						upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
							StatusCode: status, Header: http.Header{"X-Request-Id": []string{"unread-error-body"}},
							Body: io.NopCloser(errorReader),
						}}
						account := newAnthropicAPIKeyAccountForTest()
						account.Extra["anthropic_passthrough"] = endpoint == "count_tokens_passthrough"
						cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
						var err error
						if endpoint == "input_tokens" || endpoint == "embeddings" {
							account.Platform = PlatformOpenAI
							account.Credentials["base_url"] = "https://api.openai.com"
							svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
							if endpoint == "input_tokens" {
								err = svc.ForwardResponsesInputTokens(ctx, c, account, []byte(`{"model":"gpt-5.4","input":"hello"}`))
							} else {
								_, err = svc.ForwardEmbeddings(ctx, c, account, []byte(`{"model":"text-embedding-3-small","input":"hello"}`), "")
							}
						} else {
							svc := &GatewayService{cfg: cfg, httpUpstream: upstream, settingService: NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{}}, cfg)}
							err = svc.ForwardCountTokens(ctx, c, account, &ParsedRequest{Model: "claude-sonnet-4-5", Body: NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`))})
						}
						require.Error(t, err)
						value, exists := c.Get(OpsUpstreamErrorsKey)
						require.True(t, exists)
						events := value.([]*OpsUpstreamErrorEvent)
						require.Len(t, events, 1)
						require.Equal(t, status, events[0].UpstreamStatusCode)
						require.Equal(t, "unread-error-body", events[0].UpstreamRequestID)
						require.Equal(t, account.ID, events[0].AccountID)
						var failover *UpstreamFailoverError
						got := outcome{status: recorder.Code, body: recorder.Body.String(), written: c.Writer.Written(), failover: errors.As(err, &failover)}
						if endpoint == "input_tokens" || bodyFails && endpoint != "embeddings" {
							require.Equal(t, http.StatusBadGateway, got.status)
							require.Contains(t, got.body, "Failed to read response")
							require.False(t, got.failover)
						}
						if smart {
							require.Equal(t, normal, got)
						} else {
							normal = got
						}
					}
				})
			}
		}
	}
}

// 签名修复后的错误体中断必须归属第二次响应，不能用最初的 400 覆盖真实 422。
func TestSmartRoutingCountTokensSignatureRetryErrorBodyEvidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := WithSmartRoutingAttempt(context.Background())
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil).WithContext(ctx)
	upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{
		{StatusCode: http.StatusBadRequest, Header: http.Header{"X-Request-Id": []string{"first-400"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"signature read retry"}}`))},
		{StatusCode: http.StatusUnprocessableEntity, Header: http.Header{"X-Request-Id": []string{"second-422"}}, Body: io.NopCloser(tokenHTTPErrorBodyReader{})},
	}}
	account := newAnthropicAPIKeyAccountForTest()
	account.Extra["anthropic_passthrough"] = false
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	svc := &GatewayService{cfg: cfg, httpUpstream: upstream, settingService: NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyRectifierSettings: `{"enabled":true,"apikey_signature_enabled":true,"apikey_signature_patterns":["signature read retry"]}`,
	}}, cfg)}
	err := svc.ForwardCountTokens(ctx, c, account, &ParsedRequest{Model: "claude-sonnet-4-5", Body: NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`))})
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Equal(t, 2, upstream.callCount)
	require.Equal(t, http.StatusBadGateway, recorder.Code)
	require.Contains(t, recorder.Body.String(), "Failed to read response")
	require.Equal(t, http.StatusUnprocessableEntity, c.GetInt(OpsUpstreamStatusCodeKey))
	value, exists := c.Get(OpsUpstreamErrorsKey)
	require.True(t, exists)
	events := value.([]*OpsUpstreamErrorEvent)
	require.Len(t, events, 1)
	require.Equal(t, http.StatusUnprocessableEntity, events[0].UpstreamStatusCode)
	require.Equal(t, "second-422", events[0].UpstreamRequestID)
	require.Equal(t, "http_error", events[0].Kind)
}
