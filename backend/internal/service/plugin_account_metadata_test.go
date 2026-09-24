package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	pluginv1 "github.com/TokenFlux/TokenRouter/pkg/pluginapi/v1"
	"github.com/stretchr/testify/require"
)

// 元数据需要保持暂停状态且不能把凭据、任意扩展配置、代理密码或循环关联交给插件。
func TestPluginAccountMetadataPreservesScopeAndRedactsSecrets(t *testing.T) {
	until := time.Now().Add(time.Hour)
	account := Account{ID: 1, Name: "paused", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Schedulable: true, RateLimitResetAt: &until, GroupIDs: []int64{8}, Concurrency: 7,
		Credentials: map[string]any{"refresh_token": "secret-refresh"}, Extra: map[string]any{"custom_secret": "secret-extra"},
		Proxy: &Proxy{Password: "secret-proxy"}}
	account.Groups = []*Group{{AccountGroups: []AccountGroup{{Account: &account}}}}
	svc := &OpenAIGatewayService{accountRepo: &pluginScopeAccountRepository{accounts: []Account{account,
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive},
		{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive},
	}}}
	server := newPluginHostServiceServer("test.metadata", nil, svc)
	result, err := server.ListAccounts(context.Background(), &pluginv1.ListAccountsRequest{})
	require.NoError(t, err)
	require.Equal(t, []int64{1}, result.AccountIds)
	require.Len(t, result.Accounts, 1)
	info := result.Accounts[0]
	require.False(t, info.Schedulable)
	require.Equal(t, "paused", info.Name)
	require.NotContains(t, string(info.MetadataJson), "secret-")
	var snapshot map[string]any
	require.NoError(t, json.Unmarshal(info.MetadataJson, &snapshot))
	require.Equal(t, float64(7), snapshot["Concurrency"])
	require.NotNil(t, snapshot["RateLimitResetAt"])
	require.Nil(t, snapshot["Credentials"])
	require.Nil(t, snapshot["Proxy"])
	require.Equal(t, "secret-refresh", account.Credentials["refresh_token"])
	filtered, err := server.ListAccounts(context.Background(), &pluginv1.ListAccountsRequest{Platform: PlatformAnthropic})
	require.NoError(t, err)
	require.Empty(t, filtered.Accounts)
	// protobuf 的新增字段应可完整往返，旧 account_ids 字段仍保留。
	require.EqualValues(t, 2, pluginv1.HostServiceAPIVersion)
}
