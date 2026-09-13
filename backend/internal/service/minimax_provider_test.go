package service

import (
	"context"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestMiniMaxAccountModesAndProtocols(t *testing.T) {
	// MiniMax 两种计费模式共享协议能力；原生协议选择不应改变平台身份。
	for _, mode := range []string{AccountModePayG, AccountModeCoding} {
		for _, protocol := range []string{APIProtocolChatCompletions, APIProtocolAnthropic, APIProtocolResponses, APIProtocolAdaptive} {
			t.Run(mode+"/"+protocol, func(t *testing.T) {
				account := &Account{Platform: PlatformMiniMax, Type: AccountTypeAPIKey, Credentials: map[string]any{
					"api_key": "minimax-key", "account_mode": mode, "api_protocol": protocol,
				}}
				require.NoError(t, normalizeCNProviderCredentials(account, true))
				require.True(t, account.IsMiniMax())
				require.True(t, account.IsCNProvider())
				require.True(t, account.IsOpenAICompatible())
				require.Equal(t, mode, account.GetAccountMode())
				require.Equal(t, protocol, account.GetAPIProtocol())
				require.True(t, account.SupportsNativeCNResponses())
				require.Equal(t, protocol == APIProtocolResponses || protocol == APIProtocolAdaptive, account.UsesNativeCNResponses())
				require.Equal(t, "minimax-key", account.GetOpenAIProtocolAPIKey())
				if mode == AccountModeCoding {
					require.Equal(t, PlatformMiniMax, account.GetCodingPlanProvider())
				}
			})
		}
	}
	account := &Account{Platform: PlatformMiniMax, Type: AccountTypeAPIKey}
	require.NoError(t, normalizeCNProviderCredentials(account, true))
	require.Equal(t, AccountModePayG, account.GetCredential("account_mode"))
	require.Equal(t, APIProtocolChatCompletions, account.GetCredential("api_protocol"))
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeUpstream, AccountTypeBedrock, AccountTypeServiceAccount, AccountTypeCosy} {
		account := &Account{Platform: PlatformMiniMax, Type: accountType}
		require.Error(t, normalizeCNProviderCredentials(account, true))
		require.False(t, account.IsHeaderOverrideEligible())
	}
}

func TestMiniMaxEndpointsAndModelSync(t *testing.T) {
	svc := &AccountTestService{cfg: upstreamModelSyncTestConfig()}
	for _, mode := range []string{AccountModePayG, AccountModeCoding} {
		for _, protocol := range []string{APIProtocolChatCompletions, APIProtocolAnthropic, APIProtocolResponses, APIProtocolAdaptive} {
			account := &Account{Platform: PlatformMiniMax, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"api_key": "minimax-key", "account_mode": mode, "api_protocol": protocol,
			}}
			require.Equal(t, DefaultMiniMaxBaseURL, account.GetOpenAIBaseURL())
			require.Equal(t, DefaultMiniMaxBaseURL, account.GetOpenAIFormatBaseURL())
			require.Equal(t, DefaultMiniMaxBaseURL, account.GetCNProtocolBaseURL(APIProtocolResponses))
			require.Equal(t, DefaultMiniMaxAnthropicBaseURL, account.GetCNProtocolBaseURL(APIProtocolAnthropic))
			request, err := svc.buildUpstreamModelsRequest(context.Background(), account)
			require.NoError(t, err)
			require.Equal(t, "https://api.minimax.io/v1/models", request.URL.String())
			require.Equal(t, "Bearer minimax-key", request.Header.Get("Authorization"))
		}
	}
	// 自定义中继保持原地址和路径前缀，不能因为 MiniMax 身份被替换为官方主机。
	account := &Account{Platform: PlatformMiniMax, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_protocol": APIProtocolAnthropic, "base_url": "https://relay.example/minimax/anthropic", "api_key": "key",
	}}
	require.Equal(t, "https://relay.example/minimax/anthropic", account.GetAnthropicProtocolBaseURL())
	require.Equal(t, "https://relay.example/minimax", account.GetOpenAIFormatBaseURL())
	account.Credentials["api_protocol"] = APIProtocolAdaptive
	account.Credentials["api_base_urls"] = map[string]any{APIProtocolAnthropic: "https://relay.example/native", APIProtocolResponses: "https://relay.example/responses"}
	require.Equal(t, "https://relay.example/native", account.GetAnthropicProtocolBaseURL())
	require.Equal(t, "https://relay.example/responses", account.GetCNProtocolBaseURL(APIProtocolResponses))
}

func TestMiniMaxQuotaAndGroupDefaults(t *testing.T) {
	require.True(t, IsAllowedQuotaPlatform(PlatformMiniMax))
	require.Contains(t, AllowedSchedulingThresholdPlatforms, PlatformMiniMax)
	models := defaultModelsListCandidateIDs(PlatformMiniMax)
	require.Equal(t, domain.DefaultMiniMaxModelIDs(), models)
	require.Equal(t, "MiniMax-M2.7", models[0])
	require.NotContains(t, models, "claude-sonnet-4-5")
	// 请求目录与管理目录保持一致，智能路由不能将 MiniMax 账号误判为 Claude。
	require.Equal(t, models, defaultRequestModelIDsForPlatform(PlatformMiniMax))
	account := &Account{Platform: PlatformMiniMax, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	require.True(t, smartRoutingAccountCatalogContains(account, "MiniMax-M2.7"))
	require.False(t, smartRoutingAccountCatalogContains(account, "claude-sonnet-4-5"))
}
