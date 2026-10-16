package service

import (
	"context"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
	"testing"
)

// 订阅退役作用于真实上游目标，不能误伤 API Key、其它平台和显式替代映射。
func TestGPT55RetirementModelSupport(t *testing.T) {
	for _, typ := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey} {
		for _, passthrough := range []bool{false, true} {
			t.Run(typ+"/passthrough="+map[bool]string{true: "true", false: "false"}[passthrough], func(t *testing.T) {
				account := &Account{Platform: PlatformOpenAI, Type: typ, Extra: map[string]any{"openai_passthrough": passthrough}}
				for _, model := range []string{"gpt-5.5", "openai/gpt-5.5-high", "gpt-5.5-2026-04-23", "GPT_5.5", "gpt-5.5-openai-compact"} {
					require.Equal(t, typ == AccountTypeAPIKey, account.IsModelSupported(model), model)
				}
				require.True(t, account.IsModelSupported("gpt-6.1-sol"))
			})
		}
	}
	for _, mapping := range []map[string]any{
		{"legacy": "gpt-5.5"}, {"legacy*": "gpt-5.5"},
	} {
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"model_mapping": mapping}}
		require.False(t, account.IsModelSupported("legacy"))
	}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5.5": "gpt-6.1-sol"}}}
	require.True(t, account.IsModelSupported("gpt-5.5"), "管理员显式替代映射仍有权威性")
	account.Type = AccountTypeAPIKey
	require.True(t, account.IsModelSupported("gpt-5.5"))
	parent := int64(3)
	shadow := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, ParentAccountID: &parent}
	require.False(t, shadow.IsModelSupported("gpt-5.5"))
	require.False(t, isRetiredOpenAISubscriptionModel(&Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}, "gpt-5.5"))
	require.False(t, isRetiredOpenAISubscriptionModel(account, "gpt-5.50"))
}

// 先解析公开别名，再按最终模型过滤，交叉映射不能递归到另一个目标。
func TestGPT55RetirementForkMappingsAndCatalog(t *testing.T) {
	oauth := Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"model_mapping":   map[string]any{"legacy": "gpt-5.5", "gpt-5.5": "gpt-6.1-sol", "gpt-6.1-sol": "gpt-5.5"},
		"model_whitelist": []any{"gpt-5.5", "gpt-6.1-sol"},
	}}
	require.False(t, oauth.IsModelSupported("legacy"))
	require.True(t, oauth.IsModelSupported("gpt-5.5"))
	require.False(t, oauth.IsModelSupported("gpt-6.1-sol"))
	require.Equal(t, []string{"gpt-5.5"}, FilterRetiredOpenAIModelIDs([]Account{oauth}, []string{"legacy", "gpt-5.5", "gpt-6.1-sol"}))
	api := oauth
	api.Type = AccountTypeAPIKey
	require.Equal(t, []string{"legacy", "gpt-5.5", "gpt-6.1-sol"}, FilterRetiredOpenAIModelIDs([]Account{oauth, api}, []string{"legacy", "gpt-5.5", "gpt-6.1-sol"}))
	require.True(t, IsRetiredOpenAISubscriptionModel(&oauth, "gpt-5.5"), "最终模型不能再次执行 gpt-5.5 -> gpt-6.1-sol 映射")
	require.False(t, oauth.IsModelSupported("not-in-whitelist"), "退役适配不能绕过本项目最终白名单")
}

// compact 专用映射与 HTTP 保真模式使用真实目标，允许替代但禁止退回已退役型号。
func TestGPT55RetirementForkCompactRouting(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"openai_passthrough": passthrough}, Credentials: map[string]any{"compact_model_mapping": map[string]any{"gpt-5.5": "gpt-6.1-sol"}}}
		ctx := WithOpenAIHTTPPassthroughRouting(context.Background())
		require.True(t, openAIAccountSupportsRoutingModel(ctx, account, "gpt-5.5", true))
		require.False(t, openAIAccountSupportsRoutingModel(ctx, account, "gpt-5.5", false))
		svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{OpenAICompactModel: "gpt-5.5"}}}
		require.Equal(t, "gpt-6.1-sol", svc.resolveOpenAICompactFallbackModel(account, "gpt-5.5"))
		require.Empty(t, svc.resolveOpenAICompactFallbackModel(account, "gpt-6.1-sol"))
		account.Type = AccountTypeAPIKey
		require.True(t, openAIAccountSupportsRoutingModel(ctx, account, "gpt-5.5", false))
		require.Equal(t, "gpt-5.5", svc.resolveOpenAICompactFallbackModel(account, "gpt-6.1-sol"))
	}
}

// 基础和高级调度均拒绝退役订阅目标，混合 API Key 候选保持模型可用诊断。
func TestGPT55RetirementForkSchedulerAndDiagnosis(t *testing.T) {
	ctx := context.Background()
	groupID := int64(1)
	oauth := Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, GroupIDs: []int64{groupID}}
	api := Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{groupID}}
	scheduler := &defaultOpenAIAccountScheduler{}
	for _, typ := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey} {
		account := oauth
		account.Type = typ
		ok, _ := scheduler.isAccountRequestCompatibleReason(ctx, &account, OpenAIAccountScheduleRequest{RequestedModel: "gpt-5.5"})
		require.Equal(t, typ == AccountTypeAPIKey, ok)
		reason := openAICompatibleAccountEligibilityFailureReasonBeforeProfit(ctx, &account, PlatformOpenAI, "gpt-5.5", false, "")
		require.Equal(t, typ == AccountTypeAPIKey, reason == "")
	}
	oauth.Credentials = map[string]any{"compact_model_mapping": map[string]any{"gpt-6.1-sol": "gpt-5.5"}}
	ok, reason := scheduler.isAccountRequestCompatibleReason(ctx, &oauth, OpenAIAccountScheduleRequest{RequestedModel: "gpt-6.1-sol", RequireCompact: true})
	require.False(t, ok)
	require.Equal(t, "model_not_supported", reason)
	oauth.Credentials = map[string]any{"compact_model_mapping": map[string]any{"gpt-5.5": "gpt-6.1-sol"}}
	ok, _ = scheduler.isAccountRequestCompatibleReason(ctx, &oauth, OpenAIAccountScheduleRequest{RequestedModel: "gpt-5.5", RequireCompact: true})
	require.True(t, ok)
	for _, accounts := range [][]Account{{oauth}, {oauth, api}} {
		svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}}
		diagnosis := svc.DiagnoseModelAvailabilityForPlatform(ctx, &groupID, "gpt-5.5", PlatformOpenAI)
		require.True(t, diagnosis.HasAccountsInPool)
		require.True(t, diagnosis.HasRetiredSubscriptionModel)
		require.Equal(t, len(accounts) == 2, diagnosis.HasModelSupport)
	}
}
