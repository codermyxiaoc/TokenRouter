//go:build unit

package service

import (
	"context"
	"github.com/TokenFlux/TokenRouter/internal/pkg/xai"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

// 裸模型解析、真实转发模型与限流键必须相同，显式映射仍具有最高优先级。
func TestV028AntigravityFinalModelKeyUsesThinkingBudget(t *testing.T) {
	account := newAntigravityAccountWithMapping(map[string]string{"gemini-3.8-flash-high": "gemini-3.8-flash-high", "gemini-3.8-flash-medium": "gemini-3.8-flash-medium", "gemini-3.8-flash-low": "gemini-3.8-flash-low"})
	for _, tc := range []struct{ body, level string }{
		{`{}`, "high"}, {`{"thinking":{"type":"enabled","budget_tokens":4000}}`, "medium"},
		{`{"thinking":{"type":"disabled"}}`, "low"}, {`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"low"}}}`, "low"},
	} {
		ctx := WithAntigravityThinkingLevelFromBody(context.Background(), []byte(tc.body))
		want := "gemini-3.8-flash-" + tc.level
		require.Equal(t, want, resolveFinalAntigravityModelKey(ctx, account, "gemini-3.8-flash"))
		require.Contains(t, account.modelRateLimitKeysForRequest(ctx, "gemini-3.8-flash"), want)
	}
	explicit := newAntigravityAccountWithMapping(map[string]string{"gemini-*": "custom", "gemini-3.8-flash-high": "gemini-3.8-flash-high"})
	require.Equal(t, "custom", resolveFinalAntigravityModelKey(context.Background(), explicit, "gemini-3.8-flash"))
}

func TestV028GeminiMixedCatalogOnlyListsGeminiModels(t *testing.T) {
	account := newAntigravityAccountWithMapping(map[string]string{"gemini-3.8-flash-high": "gemini-3.8-flash-high", "claude-sonnet-4-6": "claude-sonnet-4-6"})
	account.Extra = map[string]any{"mixed_scheduling": true}
	require.True(t, account.IsMixedSchedulingEnabled())
	result := configuredRequestModelsFromAccounts([]Account{*account}, PlatformGemini)
	require.Contains(t, result, "gemini-3.8-flash-high")
	require.NotContains(t, result, "claude-sonnet-4-6")
}

func TestV028Grok47PricingIndependentFromDefaults(t *testing.T) {
	service := NewBillingService(nil, nil)
	legacy, _ := service.GetModelPricing("grok-4.6")
	price, err := service.GetModelPricing("grok-4.7-latest")
	require.NoError(t, err)
	require.NotSame(t, legacy, price)
	require.Equal(t, 2e-6, price.InputPricePerToken)
	require.Equal(t, 6e-6, price.OutputPricePerToken)
	require.Equal(t, 0.5e-6, price.CacheReadPricePerToken)
	require.Equal(t, 200000, price.LongContextInputThreshold)
	require.True(t, price.LongContextThresholdInclusive)
	require.Equal(t, "grok-4.6", xai.DefaultTextModel)
	require.Equal(t, "grok-4.7", xai.NormalizeModelID("grok-4.7-latest"))
}

func TestV028GeminiBackoffCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	require.ErrorIs(t, sleepGeminiBackoff(ctx, 3), context.Canceled)
	require.Less(t, time.Since(start), time.Second)
}
