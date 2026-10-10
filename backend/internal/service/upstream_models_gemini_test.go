package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

// geminiModelSyncHTTPUpstream 记录每次传输，避免依赖仅在 unit 标签下存在的测试替身。
type geminiModelSyncHTTPUpstream struct {
	t       *testing.T
	results []geminiModelSyncResult
	calls   []geminiModelSyncCall
	onCall  func(int)
}

type geminiModelSyncResult struct {
	response *http.Response
	err      error
}

type geminiModelSyncCall struct {
	request     *http.Request
	proxyURL    string
	accountID   int64
	concurrency int
	profile     *tlsfingerprint.Profile
	withTLS     bool
}

func (u *geminiModelSyncHTTPUpstream) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	return u.record(geminiModelSyncCall{request: req, proxyURL: proxyURL, accountID: accountID, concurrency: concurrency})
}

func (u *geminiModelSyncHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	return u.record(geminiModelSyncCall{request: req, proxyURL: proxyURL, accountID: accountID, concurrency: concurrency, profile: profile, withTLS: true})
}

func (u *geminiModelSyncHTTPUpstream) record(call geminiModelSyncCall) (*http.Response, error) {
	u.t.Helper()
	index := len(u.calls)
	u.calls = append(u.calls, call)
	require.Less(u.t, index, len(u.results), "不应请求未授权的额外回退端点")
	if u.onCall != nil {
		u.onCall(index)
	}
	result := u.results[index]
	return result.response, result.err
}

// geminiModelSyncBody 记录关闭次数，确保失败与成功响应均释放连接。
type geminiModelSyncBody struct {
	io.Reader
	closeCount int
}

func (b *geminiModelSyncBody) Close() error {
	b.closeCount++
	return nil
}

// geminiModelSyncFailingReader 模拟读取响应失败，验证不能把不完整响应当作端点缺失。
type geminiModelSyncFailingReader struct{}

func (geminiModelSyncFailingReader) Read([]byte) (int, error) {
	return 0, errors.New("read-secret: private-gemini-sync-key")
}

func geminiModelSyncResponse(status int, contentType, body string) (*http.Response, *geminiModelSyncBody) {
	tracked := &geminiModelSyncBody{Reader: strings.NewReader(body)}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       tracked,
	}, tracked
}

func geminiModelSyncAccount(baseURL string) *Account {
	return &Account{
		ID:          42,
		Platform:    PlatformGemini,
		Type:        AccountTypeAPIKey,
		Concurrency: 7,
		Credentials: map[string]any{
			"api_key":       "private-gemini-sync-key",
			"base_url":      baseURL,
			"provider_type": "third_party",
		},
	}
}

func TestFetchUpstreamSupportedModelsGeminiNativeSuccess(t *testing.T) {
	t.Parallel()
	response, body := geminiModelSyncResponse(http.StatusOK, "application/json", `{"models":[{"name":"models/z-custom"},{"name":"models/gemini-pro"},{"name":"models/z-custom"},{"name":"models/a-alias"}]}`)
	upstream := &geminiModelSyncHTTPUpstream{t: t, results: []geminiModelSyncResult{{response: response}}}
	svc := &AccountTestService{httpUpstream: upstream, cfg: upstreamModelSyncTestConfig()}

	models, err := svc.FetchUpstreamSupportedModels(context.Background(), geminiModelSyncAccount("https://relay.example/tenant"))
	require.NoError(t, err)
	require.Equal(t, []string{"a-alias", "gemini-pro", "z-custom"}, models)
	require.Len(t, upstream.calls, 1)
	require.Equal(t, "https://relay.example/tenant/v1beta/models", upstream.calls[0].request.URL.String())
	require.Equal(t, "private-gemini-sync-key", upstream.calls[0].request.Header.Get("x-goog-api-key"))
	require.Empty(t, upstream.calls[0].request.Header.Get("Authorization"))
	require.Equal(t, 1, body.closeCount)
}

