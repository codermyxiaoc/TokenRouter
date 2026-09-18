//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAdminProxyUpdateCredentialPresence(t *testing.T) {
	for _, tc := range []struct{ name, payload, username, password string }{
		{"omitted", `{}`, "old-user", "old-secret"},
		{"null", `{"Username":null,"Password":null}`, "old-user", "old-secret"},
		{"clear_both", `{"Username":"","Password":""}`, "", ""},
		{"clear_password", `{"Password":""}`, "old-user", ""},
		{"clear_username", `{"Username":""}`, "", "old-secret"},
		{"replace_password", `{"Password":"new-secret"}`, "old-user", "new-secret"},
		{"literal_whitespace", `{"Username":" ","Password":" secret "}`, " ", " secret "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expires := time.Now().Add(time.Hour)
			backup := int64(12)
			repo := &updatingProxyRepoStub{proxyRepoStub: &proxyRepoStub{}, proxy: &Proxy{ID: 9, Username: "old-user", Password: "old-secret", ExpiresAt: &expires, FallbackMode: FallbackModeProxy, BackupProxyID: &backup, ExpiryWarnDays: 7}}
			input := &UpdateProxyInput{ExpiresAt: &expires, FallbackMode: FallbackModeProxy, BackupProxyID: &backup, ExpiryWarnDays: 7}
			require.NoError(t, json.Unmarshal([]byte(tc.payload), input))
			_, err := (&adminServiceImpl{proxyRepo: repo}).UpdateProxy(context.Background(), 9, input)
			require.NoError(t, err)
			require.Equal(t, tc.username, repo.proxy.Username)
			require.Equal(t, tc.password, repo.proxy.Password)
			require.Equal(t, &expires, repo.proxy.ExpiresAt)
			require.Equal(t, FallbackModeProxy, repo.proxy.FallbackMode)
			require.Equal(t, &backup, repo.proxy.BackupProxyID)
			require.Equal(t, 1, repo.updateCalls)
		})
	}
}
