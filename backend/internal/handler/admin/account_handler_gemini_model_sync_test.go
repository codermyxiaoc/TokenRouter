package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

// geminiPreviewModelSyncUpstream 用路由实际发出的请求验证临时账号保留供应商类型。
type geminiPreviewModelSyncUpstream struct {
	t        *testing.T
	requests []*http.Request
}

func (u *geminiPreviewModelSyncUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.t.Helper()
	u.requests = append(u.requests, req)
	status := http.StatusNotFound
	body := `{"error":"native endpoint unavailable"}`
	if len(u.requests) == 2 {
		status = http.StatusOK
		body = `{"data":[{"id":"custom-alias"}]}`
	}
	require.LessOrEqual(u.t, len(u.requests), 2, "模型同步最多只能回退一次")
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func (u *geminiPreviewModelSyncUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func TestAccountHandlerSyncUpstreamModelsPreview_GeminiProviderType(t *testing.T) {
	cases := []struct {
		name         string
		providerType string
		wantStatus   int
		wantCalls    int
	}{
		{name: "explicit_third_party", providerType: "third_party", wantStatus: http.StatusOK, wantCalls: 2},
		{name: "omitted_legacy_provider", wantStatus: http.StatusBadGateway, wantCalls: 1},
		{name: "explicit_official", providerType: "official", wantStatus: http.StatusBadGateway, wantCalls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &geminiPreviewModelSyncUpstream{t: t}
			router := setupSyncUpstreamModelsRouter(newStubAdminService(), upstream)
			payload := map[string]string{
				"platform": "gemini",
				"type":     "apikey",
				"base_url": "https://relay.example/customer/v1beta",
				"api_key":  "preview-private-key",
			}
			if tc.providerType != "" {
				payload["provider_type"] = tc.providerType
			}
			encoded, err := json.Marshal(payload)
			require.NoError(t, err)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/models/sync-upstream-preview", strings.NewReader(string(encoded)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
			require.Len(t, upstream.requests, tc.wantCalls)
			require.Equal(t, "https://relay.example/customer/v1beta/models", upstream.requests[0].URL.String())
			require.Equal(t, "preview-private-key", upstream.requests[0].Header.Get("x-goog-api-key"))
			require.Empty(t, upstream.requests[0].Header.Get("Authorization"))
			if tc.wantCalls == 2 {
				require.Equal(t, "https://relay.example/customer/v1/models", upstream.requests[1].URL.String())
				require.Equal(t, "Bearer preview-private-key", upstream.requests[1].Header.Get("Authorization"))
				require.Empty(t, upstream.requests[1].Header.Get("x-goog-api-key"))
				var response struct {
					Data struct {
						Models []string `json:"models"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
				require.Equal(t, []string{"custom-alias"}, response.Data.Models)
			}
			require.NotContains(t, rec.Body.String(), "preview-private-key")
		})
	}
}
