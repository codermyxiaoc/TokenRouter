package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGroupFreeOpenAIFastMigration 锁定分组免费 Fast 字段的类型、默认值和用途说明。
func TestGroupFreeOpenAIFastMigration(t *testing.T) {
	content, err := FS.ReadFile("264_group_free_openai_fast.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS free_openai_fast BOOLEAN NOT NULL DEFAULT FALSE")
	require.Contains(t, sql, "COMMENT ON COLUMN groups.free_openai_fast")
}
