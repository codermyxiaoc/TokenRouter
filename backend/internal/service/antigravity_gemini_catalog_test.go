package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

// 智能路由的原生端点资格与最终映射一致，不让 Claude 因别名以 Gemini 开头而通过。
func TestAntigravityGeminiSmartRoutingEndpointEligibility(t *testing.T) {
	a := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{"model_mapping": map[string]any{"friendly": "gemini-4-argon-high", "gemini-fake": "claude-opus-4-6"}}}
	for _, path := range []string{"/v1beta/models/friendly:generateContent", "/v1beta/models/friendly:streamGenerateContent", "/antigravity/v1beta/models/friendly:generateContent"} {
		require.True(t, smartRoutingGroupEndpointEligible(PlatformAntigravity, path))
		require.True(t, smartRoutingAccountEndpointEligible(context.Background(), a, "friendly", path))
		require.False(t, smartRoutingAccountEndpointEligible(context.Background(), a, "gemini-fake", path))
	}
	require.False(t, smartRoutingGroupEndpointEligible(PlatformOpenAI, "/v1beta/models/friendly:generateContent"))
}
