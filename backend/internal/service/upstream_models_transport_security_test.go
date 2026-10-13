package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// upstreamModelSyncSecurityAccounts 为不同协议提供独立账号，避免测试共享可变凭据。
func upstreamModelSyncSecurityAccounts() map[string]*Account {
	accounts := make(map[string]*Account)
	for _, platform := range []string{
		PlatformOpenAI, PlatformAnthropic, PlatformGemini, PlatformGrok, PlatformAntigravity,
		PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax,
		PlatformOpenCodeGo, PlatformCline, PlatformCommandCode,
	} {
		baseURL := "https://relay.example"
		if platform == PlatformAntigravity {
			baseURL += "/antigravity"
		}
		accounts[platform] = &Account{
			ID: 173, Platform: platform, Type: AccountTypeAPIKey, Concurrency: 3,
			Credentials: map[string]any{"api_key": "model-sync-test-key", "base_url": baseURL},
		}
	}
	accounts["vertex"] = vertexModelSyncTestAccount(nil)
	return accounts
}

// 显式代理不完整时必须在任何令牌刷新或列表网络访问之前失败。
func TestUpstreamModelSyncRejectsUnavailableConfiguredProxyBeforeAnyRequest(t *testing.T) {
	t.Parallel()
	accounts := upstreamModelSyncSecurityAccounts()
	accounts["antigravity_oauth"] = &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	accounts["gemini_oauth"] = &Account{Platform: PlatformGemini, Type: AccountTypeOAuth}
	for name, account := range accounts {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, invalid := range []string{"missing", "identity_mismatch", "disabled", "expired"} {
				t.Run(invalid, func(t *testing.T) {
					proxyID := int64(12)
					candidate := *account
					candidate.ProxyID = &proxyID
					candidate.Proxy = &Proxy{ID: proxyID, Status: StatusActive, Protocol: "http", Host: "proxy.example", Port: 8080}
					switch invalid {
					case "missing":
						candidate.Proxy = nil
					case "identity_mismatch":
						candidate.Proxy.ID++
					case "disabled":
						candidate.Proxy.Status = "disabled"
					case "expired":
						expired := time.Now().Add(-time.Minute)
						candidate.Proxy.ExpiresAt = &expired
					}
					upstream := &geminiModelSyncHTTPUpstream{t: t}
					tokenCache := &vertexModelsTokenCacheStub{token: "ya29.vertex-token"}
					svc := &AccountTestService{
						httpUpstream: upstream, cfg: upstreamModelSyncTestConfig(),
						geminiTokenProvider: NewGeminiTokenProvider(nil, tokenCache, nil),
					}

					models, err := svc.FetchUpstreamSupportedModels(context.Background(), &candidate)
					var syncErr *UpstreamModelSyncError
					require.ErrorAs(t, err, &syncErr)
					require.Equal(t, UpstreamModelSyncErrorConfiguration, syncErr.Kind)
					require.Equal(t, "Configured proxy is unavailable", syncErr.SafeMessage())
					require.Empty(t, models)
					require.Empty(t, upstream.calls)
					require.Zero(t, tokenCache.calls)
				})
			}
		})
	}
}

// 各平台共享同一安全传输；有效代理、取消信号、TLS 及认证仍保持原配置。
func TestUpstreamModelSyncPreservesValidProxyAndDisablesRedirects(t *testing.T) {
	t.Parallel()
	for name, account := range upstreamModelSyncSecurityAccounts() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			proxyID := int64(12)
			account.ProxyID = &proxyID
			account.Proxy = &Proxy{ID: proxyID, Status: StatusActive, Protocol: "http", Host: "proxy.example", Port: 8080, Username: "user", Password: "pass"}
			body := `{"data":[{"id":"model-a"}]}`
			wantModels := []string{"model-a"}
			if name == "vertex" {
				body = `{"publisherModels":[{"name":"publishers/google/models/gemini-3.8-flash"}]}`
				wantModels = []string{"gemini-3.8-flash"}
			}
			response, trackedBody := geminiModelSyncResponse(http.StatusOK, "application/json", body)
			upstream := &geminiModelSyncHTTPUpstream{t: t, results: []geminiModelSyncResult{{response: response}}}
			svc := &AccountTestService{
				httpUpstream: upstream, cfg: upstreamModelSyncTestConfig(),
				geminiTokenProvider: NewGeminiTokenProvider(nil, &vertexModelsTokenCacheStub{token: "ya29.vertex-token"}, nil),
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			models, err := svc.FetchUpstreamSupportedModels(ctx, account)
			require.NoError(t, err)
			require.Equal(t, wantModels, models)
			require.Len(t, upstream.calls, 1)
			call := upstream.calls[0]
			require.Equal(t, "http://user:pass@proxy.example:8080", call.proxyURL)
			require.Equal(t, account.ID, call.accountID)
			require.Equal(t, account.Concurrency, call.concurrency)
			require.True(t, call.withTLS)
			require.True(t, HTTPUpstreamRedirectsDisabled(call.request.Context()))
			require.Equal(t, 1, trackedBody.closeCount)
			cancel()
			require.ErrorIs(t, call.request.Context().Err(), context.Canceled)
		})
	}
}

// 重定向既不能泄露密钥，也不能被误判为 Gemini 原生列表缺失而再次请求。
func TestUpstreamModelSyncRedirectResponseDoesNotFallback(t *testing.T) {
	t.Parallel()
	response, trackedBody := geminiModelSyncResponse(http.StatusFound, "text/html", "redirect-secret")
	response.Header.Set("Location", "https://other.example/collect")
	upstream := &geminiModelSyncHTTPUpstream{t: t, results: []geminiModelSyncResult{{response: response}}}
	svc := &AccountTestService{httpUpstream: upstream, cfg: upstreamModelSyncTestConfig()}

	models, err := svc.FetchUpstreamSupportedModels(context.Background(), geminiModelSyncAccount("https://relay.example"))
	var syncErr *UpstreamModelSyncError
	require.ErrorAs(t, err, &syncErr)
	require.Equal(t, http.StatusFound, syncErr.StatusCode)
	require.False(t, syncErr.allowGeminiListFallback)
	require.NotContains(t, syncErr.SafeMessage(), "redirect-secret")
	require.Empty(t, models)
	require.Len(t, upstream.calls, 1)
	require.True(t, HTTPUpstreamRedirectsDisabled(upstream.calls[0].request.Context()))
	require.Equal(t, 1, trackedBody.closeCount)
}
