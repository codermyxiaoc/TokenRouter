package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSmartRoutingSeedanceRequiresExplicitCapability(t *testing.T) {
	for _, endpoint := range []string{"/api/v3/contents/generations/tasks", "/v3/contents/generations/tasks", "/v1/contents/generations/tasks", "/contents/generations/tasks"} {
		for _, platform := range []string{PlatformOpenAI, PlatformGrok, PlatformAnthropic, PlatformGemini, PlatformMiniMax} {
			require.Equal(t, platform == PlatformOpenAI, smartRoutingGroupEndpointEligible(platform, endpoint))
		}
		account := smartRoutingTestAccount(PlatformOpenAI)
		account.Credentials["base_url"] = "https://ark.cn-beijing.volces.com/api/v3"
		require.False(t, smartRoutingAccountEndpointEligible(context.Background(), &account, "shared-model", endpoint))
		account.Credentials[openAIWorkloadCapabilitiesCredentialKey] = []string{"seedance"}
		require.True(t, smartRoutingAccountEndpointEligible(context.Background(), &account, "shared-model", endpoint))
		account.Type = AccountTypeOAuth
		require.False(t, smartRoutingAccountEndpointEligible(context.Background(), &account, "shared-model", endpoint))
	}
}
