//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 显式来源账号沿用本站旧策略，新增的地址识别不能把所有中继错误套入同一冷却窗口。
func TestGeminiRelayCooldownPreservesProviderPolicy(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		body      string
		settings  *RateLimit429CooldownSettings
		noService bool
		wantWait  time.Duration
		wantCalls int
	}{
		{name: "explicit_provider_default", provider: GeminiProviderTypeThirdParty, body: `{}`, wantWait: 5 * time.Minute, wantCalls: 1},
		{name: "explicit_provider_keeps_legacy_window", provider: GeminiProviderTypeThirdParty, body: `{"error":{"details":[{"metadata":{"quotaResetDelay":"42s"}}]}}`, wantWait: 5 * time.Minute, wantCalls: 1},
		{name: "explicit_provider_not_global_fallback", provider: GeminiProviderTypeThirdParty, body: `{}`, settings: &RateLimit429CooldownSettings{Enabled: false, CooldownSeconds: 12}, wantWait: 5 * time.Minute, wantCalls: 1},
		{name: "relay_default_seconds", body: `{}`, wantWait: 5 * time.Second, wantCalls: 1},
		{name: "relay_without_service", body: `{}`, noService: true, wantWait: 5 * time.Second, wantCalls: 1},
		{name: "relay_global_config", body: `{}`, settings: &RateLimit429CooldownSettings{Enabled: true, CooldownSeconds: 12}, wantWait: 12 * time.Second, wantCalls: 1},
		{name: "relay_global_disabled", body: `{}`, settings: &RateLimit429CooldownSettings{Enabled: false, CooldownSeconds: 12}},
		{name: "relay_explicit_retry_overrides_disabled_fallback", body: `{"error":{"details":[{"metadata":{"quotaResetDelay":"42s"}}]}}`, settings: &RateLimit429CooldownSettings{Enabled: false, CooldownSeconds: 12}, wantWait: 42 * time.Second, wantCalls: 1},
		{name: "relay_daily_message_keeps_explicit_retry", body: `{"error":{"message":"Quota exceeded: 20 requests per day","details":[{"metadata":{"quotaResetDelay":"42s"}}]}}`, wantWait: 42 * time.Second, wantCalls: 1},
		{name: "relay_retry_info", body: `{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"39s"}]}}`, wantWait: 39 * time.Second, wantCalls: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &rateLimit429AccountRepoStub{}
			svc := &GeminiMessagesCompatService{accountRepo: repo}
			if !tc.noService {
				svc.rateLimitService = NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
				if tc.settings != nil {
					settingsRepo := newMockSettingRepo()
					data, err := json.Marshal(tc.settings)
					require.NoError(t, err)
					settingsRepo.data[SettingKeyRateLimit429CooldownSettings] = string(data)
					svc.rateLimitService.SetSettingService(NewSettingService(settingsRepo, &config.Config{}))
				}
			}
			account := &Account{
				ID: 701, Platform: PlatformGemini, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"base_url": "https://relay.example.com", GeminiProviderTypeCredentialKey: tc.provider},
			}
			before := time.Now()
			svc.handleGeminiUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, []byte(tc.body))
			require.Equal(t, tc.wantCalls, repo.rateLimitCalls)
			if tc.wantCalls > 0 {
				require.WithinDuration(t, before.Add(tc.wantWait), repo.lastRateLimitReset, 2*time.Second)
			}
		})
	}
}
