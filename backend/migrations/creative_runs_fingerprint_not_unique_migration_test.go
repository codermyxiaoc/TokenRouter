package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCreativeRunsFingerprintNotUniqueMigration 校验创作台指纹唯一约束移除：
// 幂等只在 Idempotency-Key 级别保证，相同内容的再次提交不得永久冲突。
func TestCreativeRunsFingerprintNotUniqueMigration(t *testing.T) {
	content, err := FS.ReadFile("255_creative_runs_fingerprint_not_unique.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	require.Contains(t, sql, "alter table creative_runs drop constraint if exists creative_runs_request_fingerprint_key")
}
