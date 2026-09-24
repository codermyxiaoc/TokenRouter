package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpus55PreservesDefaultThinkingHistory(t *testing.T) {
	// 默认思考开启时，空可见文字仍可能带有下轮工具执行所需的合法签名。
	body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"valid-signature"},{"type":"thinking","thinking":"invalid"},{"type":"text","text":"hi"}]}]}`)
	got := FilterThinkingBlocks(body)
	require.Equal(t, "valid-signature", gjson.GetBytes(got, "messages.0.content.0.signature").String())
	require.Len(t, gjson.GetBytes(got, "messages.0.content").Array(), 2)
	require.False(t, gjson.GetBytes(got, "thinking").Exists())
	// 映射到旧模型仍执行原有语义；映射到新模型则按新模型保留。
	old := FilterThinkingBlocks(body, "claude-opus-5")
	require.Len(t, gjson.GetBytes(old, "messages.0.content").Array(), 1)
	alias := []byte(strings.Replace(string(body), "claude-opus-5-5", "public-alias", 1))
	require.JSONEq(t, strings.Replace(string(got), "claude-opus-5-5", "public-alias", 1), string(FilterThinkingBlocks(alias, "claude-opus-5-5")))
	redacted := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"encrypted-history"},{"type":"text","text":"hi"}]}]}`)
	require.Equal(t, string(redacted), string(FilterThinkingBlocks(redacted)))
}

func TestOpus55CompatibilityForwardersUseMappedThinking(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
		for _, protocol := range []string{"responses", "chat/completions"} {
			for _, model := range []string{"claude-opus-5-5", "claude-sonnet-4-5"} {
				t.Run(accountType+"/"+protocol+"/"+model, func(t *testing.T) {
					body := []byte(`{"model":"public-alias","input":"hello","reasoning":{"effort":"high"}}`)
					if protocol == "chat/completions" {
						body = []byte(`{"model":"public-alias","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"high"}`)
					}
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+protocol, nil)
					upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"test capture"}}`))}}
					cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
					svc := &GatewayService{cfg: cfg, responseHeaderFilter: compileResponseHeaderFilter(cfg), httpUpstream: upstream}
					account := &Account{ID: 502, Platform: PlatformAnthropic, Type: accountType, Concurrency: 1, Credentials: map[string]any{"access_token": "test-token", "api_key": "test-key", "model_mapping": map[string]any{"public-alias": model}}, Status: StatusActive, Schedulable: true}
					var err error
					if protocol == "responses" {
						_, err = svc.ForwardAsResponses(context.Background(), c, account, body, nil)
					} else {
						_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, nil)
					}
					require.Error(t, err)
					require.NotEmpty(t, upstream.lastBody, fmt.Sprintf("应真正到达模拟上游：%v", err))
					if model == "claude-opus-5-5" {
						require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
						require.Equal(t, "adaptive", gjson.GetBytes(upstream.lastBody, "thinking.type").String())
						require.False(t, gjson.GetBytes(upstream.lastBody, "thinking.budget_tokens").Exists())
					} else {
						require.Equal(t, "enabled", gjson.GetBytes(upstream.lastBody, "thinking.type").String())
						require.True(t, gjson.GetBytes(upstream.lastBody, "thinking.budget_tokens").Exists())
					}
				})
			}
		}
	}
}
