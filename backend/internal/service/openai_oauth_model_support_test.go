//go:build unit

package service

import (
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

func newOpenAIOAuthAccountForModelTest() *Account {
	return &Account{
		ID:       1,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}
}

func TestIsModelSupported_OpenAIOAuthEmptyMapping_ServableModels(t *testing.T) {
	account := newOpenAIOAuthAccountForModelTest()

	servable := []string{
		"", // 空模型交由上层必填校验。
		"gpt-5.4",
		"gpt-5.4-high", // 推理后缀变体。
		"gpt-5.3-codex",
		"gpt-5.1-codex-mini",
		"gpt-5",
		"codex-mini-latest",
		"gpt5.3codexspark", // 别名拼写。
		"gpt-image-1",      // 图像生成模型。
		"gpt-4o",           // 保守放行：非黑名单模型保持允许。
		"my-custom-alias",  // 自定义别名可能由渠道级映射改写，保持允许。
	}
	for _, model := range servable {
		require.True(t, account.IsModelSupported(model), "expected %q to be servable by empty-mapping OpenAI OAuth account", model)
	}
}

func TestIsModelSupported_OpenAIOAuthEmptyMapping_RejectsForeignModels(t *testing.T) {
	account := newOpenAIOAuthAccountForModelTest()

	// Codex 上游必然以不可重试的 400 拒绝这些厂商家族，调度阶段就应跳过
	// 该账号，让明确支持的 API Key 账号接手。
	foreign := []string{
		"claude-sonnet-4-6",
		"vendor/Claude-3-opus-20240229",
		"deepseek-v4",
		"deepseek-chat",
		"glm-4.7",
		"kimi-k2",
		"k3",
		"k3-256k",
		"moonshot-v1-128k",
		"gemini-3.0-pro",
		"grok-4",
		"qwen3-max",
		"minimax-m2.5",
		"llama-3.3-70b",
		"provider/deepseek-v4", // vendor/model 形式取最后一段判定。
		"provider/k3",          // Kimi Code bare ID 的 vendor/model 形式。
	}
	for _, model := range foreign {
		require.False(t, account.IsModelSupported(model), "expected %q to be rejected by empty-mapping OpenAI OAuth account", model)
	}
}

func TestIsModelSupported_OpenAIOAuthMappingKeepsForkSemantics(t *testing.T) {
	account := newOpenAIOAuthAccountForModelTest()
	account.Credentials = map[string]any{
		"model_mapping": map[string]any{"deepseek-v4": "gpt-5.4", "k3": "gpt-5.4"},
	}

	// fork 中映射是可选改写规则，没有独立白名单时不限制未命中的请求。
	require.True(t, account.IsModelSupported("deepseek-v4"))
	require.True(t, account.IsModelSupported("k3"))
	require.True(t, account.IsModelSupported("glm-4.7"))

	account.Credentials["model_whitelist"] = []any{"gpt-5.4"}
	require.True(t, account.IsModelSupported("deepseek-v4"))
	require.False(t, account.IsModelSupported("glm-4.7"))
}

func TestIsModelSupported_OpenAIOAuthEmptyMappingRespectsWhitelist(t *testing.T) {
	account := newOpenAIOAuthAccountForModelTest()
	account.Credentials = map[string]any{"model_whitelist": []any{"gpt-5.4", "deepseek-v4"}}

	// 最终白名单仍先限制允许范围，但不能让 Codex 上游无法服务的厂商模型绕过平台限制。
	require.True(t, account.IsModelSupported("gpt-5.4"))
	require.False(t, account.IsModelSupported("gpt-5.3-codex"))
	require.False(t, account.IsModelSupported("deepseek-v4"))
}

func TestIsModelSupported_OpenAIOAuthPassthroughAllowsAll(t *testing.T) {
	account := newOpenAIOAuthAccountForModelTest()
	account.Extra = map[string]any{"openai_passthrough": true}

	// 透传模式仅替换认证，模型语义由上游决定，保持“允许所有”。
	require.True(t, account.IsModelSupported("deepseek-v4"))
	require.True(t, account.IsModelSupported("claude-sonnet-4-6"))
}

func TestIsModelSupported_OpenAIAPIKeyEmptyMappingAllowsAll(t *testing.T) {
	account := &Account{
		ID:       2,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
	}

	// API Key 账号的第三方 OpenAI 兼容上游可服务任意别名，语义不变。
	require.True(t, account.IsModelSupported("deepseek-v4"))
	require.True(t, account.IsModelSupported("gpt-5.4"))
}

func TestIsModelSupported_NonOpenAIPlatformsUnchanged(t *testing.T) {
	anthropic := &Account{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	require.True(t, anthropic.IsModelSupported("claude-sonnet-4-6"))
	require.True(t, anthropic.IsModelSupported("deepseek-v4"))
}

func TestIsOpenAIOAuthServableModel(t *testing.T) {
	supports := newOpenAIOAuthAccountForModelTest().isOpenAIOAuthServableModel
	require.True(t, supports("gpt-5.4-high"))
	require.True(t, supports("  gpt-5.3-codex  "))
	require.False(t, supports("claude-3-5-haiku-20241022"))
	require.True(t, supports("DeepThink-x"))  // 非黑名单前缀，保持允许。
	require.False(t, supports("DeepSeek-V4")) // 大小写不敏感。
	require.False(t, supports("qwen3-235b-thinking"))
	require.False(t, supports("k3"))
	require.False(t, supports("k3-256k"))
	require.False(t, supports("provider/k3"))
	require.True(t, supports("my-k3-alias"))   // 自定义别名继续 fail-open。
	require.True(t, supports("deepseekcoder")) // 无连字符时不匹配黑名单前缀。
}

// 普通 OAuth 只接手映射后可以发给 Codex 的模型；同组 API Key 及显式透传继续独立判断。
func TestOpenAIOAuthClaudeRoutingUsesFinalTarget(t *testing.T) {
	const claude = "claude-sonnet-4-6"
	const gpt = "gpt-5.4"
	account := newOpenAIOAuthAccountForModelTest()
	account.Status, account.Schedulable = StatusActive, true
	account.Credentials = map[string]any{
		"model_mapping":         map[string]any{"team-claude": claude, "claude-as-gpt": gpt},
		"compact_model_mapping": map[string]any{claude: gpt},
	}
	scheduler := &defaultOpenAIAccountScheduler{}
	for _, tc := range []struct {
		request, routing string
		compact, want    bool
	}{
		{claude, claude, false, false},
		{claude, gpt, false, true},
		{"team-claude", "team-claude", false, false},
		{"claude-as-gpt", "claude-as-gpt", false, true},
		{claude, claude, true, true},
	} {
		req := OpenAIAccountScheduleRequest{RequestedModel: tc.request, RoutingModel: tc.routing, RequireCompact: tc.compact}
		ok, _ := scheduler.isAccountRequestCompatibleReason(t.Context(), account, req)
		require.Equal(t, tc.want, ok, "%+v", tc)
	}
	// compact 全局或账号回退目标也必须合格，避免请求进入第二次转发时才选到 Claude。
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{OpenAICompactModel: claude}}}
	require.Empty(t, svc.resolveOpenAICompactFallbackModel(account, gpt))
	require.Equal(t, gpt, svc.resolveOpenAICompactFallbackModel(account, claude))
	account.Credentials = map[string]any{"model_whitelist": []any{claude}}
	require.False(t, account.IsModelSupported(claude))
	account.Type = AccountTypeAPIKey
	require.True(t, account.IsModelSupported(claude))
	for _, typ := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		account.Type = typ
		account.Extra = map[string]any{"openai_passthrough": true, "openai_passthrough_model_filter": true}
		require.True(t, account.IsModelSupported(claude))
	}
}

// 混合账号池中，同一个外部名称可以由 API Key 承接，有显式 GPT 映射时也可由普通 OAuth 承接。
func TestOpenAIOAuthClaudeMixedPoolSelection(t *testing.T) {
	const alias = "claude-sonnet-4-6"
	accounts := []Account{
		{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Priority: 1},
		{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 2},
	}
	selectFrom := func(pool []Account) *Account {
		t.Helper()
		svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: pool}}
		got, err := svc.SelectAccountForModel(t.Context(), nil, "", alias)
		require.NoError(t, err)
		require.NotNil(t, got)
		return got
	}
	require.Equal(t, int64(12), selectFrom(accounts).ID)
	accounts[0].Credentials = map[string]any{"model_mapping": map[string]any{alias: "gpt-5.4"}}
	require.Equal(t, int64(11), selectFrom(accounts[:1]).ID)
}
