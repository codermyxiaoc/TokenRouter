package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAccountGroupPriorityRemovalMigration 锁定关联优先级列与专用索引的清理边界。
func TestAccountGroupPriorityRemovalMigration(t *testing.T) {
	content, err := FS.ReadFile("240_remove_account_group_priority.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	require.Contains(t, sql, "drop index if exists idx_account_groups_priority")
	require.Contains(t, sql, "drop index if exists idx_account_groups_group_priority_account")
	require.Contains(t, sql, "drop index if exists idx_account_groups_account_priority_group")
	require.Contains(t, sql, "drop column if exists priority")
}
