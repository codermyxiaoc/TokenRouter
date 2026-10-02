package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserPlatformQuotaVideoValidator(t *testing.T) {
	// 生成的 runtime 读取 schema 的校验器，新增值必须在真实 schema 中被接受。
	for _, field := range (UserPlatformQuota{}).Fields() {
		if field.Descriptor().Name != "platform" {
			continue
		}
		for _, validator := range field.Descriptor().Validators {
			require.NoError(t, validator.(func(string) error)("video"))
		}
		return
	}
	t.Fatal("missing platform field")
}
