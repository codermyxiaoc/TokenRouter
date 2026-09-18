package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

// 质询页没有旧正文关键字时，仍应通过标准响应头正确标记隐私失败原因。
func TestDisableOpenAITraining_CloudflareChallengeHeader(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		header string
		body   string
		want   string
	}{
		{"header_only_403", http.StatusForbidden, "challenge", "blocked", PrivacyModeCFBlocked},
		{"header_only_503", http.StatusServiceUnavailable, "Challenge", "blocked", PrivacyModeCFBlocked},
		{"legacy_body", http.StatusForbidden, "", "Just a moment", PrivacyModeCFBlocked},
		{"ordinary_failure", http.StatusServiceUnavailable, "", "unavailable", PrivacyModeFailed},
		{"success", http.StatusOK, "", `{}`, PrivacyModeTrainingOff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPatch {
					t.Errorf("expected PATCH, got %s", r.Method)
				}
				w.Header().Set("cf-mitigated", tc.header)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			oldURL := openAISettingsURL
			openAISettingsURL = server.URL
			t.Cleanup(func() { openAISettingsURL = oldURL })
			factory := func(string) (*req.Client, error) { return req.C(), nil }
			require.Equal(t, tc.want, disableOpenAITraining(context.Background(), factory, "test-token", ""))
		})
	}
}
