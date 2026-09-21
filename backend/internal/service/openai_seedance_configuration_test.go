//go:build unit

package service

import (
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/openai_compat"
	"github.com/stretchr/testify/require"
)

// 新旧客户端均可显式保存视频能力，默认账号不自动获得付费视频入口。
func TestSeedanceWorkloadConfigurationNormalization(t *testing.T) {
	for _, key := range []string{openAIWorkloadCapabilitiesCredentialKey, legacyOpenAICapabilitiesCredentialKey} {
		t.Run(key, func(t *testing.T) {
			credentials := map[string]any{key: []any{"seedance", "text_generation", "seedance"}}
			require.NoError(t, normalizeOpenAIWorkloadCapabilities(credentials, true))
			require.Equal(t, []string{"text_generation", "seedance"}, credentials[openAIWorkloadCapabilitiesCredentialKey])
			require.NotContains(t, credentials, legacyOpenAICapabilitiesCredentialKey)
		})
	}
	credentials := map[string]any{}
	require.NoError(t, normalizeOpenAIWorkloadCapabilities(credentials, true))
	require.Equal(t, []string{"text_generation", "embeddings"}, credentials[openAIWorkloadCapabilitiesCredentialKey])
}

// 批量编辑允许纯视频账号，但不能为其强制设置文本路由。
func TestSeedanceBulkWorkloadValidation(t *testing.T) {
	includeText, err := validateBulkOpenAIWorkloadCapabilities([]any{"seedance"})
	require.NoError(t, err)
	require.False(t, includeText)
	_, err = validateBulkOpenAIWorkloadCapabilities([]any{"seedance", "unknown"})
	require.Error(t, err)
	input := &BulkUpdateAccountsInput{Credentials: map[string]any{openAIWorkloadCapabilitiesCredentialKey: []string{"seedance"}}}
	_, err = normalizeBulkOpenAISettings(input)
	require.NoError(t, err)
	require.Equal(t, string(openai_compat.TextRouteModePreserveClientProtocol), input.Extra[openai_compat.ExtraKeyTextRouteMode])
	input.Extra[openai_compat.ExtraKeyTextRouteMode] = string(openai_compat.TextRouteModeForceResponses)
	_, err = normalizeBulkOpenAISettings(input)
	require.Error(t, err)
}
