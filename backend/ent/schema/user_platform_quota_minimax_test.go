package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserPlatformQuotaMiniMaxValidator(t *testing.T) {
	// Ent 独立校验也要允许 MiniMax，不能只有服务 allowlist 与 SQL 接受该平台。
	for _, schemaField := range (UserPlatformQuota{}).Fields() {
		descriptor := schemaField.Descriptor()
		if descriptor.Name != "platform" {
			continue
		}
		rejectedUnknown := false
		for _, validator := range descriptor.Validators {
			validate, ok := validator.(func(string) error)
			require.True(t, ok)
			require.NoError(t, validate("minimax"))
			if validate("unknown") != nil {
				rejectedUnknown = true
			}
		}
		require.True(t, rejectedUnknown)
		return
	}
	t.Fatal("missing platform field")
}
