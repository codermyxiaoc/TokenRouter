//go:build unit

package service

import (
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apicompat"
	"github.com/TokenFlux/TokenRouter/internal/pkg/claude"
	"github.com/stretchr/testify/require"
)

// Fable/Mythos 5.2 按官方价计费，必须先于宽泛的 Fable 5 匹配，Fast 档位按 2 倍。
func TestClaude52FallbackPricing(t *testing.T) {
	svc := NewBillingService(&config.Config{}, nil)
	tokens := UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000}
	for _, model := range []string{"claude-fable-5-2", "claude-mythos-5-2", "anthropic.claude-fable-5-2", "us.anthropic.claude-mythos-5-2", "claude-fable-5-2-20261109"} {
		cost, err := svc.CalculateCost(model, tokens, 1)
		require.NoError(t, err, model)
		require.InDelta(t, 8, cost.InputCost, 1e-9, model)
		require.InDelta(t, 40, cost.OutputCost, 1e-9, model)
		require.InDelta(t, 0.2, cost.CacheReadCost, 1e-9, model)
	}
	// Fable 5 / 5.1 价格不受影响。
	cost, err := svc.CalculateCost("claude-fable-5-1", UsageTokens{InputTokens: 1_000_000}, 1)
	require.NoError(t, err)
	require.InDelta(t, 10, cost.InputCost, 1e-9)

	pricing := &PricingService{pricingData: map[string]*LiteLLMModelPricing{}}
	require.Same(t, claude52FallbackPricing, pricing.GetModelPricing("claude-mythos-5-2"))
	require.True(t, claude52FallbackPricing.SupportsServiceTier)
}

// 5.2 只接受自适应思考与锁定的采样参数，签名思考历史与 5.5 同样保留。
func TestClaude52RequestConstraints(t *testing.T) {
	require.True(t, claude.IsClaude55SignedThinkingModel("claude-fable-5-2"))
	require.False(t, claude.IsClaude52Model("claude-fable-5-1"))
	for _, body := range []string{
		`{"thinking":{"type":"enabled","budget_tokens":1024}}`,
		`{"thinking":{"type":"between_tools"}}`,
		`{"temperature":0.5}`,
		`{"top_p":0.9}`,
		`{"top_k":5}`,
		`{"temperature":1,"top_p":0.99}`,
		`{"tool_choice":{"type":"any"}}`,
	} {
		require.Error(t, ValidateSonnet55Request([]byte(body), "claude-mythos-5-2"), body)
	}
	require.NoError(t, ValidateSonnet55Request([]byte(`{"temperature":1,"thinking":{"type":"adaptive"}}`), "claude-fable-5-2"))
	require.NoError(t, ValidateSonnet55Request([]byte(`{"temperature":0.5}`), "claude-fable-5-1"))

	out, err := apicompat.ResponsesToAnthropicRequest(&apicompat.ResponsesRequest{Model: "claude-fable-5-2", Input: []byte(`"hi"`)})
	require.NoError(t, err)
	require.Equal(t, "adaptive", out.Thinking.Type)
	require.Equal(t, "high", out.OutputConfig.Effort)
	out, err = apicompat.ResponsesToAnthropicRequest(&apicompat.ResponsesRequest{Model: "claude-fable-5-2", Input: []byte(`"hi"`), Reasoning: &apicompat.ResponsesReasoning{Effort: "none"}})
	require.NoError(t, err)
	require.Equal(t, "adaptive", out.Thinking.Type)
	require.Equal(t, "low", out.OutputConfig.Effort)
}
