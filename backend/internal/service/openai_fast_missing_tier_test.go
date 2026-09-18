package service

import (
	"context"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 缺省档位是显式选择的新策略，不能扩大旧规则或绕过当前 Key、用户和平台约束。
func TestOpenAIFastMissingTierPreservesForkPolicies(t *testing.T) {
	missing := OpenAIFastPolicyRule{ServiceTier: OpenAIFastTierMissing, Action: OpenAIFastPolicyActionForcePriority, Scope: BetaPolicyScopeAll}
	legacy := missing
	legacy.ServiceTier = OpenAIFastTierAny
	filter := OpenAIFastPolicyRule{ServiceTier: OpenAIFastTierPriority, Action: BetaPolicyActionFilter, Scope: BetaPolicyScopeAll}
	block := filter
	block.Action = BetaPolicyActionBlock
	for _, test := range []struct {
		name        string
		body        string
		rules       []OpenAIFastPolicyRule
		platform    string
		keyPolicy   string
		wantTier    string
		wantBlocked bool
	}{
		{name: "default stays unset"},
		{name: "legacy all stays unset", rules: []OpenAIFastPolicyRule{legacy}},
		{name: "missing forces priority", rules: []OpenAIFastPolicyRule{missing}, wantTier: OpenAIFastTierPriority},
		{name: "explicit default stays default", rules: []OpenAIFastPolicyRule{missing}, body: `,"service_tier":"default"`, wantTier: "default"},
		{name: "explicit flex stays flex", rules: []OpenAIFastPolicyRule{missing}, body: `,"service_tier":"flex"`, wantTier: OpenAIFastTierFlex},
		{name: "null uses missing semantics", rules: []OpenAIFastPolicyRule{missing}, body: `,"service_tier":null`, wantTier: OpenAIFastTierPriority},
		{name: "key force off removes injected tier", rules: []OpenAIFastPolicyRule{missing}, keyPolicy: APIKeyFastModePolicyForceOff},
		{name: "system filter still applies", rules: []OpenAIFastPolicyRule{missing, filter}},
		{name: "system block still applies", rules: []OpenAIFastPolicyRule{missing, block}, wantBlocked: true},
		{name: "other platform untouched", rules: []OpenAIFastPolicyRule{missing}, platform: PlatformGrok},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{Rules: test.rules})
			platform := test.platform
			if platform == "" {
				platform = PlatformOpenAI
			}
			account := &Account{Platform: platform, Type: AccountTypeAPIKey}
			ctx := withAPIKeyFastModePolicy(context.Background(), test.keyPolicy)
			body := []byte(`{"type":"response.create","model":"gpt-5.5","input":"test"` + test.body + `}`)
			original := string(body)
			result, err := svc.applyOpenAIFastPolicyToBody(ctx, account, "gpt-5.5", body)
			if test.wantBlocked {
				var blocked *OpenAIFastBlockedError
				require.ErrorAs(t, err, &blocked)
			} else {
				require.NoError(t, err)
				require.Equal(t, test.wantTier, gjson.GetBytes(result, "service_tier").String())
			}
			frame, blocked, wsErr := svc.applyOpenAIFastPolicyToWSResponseCreate(ctx, account, "gpt-5.5", body)
			require.NoError(t, wsErr)
			require.Equal(t, test.wantBlocked, blocked != nil)
			if !test.wantBlocked {
				require.Equal(t, test.wantTier, gjson.GetBytes(frame, "service_tier").String())
			}
			require.Equal(t, original, string(body), "不得改写重试保留的原始请求")
		})
	}
}

func TestOpenAIFastMissingTierMatchesUserModelAndAccount(t *testing.T) {
	rule := OpenAIFastPolicyRule{ServiceTier: OpenAIFastTierMissing, Action: OpenAIFastPolicyActionForcePriority,
		Scope: BetaPolicyScopeOAuth, UserIDs: []int64{42}, ModelWhitelist: []string{"gpt-5.5"}, FallbackAction: BetaPolicyActionPass}
	svc := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{Rules: []OpenAIFastPolicyRule{rule}})
	for _, test := range []struct {
		name, model, accountType string
		userID                   int64
		want                     bool
	}{
		{"matching", "gpt-5.5", AccountTypeOAuth, 42, true},
		{"other user", "gpt-5.5", AccountTypeOAuth, 43, false},
		{"other model", "gpt-4.1", AccountTypeOAuth, 42, false},
		{"other account type", "gpt-5.5", AccountTypeAPIKey, 42, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), ctxkey.UserID, test.userID)
			require.Equal(t, test.want, svc.shouldForceOpenAIFastPriorityForMissingTier(ctx,
				&Account{Platform: PlatformOpenAI, Type: test.accountType}, test.model))
		})
	}
	// 新条件必须能被管理设置保存，不能只在转发代码中可用。
	require.NoError(t, svc.settingService.SetOpenAIFastPolicySettings(context.Background(), &OpenAIFastPolicySettings{Rules: []OpenAIFastPolicyRule{rule}}))
}
