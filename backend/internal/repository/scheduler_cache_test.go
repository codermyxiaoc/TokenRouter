package repository

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// GO 调度投影经过缓存序列化后仍保留查询身份和 Zen/GO 协议差异，不能误用旧窗口。
func TestSchedulerMetadataOpenCodePreservesUsageIdentity(t *testing.T) {
	proxyID := int64(9)
	account := service.Account{
		ID: 60, Platform: service.PlatformOpenCodeGo, Type: service.AccountTypeAPIKey, Concurrency: 3,
		ProxyID: &proxyID, Proxy: &service.Proxy{ID: proxyID, Protocol: "http", Host: "relay.example", Port: 8080, Username: "proxy-user", Password: "proxy-secret"},
		Credentials: map[string]any{"api_key": "test-key", "account_mode": "zen", "api_protocol": "adaptive", "base_url": "https://opencode.ai/zen/v1", "account_scheduling_threshold": 80},
		Extra: map[string]any{
			"upstream_usage_query": map[string]any{"enabled": true}, "enable_tls_fingerprint": true,
			"tls_fingerprint_profile_id": 2, "tls_fingerprint_router_id": 5,
			"cn_usage_monitor_snapshot": map[string]any{"identity_hash": "verified-identity", "provider": "opencode_go"},
			"unrelated_secret":          "not-in-metadata",
		},
	}
	payload, err := json.Marshal(buildSchedulerMetadataAccount(account))
	require.NoError(t, err)
	var decoded service.Account
	require.NoError(t, json.Unmarshal(payload, &decoded))
	require.Equal(t, service.AccountModeZen, decoded.GetOpenCodeAccountMode())
	require.Equal(t, service.APIProtocolAnthropic, decoded.ResolveOpenCodeGoUpstreamProtocol("claude-sonnet-4-5"))
	require.Equal(t, account.Proxy, decoded.Proxy)
	require.Equal(t, account.ProxyID, decoded.ProxyID)
	for key, value := range account.Credentials {
		expected, _ := json.Marshal(value)
		actual, _ := json.Marshal(decoded.Credentials[key])
		require.JSONEq(t, string(expected), string(actual), key)
	}
	for _, key := range []string{"upstream_usage_query", "enable_tls_fingerprint", "tls_fingerprint_profile_id", "tls_fingerprint_router_id", "cn_usage_monitor_snapshot"} {
		expected, _ := json.Marshal(account.Extra[key])
		actual, _ := json.Marshal(decoded.Extra[key])
		require.JSONEq(t, string(expected), string(actual), key)
	}
	require.NotContains(t, decoded.Extra, "unrelated_secret")
}

// 缓存序列化后仍执行与完整账号相同的阈值，不携带 OAuth 凭据。
func TestSchedulerMetadataAccountPreservesAnthropicThreshold(t *testing.T) {
	now := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	account := service.Account{
		ID: 25, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"account_scheduling_threshold": 75, "access_token": "secret", "refresh_token": "secret-refresh"},
		Extra: map[string]any{
			"session_window_utilization":      0.1,
			"passive_usage_7d_utilization":    0.8,
			"passive_usage_7d_reset":          now.Add(48 * time.Hour).Unix(),
			"passive_usage_7d_oi_utilization": 0.85,
			"passive_usage_7d_oi_reset":       now.Add(72 * time.Hour).Unix(),
		},
	}
	_, payload, err := marshalSchedulerCacheAccount(account)
	require.NoError(t, err)
	metadata, err := decodeCachedAccount(payload)
	require.NoError(t, err)
	thresholds := map[string]int{service.PlatformAnthropic: 95}
	want := service.EvaluateAccountSchedulingThreshold(&account, thresholds, now)
	require.True(t, want.ShouldPause)
	require.Equal(t, want, service.EvaluateAccountSchedulingThreshold(metadata, thresholds, now))
	require.Equal(t, float64(0.85), metadata.Extra["passive_usage_7d_oi_utilization"])
	require.Equal(t, float64(now.Add(72*time.Hour).Unix()), metadata.Extra["passive_usage_7d_oi_reset"])
	require.Empty(t, metadata.GetCredential("access_token"))
	require.Empty(t, metadata.GetCredential("refresh_token"))
}

func TestFilterSchedulerCredentialsKeepsSubscriptionPlanType(t *testing.T) {
	filtered := filterSchedulerCredentials(map[string]any{
		"plan_type":     "plus",
		"access_token":  "secret-access-token",
		"refresh_token": "secret-refresh-token",
	})

	require.Equal(t, "plus", filtered["plan_type"])
	require.NotContains(t, filtered, "access_token")
	require.NotContains(t, filtered, "refresh_token")
}

func TestSchedulerMetadataAccountKeepsOpenAISubscriptionIdentity(t *testing.T) {
	account := service.Account{
		ID:       24,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Credentials: map[string]any{
			"plan_type":    "plus",
			"access_token": "secret-access-token",
		},
	}

	metadata := buildSchedulerMetadataAccount(account)

	require.True(t, metadata.IsOpenAIChatGPTSubscription())
	require.Empty(t, metadata.GetCredential("access_token"))
}
