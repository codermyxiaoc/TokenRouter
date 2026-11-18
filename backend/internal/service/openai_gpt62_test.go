//go:build unit

package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apicompat"
	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/TokenFlux/TokenRouter/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

// GPT-6.2 Sol/Astra 的模型识别、推理档位与兼容请求处理（同步自上游 v0.2.23，按分叉结构改写）。
func TestGPT62IdentityAndReasoningEffort(t *testing.T) {
	ids := make([]string, 0, len(openai.DefaultModels))
	for _, m := range openai.DefaultModels {
		ids = append(ids, m.ID)
	}
	for _, base := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		require.Contains(t, ids, base)
		for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "openai-compact"} {
			model := "openai/" + strings.ToUpper(base) + "-" + effort
			require.Equal(t, base, normalizeKnownOpenAICodexModel(model), model)
			require.Equal(t, base, openAIGPT62ModelID(model), model)
			require.True(t, shouldAutoInjectPromptCacheKeyForCompat(base), model)
			// 6.2 有独立身份，不能被误认为 6.1 Sol 或 GPT-6 Astra。
			require.False(t, isOpenAIGPT61SolModel(model), model)
			require.False(t, isOpenAIGPT6AstraModel(model), model)
			require.True(t, openAIModelSupportsUltrafast(model), model)
		}
		// none/minimal 不受支持；其余档位正常声明。
		for _, effort := range []string{"none", "minimal"} {
			require.False(t, openAIModelSupportsReasoningEffort(base, effort))
		}
		for _, effort := range []string{"low", "medium", "high", "xhigh"} {
			require.True(t, openAIModelSupportsReasoningEffort(base, effort))
		}
		// Codex 映射保留型号本身。
		require.Equal(t, base, normalizeCodexModel(base))
		// 模型后缀上的 none/minimal 保留给后续校验明确拒绝，不能静默回落到默认档位。
		for _, effort := range []string{"none", "minimal"} {
			_, derived, ok := splitOpenAICompatReasoningModel(base + "-" + effort)
			require.True(t, ok)
			require.Equal(t, effort, derived)
			req := &apicompat.AnthropicRequest{Model: base + "-" + effort}
			applyOpenAICompatModelNormalization(req)
			require.NotNil(t, req.OutputConfig)
			require.Equal(t, effort, req.OutputConfig.Effort)
		}
	}
	// 未公布的变体不归入 GPT-6.2，也不支持 Ultrafast。
	for _, model := range []string{"gpt-6.2", "gpt-6.2-foo", "gpt-6.2-sol-preview", "gpt-6.2-astra-law"} {
		require.Empty(t, normalizeKnownOpenAICodexModel(model), model)
		require.Empty(t, openAIGPT62ModelID(model), model)
		require.False(t, openAIModelSupportsUltrafast(model), model)
	}
}

// GPT-6.2 价格：Sol 与 6.1 Sol 相同，Astra 使用独立价卡；Ultrafast 在 2027-03-01 08:00 UTC 前为 3 倍，之后 6 倍。
func TestGPT62PricingAtRequestTime(t *testing.T) {
	before := time.Date(2027, 3, 1, 7, 59, 59, 0, time.UTC)
	after := before.Add(time.Second)
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		unit := 2e-6
		if model == "gpt-6.2-astra" {
			unit = 8e-6
		}
		for name, svc := range map[string]*BillingService{
			"fallback":         newTestBillingService(),
			"catalog fallback": NewBillingService(nil, newStubPricingServiceFromJSON(t, `{"other":{"input_cost_per_token":1}}`)),
		} {
			t.Run(model+"/"+name, func(t *testing.T) {
				for _, resolver := range []*ModelPricingResolver{nil, NewModelPricingResolver(nil, svc)} {
					input := CostInput{
						Model:          model,
						Tokens:         UsageTokens{InputTokens: 100, OutputTokens: 50, CacheCreationTokens: 40, CacheReadTokens: 20},
						RateMultiplier: 1, Resolver: resolver,
					}
					for _, tc := range []struct {
						tier  string
						at    time.Time
						ratio float64
					}{{"", before, 1}, {"priority", before, 2}, {"flex", before, 0.5}, {"ultrafast", before, 3}, {"ultrafast", after, 6}} {
						input.ServiceTier = tc.tier
						input.PricingAt = tc.at
						cost, err := svc.CalculateCostUnified(input)
						require.NoError(t, err)
						require.InDelta(t, 100*unit*tc.ratio, cost.InputCost, 1e-12, tc.tier)
						require.InDelta(t, 50*unit*5*tc.ratio, cost.OutputCost, 1e-12, tc.tier)
						require.InDelta(t, 40*unit*1.25*tc.ratio, cost.CacheCreationCost, 1e-12, tc.tier)
						require.InDelta(t, 20*unit*0.05*tc.ratio, cost.CacheReadCost, 1e-12, tc.tier)
					}
					// 272K 长上下文门槛：超过后输入 2 倍、输出 1.5 倍。
					input.ServiceTier = ""
					input.PricingAt = before
					input.Tokens = UsageTokens{InputTokens: 272000, OutputTokens: 50}
					cost, err := svc.CalculateCostUnified(input)
					require.NoError(t, err)
					require.InDelta(t, 272000*unit, cost.InputCost, 1e-12)
					input.Tokens.InputTokens++
					cost, err = svc.CalculateCostUnified(input)
					require.NoError(t, err)
					require.InDelta(t, 272001*unit*2, cost.InputCost, 1e-12)
					require.InDelta(t, 50*unit*5*1.5, cost.OutputCost, 1e-12)
				}
				// 按请求计算不得修改共享的回退价卡。
				require.Zero(t, svc.fallbackPrices[model].UltrafastMultiplier)
			})
		}
	}
	// 其它支持 Ultrafast 的型号始终 6 倍。
	for _, model := range []string{"gpt-6-astra", "gpt-6.1-sol"} {
		require.Equal(t, 6.0, openAIUltrafastMultiplier(model, before))
		require.Equal(t, 6.0, openAIUltrafastMultiplier(model, after))
	}
}

