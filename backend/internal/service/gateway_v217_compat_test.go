package service

import (
	"context"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

// 兼容转换必须尊重部署下限，不能把省略的预算强行改为固定值。
func TestCompatibilityConfiguredOutputFloor(t *testing.T) {
	for _, floor := range []int{16, 128, 256} {
		cfg := &config.Config{Gateway: config.GatewayConfig{MinOutputTokens: floor}}
		for _, input := range []int{0, 1, 16, 512} {
			want := input
			if want > 0 && want < floor {
				want = floor
			}
			chat, err := chatCompletionsToResponses(cfg, &apicompat.ChatCompletionsRequest{Model: "test", MaxTokens: &input})
			require.NoError(t, err)
			if want == 0 {
				require.Nil(t, chat.MaxOutputTokens)
			} else {
				require.Equal(t, want, *chat.MaxOutputTokens)
			}
			anthropic, err := anthropicToResponses(cfg, &apicompat.AnthropicRequest{Model: "test", MaxTokens: input})
			require.NoError(t, err)
			if want == 0 {
				require.Nil(t, anthropic.MaxOutputTokens)
			} else {
				require.Equal(t, want, *anthropic.MaxOutputTokens)
			}
		}
	}
	require.Equal(t, 128, configuredMinOutputTokens(nil))
	require.Equal(t, 128, configuredMinOutputTokens(&config.Config{}))
}

// 最终退役判断必须发生在渠道映射、账号映射之后，且不得重复映射。
func TestRequestableModelsRetirementAfterChannelAndAccountMappings(t *testing.T) {
	groupID := int64(217)
	channel := Channel{ID: 217, Status: StatusActive, ModelMapping: map[string]map[string]string{PlatformOpenAI: {"public-alias": "account-alias"}}}
	for _, tc := range []struct {
		name, typ, target string
		keep              bool
	}{
		{"订阅目标退役", AccountTypeOAuth, "gpt-5.5", false},
		{"订阅有效替代", AccountTypeOAuth, "gpt-6.1-sol", true},
		{"APIKey仍可用", AccountTypeAPIKey, "gpt-5.5", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := Account{ID: 217, Platform: PlatformOpenAI, Type: tc.typ, Credentials: map[string]any{
				"model_mapping":   map[string]any{"account-alias": tc.target, "gpt-5.5": "gpt-6.1-sol"},
				"model_whitelist": []any{tc.target},
			}}
			svc := &GatewayService{accountRepo: &modelsListAccountRepoStub{byGroup: map[int64][]Account{groupID: {account}}}, channelService: newRequestableModelsChannelService(groupID, PlatformOpenAI, channel)}
			result := svc.ResolveRequestableModels(context.Background(), &groupID, PlatformOpenAI)
			_, ok := requestableModelByID(result.Models, "public-alias")
			require.Equal(t, tc.keep, ok)
		})
	}
}

// 显式空模型结果不得回填默认目录；混合 API Key 账号仍能提供旧型号。
func TestRequestableModelsRetirementEmptyAndMixedPool(t *testing.T) {
	groupID := int64(218)
	oauth := Account{ID: 218, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"model_whitelist": []any{"gpt-5.5"}}}
	svc := &GatewayService{accountRepo: &modelsListAccountRepoStub{byGroup: map[int64][]Account{groupID: {oauth}}}}
	result := svc.ResolveRequestableModels(context.Background(), &groupID, PlatformOpenAI)
	require.Empty(t, result.Models)
	require.True(t, result.Restricted)
	fallback := requestableModelsFallback([]string{}, PlatformOpenAI)
	require.Empty(t, fallback.Models)
	require.True(t, fallback.Restricted)
	api := oauth
	api.ID = 219
	api.Type = AccountTypeAPIKey
	svc.accountRepo = &modelsListAccountRepoStub{byGroup: map[int64][]Account{groupID: {oauth, api}}}
	result = svc.ResolveRequestableModels(context.Background(), &groupID, PlatformOpenAI)
	require.Contains(t, RequestableModelIDs(result.Models), "gpt-5.5")
}
