package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 新平台的能力独立，不能借新平台扩展打开 TypeSafe 文本或改变 Video 调度。
func TestAdditionalProviderCredentialAndProtocolIsolation(t *testing.T) {
	for _, platform := range []string{PlatformTypeSafe, PlatformCline, PlatformCommandCode} {
		require.Equal(t, platform, NormalizeOpenAICompatiblePlatform(platform), "分组不能被错误归入 OpenAI 账号池")
		account := &Account{Platform: platform, Type: AccountTypeOAuth}
		require.Error(t, normalizeAdditionalProviderCredentials(account, true))
		account.Type = AccountTypeAPIKey
		require.NoError(t, normalizeAdditionalProviderCredentials(account, true))
		require.Equal(t, AccountModePayG, account.GetAccountMode())
		require.NotEmpty(t, account.GetOpenAIBaseURL())
	}
	a := &Account{Platform: PlatformTypeSafe, Type: AccountTypeAPIKey}
	require.True(t, a.IsModelSupported(DefaultTypeSafeModel))
	require.False(t, a.IsModelSupported("gpt-6-sol"))
	require.True(t, supportsAccountTestEndpoint(a, APIProtocolSystemOne))
	require.False(t, supportsAccountTestEndpoint(a, APIProtocolResponses))
	require.True(t, smartRoutingGroupEndpointEligible(PlatformTypeSafe, "/v1/systemone"))
	for _, endpoint := range []string{"/v1/messages", "/v1/responses", "/v1/chat/completions", "/v1/videos", "/v1/images/generations"} {
		require.False(t, smartRoutingGroupEndpointEligible(PlatformTypeSafe, endpoint))
	}
	for _, platform := range []string{PlatformCline, PlatformCommandCode} {
		require.True(t, smartRoutingGroupEndpointEligible(platform, "/v1/responses/input_tokens"))
		require.False(t, smartRoutingGroupEndpointEligible(platform, "/v1/systemone"))
		require.False(t, smartRoutingGroupEndpointEligible(platform, "/v1/videos"))
	}
	a = &Account{Platform: PlatformCommandCode, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay.example/prefix/v1", "api_protocol": "adaptive"}}
	s := &OpenAIGatewayService{}
	require.Equal(t, "https://relay.example/prefix", a.GetAnthropicProtocolBaseURL())
	require.Equal(t, APIProtocolAnthropic, s.modelRoutedUpstreamProtocol(context.Background(), a, APIProtocolResponses, "anthropic/claude-sonnet-4-6"))
	a.Credentials["api_protocol"] = APIProtocolChatCompletions
	require.Equal(t, APIProtocolChatCompletions, s.modelRoutedUpstreamProtocol(context.Background(), a, APIProtocolResponses, "anthropic/claude-sonnet-4-6"))
	a.Credentials["api_protocol"] = APIProtocolAdaptive
	a.Credentials["protocol_rules"] = []any{map[string]any{"pattern": "anthropic/*", "protocol": APIProtocolResponses}}
	require.Equal(t, APIProtocolResponses, s.modelRoutedUpstreamProtocol(context.Background(), a, APIProtocolChatCompletions, "anthropic/claude-sonnet-4-6"))
}

func TestSystemOneAuditIncludesNestedQuestionsAndState(t *testing.T) {
	input := ExtractContentModerationInput(ContentModerationProtocolSystemOne, []byte(`{"model":"jev-latest","state":{"danger_state":["nested_state"]},"questions":{"danger_question":{"type":"choice","criteria":{"choice_one":"nested_instruction"}}}}`))
	for _, value := range []string{"danger_state", "nested_state", "danger_question", "choice_one", "nested_instruction"} {
		require.Contains(t, input.Text, value)
	}
}

func TestTypeSafeGatewayPreservesBodyAndSafeContentType(t *testing.T) {
	body := []byte(`{"model":"jev-latest","state":["x"],"questions":{"q":{"type":"noul"}}}`)
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(bytes.NewBufferString(`{"answers":{"q":{"noul":0}},"usage":{"input_tokens":"15","output_tokens":0}}`))}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	a := &Account{ID: 9, Platform: PlatformTypeSafe, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "secret", "base_url": "https://relay.example/v1"}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/systemone", bytes.NewReader(body))
	result, err := svc.ForwardOpenCodeSystemOne(context.Background(), c, a, body)
	require.NoError(t, err)
	require.Equal(t, 15, result.Usage.InputTokens)
	require.Contains(t, w.Header().Get("Content-Type"), "application/json")
	require.Equal(t, "https://relay.example/v1/systemone", upstream.lastReq.URL.String())
	require.Equal(t, body, upstream.lastBody)
}

func TestCommandCodeCatalogConcurrentRefreshAndIsolation(t *testing.T) {
	var catalog modelProtocolCatalog
	var calls atomic.Int32
	now := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := catalog.lookup(context.Background(), "account1", "m", now, func() {
				calls.Add(1)
				time.Sleep(10 * time.Millisecond)
				catalog.store("account1", map[string][]string{"m": {APIProtocolAnthropic}}, nil, time.Now())
			})
			require.Equal(t, []string{APIProtocolAnthropic}, got)
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), calls.Load())
	require.Nil(t, catalog.lookup(context.Background(), "account2", "m", now, nil))
	a, b := &Account{ID: 1}, &Account{ID: 2}
	require.NotEqual(t, modelProtocolCatalogKey(a, "https://same.example"), modelProtocolCatalogKey(b, "https://same.example"))
	old := modelProtocolCatalogKey(a, "https://same.example")
	a.Credentials = map[string]any{"api_key": "other-tenant-secret"}
	updated := modelProtocolCatalogKey(a, "https://same.example")
	require.NotEqual(t, old, updated)
	require.NotContains(t, updated, "other-tenant-secret")
	_, err := parseModelProtocolCatalog([]byte(`{"data":[{"id":"m"}]}`))
	require.Error(t, err)
	models, err := parseModelProtocolCatalog([]byte(`{"data":[{"id":"M","supported_endpoints":["/v1/messages","/responses","/responses"]}]}`))
	require.NoError(t, err)
	require.Equal(t, []string{APIProtocolAnthropic, APIProtocolResponses}, models["m"])
}

func TestSystemOneUsageRequiresExplicitValidCounters(t *testing.T) {
	for _, body := range []string{`{"usage":{}}`, `{"usage":{"input_tokens":"bad","output_tokens":0}}`, `{"usage":{"input_tokens":-1,"output_tokens":0}}`, `{"usage":{"input_tokens":1.5,"output_tokens":0}}`, `{"usage":{"input_tokens":"NaN","output_tokens":0}}`, `{"usage":{"input_tokens":0,"output_tokens":null}}`} {
		_, ok := extractSystemOneUsage([]byte(body))
		require.False(t, ok, body)
	}
	for _, body := range []string{`{"usage":{"input_tokens":0,"output_tokens":0}}`, `{"usage":{"input_tokens":"42","output_tokens":"1"}}`} {
		_, ok := extractSystemOneUsage([]byte(body))
		require.True(t, ok, body)
	}
}
