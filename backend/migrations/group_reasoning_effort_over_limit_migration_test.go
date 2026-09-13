package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGroupReasoningEffortOverLimitMigration 锁定新字段的类型、默认值和说明。
func TestGroupReasoningEffortOverLimitMigration(t *testing.T) {
	content, err := FS.ReadFile("263_group_reasoning_effort_over_limit.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS max_reasoning_effort_over_limit VARCHAR(20) NOT NULL DEFAULT 'downgrade'")
	require.Contains(t, sql, "COMMENT ON COLUMN groups.max_reasoning_effort_over_limit")
	require.Contains(t, sql, "deny 拒绝访问")
}
