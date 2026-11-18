//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// 组级强制档位优先级：Ultra Fast（仅支持的模型）> Fast > 客户端。
func TestOpenAIGroupForcedServiceTier(t *testing.T) {
	account := &Account{ID: 1, Platform: PlatformOpenAI}
	withGroup := func(fast, ultra bool) context.Context {
		return context.WithValue(context.Background(), ctxkey.Group, &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, ForceOpenAIFast: fast, ForceOpenAIUltrafast: ultra})
	}
	for _, tc := range []struct {
		name        string
		fast, ultra bool
		model, want string
	}{
		{"ultra on supported Sol", false, true, "gpt-6.1-sol", OpenAIFastTierUltrafast},
		{"ultra on supported Astra", false, true, "gpt-6-astra", OpenAIFastTierUltrafast},
		{"ultra only unsupported follows client", false, true, "gpt-6-luna", ""},
		{"both unsupported falls back to fast", true, true, "gpt-6-luna", OpenAIFastTierPriority},
		{"both supported prefers ultra", true, true, "gpt-6.1-sol", OpenAIFastTierUltrafast},
		{"fast only", true, false, "gpt-6.1-sol", OpenAIFastTierPriority},
		{"none", false, false, "gpt-6.1-sol", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, openAIGroupForcedServiceTier(withGroup(tc.fast, tc.ultra), account, tc.model))
		})
	}
	// 非 OpenAI 账号或缺少可信分组上下文时不强制。
	require.Empty(t, openAIGroupForcedServiceTier(withGroup(true, true), &Account{Platform: PlatformAnthropic}, "gpt-6.1-sol"))
	require.Empty(t, openAIGroupForcedServiceTier(context.Background(), account, "gpt-6.1-sol"))
}

// GPT-6.1 Sol 的 Ultrafast 与 Astra 一致按 6 倍计费，其它 GPT-6 模型不提供 Ultrafast 倍率。
func TestOpenAIUltrafastMultiplierForSupportedModels(t *testing.T) {
	svc := NewBillingService(&config.Config{}, nil)
	for model, want := range map[string]float64{"gpt-6.1-sol": 6, "gpt-6-astra": 6, "gpt-6-luna": 0, "gpt-6-sol": 0} {
		p, err := svc.GetModelPricing(model)
		require.NoError(t, err, model)
		require.Equal(t, want, p.UltrafastMultiplier, model)
	}
}
