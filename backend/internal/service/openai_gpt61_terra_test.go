package service

import (
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apicompat"
	"github.com/TokenFlux/TokenRouter/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

// GPT-6.1 Terra 与 6 Sol/Luna 共用别名边界：受支持的推理后缀（含 none）归入 Terra，未知后缀不归入任何定价。
func TestGPT61TerraIdentityAndEffort(t *testing.T) {
	require.Contains(t, openai.DefaultModelIDs(), "gpt-6.1-terra")
	for _, suffix := range []string{"", "-none", "-low", "-medium", "-high", "-xhigh", "-max", "-openai-compact", "-none-openai-compact"} {
		alias := "OPENAI/" + strings.ToUpper(strings.ReplaceAll("gpt-6.1-terra"+suffix, "-", "_"))
		require.True(t, isOpenAIGPT61TerraModel(alias), alias)
		require.False(t, isOpenAIGPT61SolModel(alias), alias)
		require.False(t, isOpenAIGPT6SolModel(alias), alias)
		require.Equal(t, "gpt-6.1-terra", normalizeKnownOpenAICodexModel(alias), alias)
		require.Equal(t, "gpt-6.1-terra", normalizeCodexModel(alias), alias)
		require.Contains(t, usageBillingModelCandidates(alias), "gpt-6.1-terra", alias)
	}
	for _, unknown := range []string{"gpt-6.1-terra-pro", "gpt-6.1-terra-ultra", "gpt-6.1-terra-minimal", "gpt-6.1-terra-preview", "gpt-6.1-terra-2026-10-27"} {
		require.False(t, isOpenAIGPT61TerraModel(unknown), unknown)
		require.Empty(t, normalizeKnownOpenAICodexModel(unknown), unknown)
		require.NotContains(t, usageBillingModelCandidates(unknown), "gpt-6.1-terra", unknown)
	}
	require.False(t, isOpenAIGPT61TerraModel("gpt-5.6-terra"))
	require.False(t, isOpenAIGPT61TerraModel("gpt-6.1-sol"))

	// Terra 支持关闭推理：none 原样保留，不被改成默认档位，也不像 6.1 Sol 那样被拒绝。
	for _, effort := range []string{"none", "low", "medium", "high", "xhigh", "max"} {
		require.True(t, openAIModelSupportsReasoningEffort("gpt-6.1-terra", effort), effort)
		request := &apicompat.AnthropicRequest{Model: "gpt-6.1-terra-" + effort}
		applyOpenAICompatModelNormalization(request)
		require.Equal(t, "gpt-6.1-terra", request.Model, effort)
		require.NotNil(t, request.OutputConfig, effort)
		converted, err := apicompat.AnthropicToResponses(request)
		require.NoError(t, err)
		// 与其它 GPT 型号一致：Claude 桥接把 xhigh 映射为 max。
		expected := effort
		if effort == "xhigh" {
			expected = "max"
		}
		require.Equal(t, expected, converted.Reasoning.Effort, effort)
	}
	require.True(t, shouldAutoInjectPromptCacheKeyForCompat("gpt-6.1-terra"))
}

// Terra 使用独立官方价，Fast 档按 2 倍，超过 272K 输入时按长上下文倍率计价，不借用 6.1 Sol 或 5.6 Terra 的价格。
func TestGPT61TerraPricing(t *testing.T) {
	billing := NewBillingService(nil, nil)
	pricing, err := billing.GetModelPricing("gpt-6.1-terra-high")
	require.NoError(t, err)
	require.InDelta(t, 1e-6, pricing.InputPricePerToken, 1e-15)
	require.InDelta(t, 5e-6, pricing.OutputPricePerToken, 1e-15)
	require.InDelta(t, 1.25e-6, pricing.CacheCreationPricePerToken, 1e-15)
	require.InDelta(t, 0.05e-6, pricing.CacheReadPricePerToken, 1e-15)
	require.InDelta(t, 2e-6, pricing.InputPricePerTokenPriority, 1e-15)
	require.InDelta(t, 10e-6, pricing.OutputPricePerTokenPriority, 1e-15)
	require.Equal(t, 272000, pricing.LongContextInputThreshold)
	require.InDelta(t, 2, pricing.LongContextInputMultiplier, 1e-9)
	require.InDelta(t, 1.5, pricing.LongContextOutputMultiplier, 1e-9)
	require.InDelta(t, 2.0, openAIModelFastPricingRatio("gpt-6.1-terra"), 1e-9)
	sol, err := billing.GetModelPricing("gpt-6.1-sol")
	require.NoError(t, err)
	require.NotEqual(t, sol.InputPricePerToken, pricing.InputPricePerToken)
	_, err = billing.GetModelPricing("gpt-6.1-terra-pro")
	require.Error(t, err, "未知变体不能按 Terra 或其它型号计价")
}
