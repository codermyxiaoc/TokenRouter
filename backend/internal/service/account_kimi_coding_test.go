package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetCodingPlanProvider_KimiOfficialURLs(t *testing.T) {
	for _, host := range []string{"api.kimi.com", "api.kimi.ai", "API.KIMI.AI", "api.kimi.ai:443"} {
		for _, path := range []string{"/coding", "/coding/", "/coding/v1", "/coding/v1/"} {
			t.Run(host+path, func(t *testing.T) {
				account := &Account{Platform: PlatformKimi, Type: AccountTypeAPIKey, Credentials: map[string]any{
					"account_mode": AccountModeCoding,
					"base_url":     "https://" + host + path,
				}}
				require.Equal(t, PlatformKimi, account.GetCodingPlanProvider())
				endpoint, err := cnUsageEndpoint(account.GetOpenAIBaseURL(), "/v1/usages", true)
				require.NoError(t, err)
				require.Equal(t, "https://"+host+"/coding/v1/usages", endpoint)
				account.Credentials["account_mode"] = AccountModePayG
				require.Empty(t, account.GetCodingPlanProvider())
			})
		}
	}
}

// 本站按平台声明兼容第三方中转地址，不限制为上游官方域名。
func TestGetCodingPlanProvider_KimiRelayRemainsSupported(t *testing.T) {
	account := &Account{Platform: PlatformKimi, Type: AccountTypeAPIKey, Credentials: map[string]any{"account_mode": AccountModeCoding, "base_url": "https://relay.example/coding/v1"}}
	require.Equal(t, PlatformKimi, account.GetCodingPlanProvider())
	endpoint, err := cnUsageEndpoint(account.GetOpenAIBaseURL(), "/v1/usages", true)
	require.NoError(t, err)
	require.Equal(t, "https://relay.example/coding/v1/usages", endpoint)
}

func TestGetCodingPlanProvider_UnrelatedProvidersUnchanged(t *testing.T) {
	for _, testCase := range []struct {
		platform string
		baseURL  string
	}{
		{PlatformZhipu, "https://open.bigmodel.cn/api/coding/paas/v4"},
		{PlatformZhipu, "https://api.z.ai/api/coding/paas/v4"},
		{PlatformMiniMax, "https://api.minimax.io/v1"},
		{PlatformMiniMax, "https://api.minimaxi.com/v1"},
		{PlatformMiniMax, "https://api.minimax.com/v1"},
	} {
		t.Run(testCase.baseURL, func(t *testing.T) {
			account := &Account{Platform: testCase.platform, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"account_mode": AccountModeCoding,
				"base_url":     testCase.baseURL,
			}}
			require.Equal(t, testCase.platform, account.GetCodingPlanProvider())
		})
	}
}