func TestFetchUpstreamSupportedModelsGeminiFallbackAllowedResponses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		{name: "native_not_found", status: http.StatusNotFound, contentType: "application/json", body: `{"error":"unsupported endpoint"}`},
		{name: "native_method_not_allowed", status: http.StatusMethodNotAllowed, contentType: "application/json", body: `{"error":"method not allowed"}`},
		{name: "cloudflare_block", status: http.StatusForbidden, contentType: "text/html; charset=UTF-8", body: `<html><h1>Sorry, you have been blocked</h1><div>Cloudflare Ray ID: example</div></html>`},
		{name: "cloudflare_challenge", status: http.StatusForbidden, contentType: "text/html", body: `<html><title>Just a moment...</title><script>window.__cf_chl_opt = {};</script></html>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			native, nativeBody := geminiModelSyncResponse(tc.status, tc.contentType, tc.body)
			fallback, fallbackBody := geminiModelSyncResponse(http.StatusOK, "application/json", `{"data":[{"id":"z-alias"},{"id":"gemini-flash"},{"id":"a-alias"},{"id":"z-alias"}]}`)
			upstream := &geminiModelSyncHTTPUpstream{t: t, results: []geminiModelSyncResult{{response: native}, {response: fallback}}}
			svc := &AccountTestService{httpUpstream: upstream, cfg: upstreamModelSyncTestConfig()}

			models, err := svc.FetchUpstreamSupportedModels(context.Background(), geminiModelSyncAccount("https://relay.example"))
			require.NoError(t, err)
			require.Equal(t, []string{"a-alias", "gemini-flash", "z-alias"}, models)
			require.Len(t, upstream.calls, 2)
			require.Equal(t, "/v1beta/models", upstream.calls[0].request.URL.Path)
			require.Equal(t, "/v1/models", upstream.calls[1].request.URL.Path)
			require.Equal(t, "Bearer private-gemini-sync-key", upstream.calls[1].request.Header.Get("Authorization"))
			require.Empty(t, upstream.calls[1].request.Header.Get("x-goog-api-key"))
			require.Equal(t, 1, nativeBody.closeCount)
			require.Equal(t, 1, fallbackBody.closeCount)
		})
	}
}

func TestFetchUpstreamSupportedModelsGeminiFallbackPreservesRequestSettings(t *testing.T) {
	t.Parallel()
	for _, suffix := range []string{"", "/", "/v1beta", "/v1beta/models"} {
		t.Run(suffix, func(t *testing.T) {
			t.Parallel()
			native, nativeBody := geminiModelSyncResponse(http.StatusNotFound, "application/json", `{}`)
			fallback, fallbackBody := geminiModelSyncResponse(http.StatusOK, "application/json", `{"data":[{"id":"private-model"}]}`)
			upstream := &geminiModelSyncHTTPUpstream{t: t, results: []geminiModelSyncResult{{response: native}, {response: fallback}}}
			// 回退发出前必须释放原生响应，避免连接资源被重试持有。
			upstream.onCall = func(index int) {
				if index == 1 {
					require.Equal(t, 1, nativeBody.closeCount)
				}
			}
			svc := &AccountTestService{httpUpstream: upstream, cfg: upstreamModelSyncTestConfig(), tlsFPProfileService: &TLSFingerprintProfileService{}}
			account := geminiModelSyncAccount("https://relay.example:8443/customer/gateway" + suffix)
			proxyID := int64(12)
			account.ProxyID = &proxyID
			account.Proxy = &Proxy{Protocol: "http", Host: "proxy.example", Port: 8080, Username: "user", Password: "pass"}
			type contextKey struct{}
			ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), contextKey{}, "request-marker"), time.Minute)
			defer cancel()

			_, err := svc.FetchUpstreamSupportedModels(ctx, account)
			require.NoError(t, err)
			require.Len(t, upstream.calls, 2)
			require.Equal(t, "https://relay.example:8443/customer/gateway/v1beta/models", upstream.calls[0].request.URL.String())
			require.Equal(t, "https://relay.example:8443/customer/gateway/v1/models", upstream.calls[1].request.URL.String())
			for _, call := range upstream.calls {
				require.Equal(t, http.MethodGet, call.request.Method)
				require.Equal(t, "application/json", call.request.Header.Get("Accept"))
				require.Equal(t, "http://user:pass@proxy.example:8080", call.proxyURL)
				require.Equal(t, account.ID, call.accountID)
				require.Equal(t, account.Concurrency, call.concurrency)
				require.True(t, call.withTLS)
				require.Equal(t, svc.tlsFPProfileService.ResolveTLSProfile(account), call.profile)
				require.Equal(t, "request-marker", call.request.Context().Value(contextKey{}))
				deadline, ok := call.request.Context().Deadline()
				require.True(t, ok)
				wantDeadline, _ := ctx.Deadline()
				require.Equal(t, wantDeadline, deadline)
			}
			require.Equal(t, 1, fallbackBody.closeCount)
		})
	}
}

func TestFetchUpstreamSupportedModelsGeminiDoesNotFallbackOnOtherFailures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		status      int
		contentType string
		body        string
		transport   error
		bodyLimit   int64
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, contentType: "application/json", body: `{"error":"bad credentials"}`},
		{name: "json_forbidden", status: http.StatusForbidden, contentType: "application/json", body: `{"error":"Cloudflare: Sorry, you have been blocked"}`},
		{name: "ordinary_html_forbidden", status: http.StatusForbidden, contentType: "text/html", body: `<html><h1>Access denied</h1></html>`},
		{name: "cloudflare_brand_only", status: http.StatusForbidden, contentType: "text/html", body: `<html><footer>Cloudflare</footer><p>Permission denied</p></html>`},
		{name: "rate_limited", status: http.StatusTooManyRequests, contentType: "application/json", body: `{}`},
		{name: "internal_error", status: http.StatusInternalServerError, contentType: "text/html", body: `<html>Cloudflare: Sorry, you have been blocked</html>`},
		{name: "bad_gateway", status: http.StatusBadGateway, contentType: "application/json", body: `{}`},
		{name: "service_unavailable", status: http.StatusServiceUnavailable, contentType: "application/json", body: `{}`},
		{name: "transport_error", transport: errors.New("transport-secret: private-gemini-sync-key")},
		{name: "invalid_json", status: http.StatusOK, contentType: "application/json", body: `not valid JSON`},
		{name: "empty_models", status: http.StatusOK, contentType: "application/json", body: `{"models":[]}`},
		{name: "oversized_not_found", status: http.StatusNotFound, contentType: "text/html", body: strings.Repeat("x", 65), bodyLimit: 64},
		{name: "oversized_cloudflare", status: http.StatusForbidden, contentType: "text/html", body: `<html>Sorry, you have been blocked: Cloudflare Ray ID</html>` + strings.Repeat("x", 65), bodyLimit: 64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			response, body := geminiModelSyncResponse(tc.status, tc.contentType, tc.body)
			response.Header.Set("Server", "cloudflare")
			result := geminiModelSyncResult{response: response}
			if tc.transport != nil {
				result = geminiModelSyncResult{err: tc.transport}
			}
			upstream := &geminiModelSyncHTTPUpstream{t: t, results: []geminiModelSyncResult{result}}
			cfg := upstreamModelSyncTestConfig()
			if tc.bodyLimit > 0 {
				cfg.Gateway.ModelsListReadMaxBytes = tc.bodyLimit
			}
			svc := &AccountTestService{httpUpstream: upstream, cfg: cfg}

			_, err := svc.FetchUpstreamSupportedModels(context.Background(), geminiModelSyncAccount("https://relay.example"))
			require.Error(t, err)
			require.Len(t, upstream.calls, 1)
			var syncErr *UpstreamModelSyncError
			require.ErrorAs(t, err, &syncErr)
			require.Equal(t, UpstreamModelSyncErrorUpstream, syncErr.Kind)
			require.NotContains(t, syncErr.SafeMessage(), "private-gemini-sync-key")
			require.NotContains(t, syncErr.SafeMessage(), "transport-secret")
			if tc.transport == nil {
				require.Equal(t, 1, body.closeCount)
			}
		})
	}
}

func TestFetchUpstreamSupportedModelsGeminiFallbackRequiresExplicitThirdParty(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		baseURL      string
		providerType string
		accountType  string
	}{
		{name: "legacy_custom_base", baseURL: "https://relay.example", accountType: AccountTypeAPIKey},
		{name: "official_custom_base", baseURL: "https://relay.example", providerType: "official", accountType: AccountTypeAPIKey},
		{name: "third_party_missing_base", providerType: "third_party", accountType: AccountTypeAPIKey},
		{name: "third_party_official_host", baseURL: "https://generativelanguage.googleapis.com", providerType: "third_party", accountType: AccountTypeAPIKey},
		{name: "third_party_official_host_case", baseURL: "https://GENERATIVELANGUAGE.GOOGLEAPIS.COM/v1beta", providerType: "third_party", accountType: AccountTypeAPIKey},
		{name: "oauth_custom_base", baseURL: "https://relay.example", providerType: "third_party", accountType: AccountTypeOAuth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			response, body := geminiModelSyncResponse(http.StatusNotFound, "application/json", `{}`)
			upstream := &geminiModelSyncHTTPUpstream{t: t, results: []geminiModelSyncResult{{response: response}}}
			svc := &AccountTestService{httpUpstream: upstream, cfg: upstreamModelSyncTestConfig(), geminiTokenProvider: NewGeminiTokenProvider(nil, nil, nil)}
			account := geminiModelSyncAccount(tc.baseURL)
			account.Type = tc.accountType
			delete(account.Credentials, "provider_type")
			if tc.providerType != "" {
				account.Credentials["provider_type"] = tc.providerType
			}
			account.Credentials["access_token"] = "oauth-token"
			account.Credentials["expires_at"] = time.Now().Add(time.Hour).Format(time.RFC3339)

			_, err := svc.FetchUpstreamSupportedModels(context.Background(), account)
			require.Error(t, err)
			require.Len(t, upstream.calls, 1)
			require.Equal(t, 1, body.closeCount)
			if tc.accountType == AccountTypeOAuth {
				require.Equal(t, "Bearer oauth-token", upstream.calls[0].request.Header.Get("Authorization"))
				require.Empty(t, upstream.calls[0].request.Header.Get("x-goog-api-key"))
			}
		})
	}
}

func TestFetchUpstreamSupportedModelsGeminiDoesNotFallbackAfterReadFailureOrCancellation(t *testing.T) {
	t.Parallel()
	for _, readFailure := range []bool{false, true} {
		name := "cancelled_request"
		if readFailure {
			name = "incomplete_response"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			response, body := geminiModelSyncResponse(http.StatusNotFound, "application/json", `{}`)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			upstream := &geminiModelSyncHTTPUpstream{t: t, results: []geminiModelSyncResult{{response: response}}}
			if readFailure {
				body.Reader = geminiModelSyncFailingReader{}
			} else {
				upstream.onCall = func(int) { cancel() }
			}
			svc := &AccountTestService{httpUpstream: upstream, cfg: upstreamModelSyncTestConfig()}

			_, err := svc.FetchUpstreamSupportedModels(ctx, geminiModelSyncAccount("https://relay.example"))
			require.Error(t, err)
			require.Len(t, upstream.calls, 1)
			require.Equal(t, 1, body.closeCount)
			var syncErr *UpstreamModelSyncError
			require.ErrorAs(t, err, &syncErr)
			require.NotContains(t, syncErr.SafeMessage(), "read-secret")
			require.NotContains(t, syncErr.SafeMessage(), "private-gemini-sync-key")
		})
	}
}

func TestFetchUpstreamSupportedModelsGeminiFallbackFailureIsSafe(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		status    int
		body      string
		transport error
		bodyLimit int64
		wantError string
	}{
		{name: "http_error", status: http.StatusUnauthorized, body: `{"error":"fallback-body-secret private-gemini-sync-key"}`},
		{name: "still_not_found", status: http.StatusNotFound, body: `{"error":"fallback-body-secret"}`},
		{name: "invalid_json", status: http.StatusOK, body: `fallback-body-secret`, wantError: "not valid JSON"},
		{name: "empty_models", status: http.StatusOK, body: `{"data":[]}`, wantError: "no supported models"},
		{name: "transport_error", transport: errors.New("transport-secret: private-gemini-sync-key"), wantError: "Failed to request"},
		{name: "oversized_fallback", status: http.StatusOK, body: strings.Repeat("x", 65), bodyLimit: 64, wantError: "too large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			native, nativeBody := geminiModelSyncResponse(http.StatusNotFound, "application/json", `{"error":"native-body-secret"}`)
			fallback, fallbackBody := geminiModelSyncResponse(tc.status, "application/json", tc.body)
			fallbackResult := geminiModelSyncResult{response: fallback}
			if tc.transport != nil {
				fallbackResult = geminiModelSyncResult{err: tc.transport}
			}
			upstream := &geminiModelSyncHTTPUpstream{t: t, results: []geminiModelSyncResult{{response: native}, fallbackResult}}
			cfg := upstreamModelSyncTestConfig()
			if tc.bodyLimit > 0 {
				cfg.Gateway.ModelsListReadMaxBytes = tc.bodyLimit
			}
			svc := &AccountTestService{httpUpstream: upstream, cfg: cfg}

			_, err := svc.FetchUpstreamSupportedModels(context.Background(), geminiModelSyncAccount("https://relay.example/tenant"))
			require.Error(t, err)
			require.Len(t, upstream.calls, 2)
			var syncErr *UpstreamModelSyncError
			require.ErrorAs(t, err, &syncErr)
			require.Equal(t, UpstreamModelSyncErrorUpstream, syncErr.Kind)
			message := syncErr.SafeMessage()
			require.Contains(t, message, "/v1beta/models")
			require.Contains(t, message, "/v1/models")
			require.Contains(t, message, "404")
			if tc.status >= http.StatusBadRequest {
				require.Contains(t, message, strconv.Itoa(tc.status))
			}
			if tc.wantError != "" {
				require.Contains(t, message, tc.wantError)
			}
			for _, secret := range []string{"private-gemini-sync-key", "native-body-secret", "fallback-body-secret", "transport-secret"} {
				require.NotContains(t, message, secret)
			}
			require.Equal(t, 1, nativeBody.closeCount)
			if tc.transport == nil {
				require.Equal(t, 1, fallbackBody.closeCount)
			}
		})
	}
}
