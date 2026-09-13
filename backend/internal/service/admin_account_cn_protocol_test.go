package service

import (
	"context"
	"net/http"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// 创建与编辑必须接受前端提供的协议，并保留自适应模式的分协议地址。
func TestCNProviderProtocolCreateAndUpdate(t *testing.T) {
	for _, platform := range []string{PlatformDeepseek, PlatformKimi, PlatformZhipu, PlatformMiniMax} {
		modes := []string{AccountModePayG, AccountModeCoding}
		if platform == PlatformDeepseek {
			modes = []string{AccountModePayG}
		}
		protocols := []string{APIProtocolChatCompletions, APIProtocolAnthropic, APIProtocolAdaptive}
		if platform != PlatformZhipu {
			protocols = append(protocols, APIProtocolResponses)
		}
		for _, mode := range modes {
			for _, protocol := range protocols {
				for _, operation := range []string{"create", "update"} {
					t.Run(platform+"/"+mode+"/"+protocol+"/"+operation, func(t *testing.T) {
						t.Parallel()
						ctx := context.Background()
						repo := &accountServiceAdminTestRepo{accountServiceTestRepo: &accountServiceTestRepo{}}
						svc := &adminServiceImpl{accountRepo: repo}
						credentials := map[string]any{
							"api_key": "test-key", "account_mode": mode, "api_protocol": protocol,
						}
						if protocol == APIProtocolAdaptive {
							credentials["base_url"] = "https://relay.example/chat"
							credentials["api_base_urls"] = map[string]any{
								APIProtocolChatCompletions: "https://relay.example/chat",
								APIProtocolAnthropic:       "https://relay.example/anthropic",
								APIProtocolResponses:       "https://relay.example/responses",
							}
						}
						var account *Account
						var err error
						if operation == "create" {
							account, err = svc.CreateAccount(ctx, &CreateAccountInput{
								Name: "cn-protocol", Platform: platform, Type: AccountTypeAPIKey,
								Credentials: credentials, SkipDefaultGroupBind: true,
							})
						} else {
							// 直接放入历史固定协议账号，确保编辑路径可独立复现校验错误。
							existing := &Account{Platform: platform, Type: AccountTypeAPIKey,
								Credentials: map[string]any{"api_key": "old-key", "api_protocol": APIProtocolChatCompletions}}
							require.NoError(t, repo.Create(ctx, existing))
							account, err = svc.UpdateAccount(ctx, existing.ID, &UpdateAccountInput{Credentials: credentials})
						}
						require.NoError(t, err)
						stored, err := repo.GetByID(ctx, account.ID)
						require.NoError(t, err)
						require.Equal(t, platform, stored.Platform)
						require.Equal(t, mode, stored.GetAccountMode())
						require.Equal(t, protocol, stored.GetAPIProtocol())
						require.Equal(t, protocol, stored.GetCredential("api_protocol"))
						if protocol == APIProtocolAdaptive {
							require.Equal(t, credentials["api_base_urls"], stored.Credentials["api_base_urls"])
							require.Equal(t, "https://relay.example/anthropic", stored.GetCNProtocolBaseURL(APIProtocolAnthropic))
						}
					})
				}
			}
		}
	}
}

// 混合平台批量修改也必须接受自适应，并保留每个账号已有的认证字段。
func TestCNProviderProtocolBulkUpdate(t *testing.T) {
	ctx := context.Background()
	repo := &accountServiceTestRepo{}
	svc := &adminServiceImpl{accountRepo: repo}
	var ids []int64
	for _, platform := range []string{PlatformDeepseek, PlatformKimi, PlatformZhipu, PlatformMiniMax} {
		account := &Account{Platform: platform, Type: AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "key-" + platform, "api_protocol": APIProtocolChatCompletions}}
		require.NoError(t, repo.Create(ctx, account))
		ids = append(ids, account.ID)
	}
	result, err := svc.BulkUpdateAccounts(ctx, &BulkUpdateAccountsInput{
		AccountIDs: ids, Credentials: map[string]any{"api_protocol": APIProtocolAdaptive},
	})
	require.NoError(t, err)
	require.Equal(t, 4, result.Success)
	require.Zero(t, result.Failed)
	require.Len(t, repo.bulkUpdates, 1)
	require.Equal(t, map[string]any{"api_protocol": APIProtocolAdaptive}, repo.bulkUpdates[0].Credentials)
	for _, account := range repo.accounts {
		require.Equal(t, "key-"+account.Platform, account.GetCredential("api_key"))
	}
}

// 放行自适应不能同时放行未知协议、GLM 原生 Responses 或不支持的账号组合。
func TestCNProviderProtocolValidationRejectsUnsupportedCombinations(t *testing.T) {
	for _, tc := range []struct {
		name, platform, accountType, mode, protocol, reason string
	}{
		{"unknown protocol", PlatformMiniMax, AccountTypeAPIKey, AccountModePayG, "unknown", "CN_PROVIDER_PROTOCOL_INVALID"},
		{"glm responses", PlatformZhipu, AccountTypeAPIKey, AccountModePayG, APIProtocolResponses, "CN_PROVIDER_PROTOCOL_INVALID"},
		{"deepseek coding", PlatformDeepseek, AccountTypeAPIKey, AccountModeCoding, APIProtocolAdaptive, "CN_PROVIDER_MODE_INVALID"},
		{"oauth", PlatformKimi, AccountTypeOAuth, AccountModePayG, APIProtocolAdaptive, "CN_PROVIDER_ACCOUNT_TYPE_INVALID"},
		{"unknown mode", PlatformKimi, AccountTypeAPIKey, "unknown", APIProtocolAdaptive, "CN_PROVIDER_ACCOUNT_MODE_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := normalizeCNProviderCredentials(&Account{
				Platform: tc.platform, Type: tc.accountType,
				Credentials: map[string]any{"account_mode": tc.mode, "api_protocol": tc.protocol},
			}, true)
			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Equal(t, tc.reason, infraerrors.Reason(err))
		})
	}
}