// 订阅账号的 GPT-6.2 上游成本与 6.1 Sol 一样豁免长上下文费用；API Key 账号不豁免。
func TestGPT62SubscriptionLongContextExempt(t *testing.T) {
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		result := &OpenAIForwardResult{Model: "public", UpstreamModel: model}
		require.True(t, openAISubscriptionLongContextExempt(&Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, result))
		require.False(t, openAISubscriptionLongContextExempt(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, result))
	}
}

// 组级强制 Ultra Fast 对 GPT-6.2：API Key 账号直接生效；订阅账号按套餐判定资格
// （Pro 只含 Sol，Pro Max 两个都含），无资格时回落到强制 Fast 或跟随客户端。
func TestGPT62SubscriptionUltrafastEntitlement(t *testing.T) {
	withGroup := func(fast bool) context.Context {
		return context.WithValue(context.Background(), ctxkey.Group, &Group{
			ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true,
			ForceOpenAIFast: fast, ForceOpenAIUltrafast: true,
		})
	}
	apiKey := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	for _, base := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		require.Equal(t, OpenAIFastTierUltrafast, openAIGroupForcedServiceTier(withGroup(false), apiKey, base))
	}
	for _, tc := range []struct {
		plan, model string
		want        bool
	}{
		{"pro", "gpt-6.2-sol", true},
		{"pro", "gpt-6.2-astra", false},
		{"chatgpt_pro", "gpt-6.2-sol", true},
		{"ChatGPT Pro", "gpt-6.2-sol", true},
		{"promax", "gpt-6.2-astra", true},
		{"pro-max", "gpt-6.2-sol", true},
		{"prolite", "gpt-6.2-sol", false},
		{"plus", "gpt-6.2-sol", false},
		{"", "gpt-6.2-astra", false},
	} {
		account := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"plan_type": tc.plan}}
		require.Equal(t, tc.want, gpt62PlanIncludesUltrafast(account, tc.model), tc.plan+"/"+tc.model)
		want := ""
		if tc.want {
			want = OpenAIFastTierUltrafast
		}
		require.Equal(t, want, openAIGroupForcedServiceTier(withGroup(false), account, tc.model), tc.plan+"/"+tc.model)
		// 无资格时，同时开启的强制 Fast 仍然生效。
		wantWithFast := OpenAIFastTierPriority
		if tc.want {
			wantWithFast = OpenAIFastTierUltrafast
		}
		require.Equal(t, wantWithFast, openAIGroupForcedServiceTier(withGroup(true), account, tc.model), tc.plan+"/"+tc.model)
	}
	// 订阅资格判定不影响 GPT-6 Astra / 6.1 Sol 的既有行为。
	sub := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"plan_type": "plus"}}
	for _, model := range []string{"gpt-6-astra", "gpt-6.1-sol"} {
		require.Equal(t, OpenAIFastTierUltrafast, openAIGroupForcedServiceTier(withGroup(false), sub, model))
	}
}
