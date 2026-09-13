package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserPlatformQuotasMiniMaxMigration(t *testing.T) {
	// 平台扩展必须包含全部旧平台，防止新用户默认额度的多行插入被约束整体拒绝。
	content, err := FS.ReadFile("270_allow_minimax_user_platform_quotas.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check")
	require.Contains(t, sql, "CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'qoder', 'grok', 'kimi', 'zhipu', 'deepseek', 'minimax'))")
}
