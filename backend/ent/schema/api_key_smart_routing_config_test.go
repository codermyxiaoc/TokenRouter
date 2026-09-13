package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSmartRoutingCooldownSchema(t *testing.T) {
	// Ent 独立写入路径同样遵守数据库与 API 的默认值和范围。
	for _, schemaField := range (APIKey{}).Fields() {
		descriptor := schemaField.Descriptor()
		if descriptor.Name != "smart_routing_cooldown_seconds" {
			continue
		}
		require.Equal(t, 60, descriptor.Default)
		for _, seconds := range []int{-1, 0, 60, 3600, 3601} {
			valid := true
			for _, validator := range descriptor.Validators {
				validate, ok := validator.(func(int) error)
				require.True(t, ok)
				valid = validate(seconds) == nil && valid
			}
			require.Equal(t, seconds >= 0 && seconds <= 3600, valid)
		}
		return
	}
	t.Fatal("missing smart routing cooldown field")
}
