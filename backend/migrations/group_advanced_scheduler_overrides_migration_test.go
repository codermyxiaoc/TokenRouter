package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGroupAdvancedSchedulerOverridesMigration 锁定分组覆盖列的默认值、类型与约束。
func TestGroupAdvancedSchedulerOverridesMigration(t *testing.T) {
	content, err := FS.ReadFile("239_add_group_advanced_scheduler_overrides.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	require.Contains(t, sql, "add column if not exists advanced_scheduler_overrides jsonb not null default '{}'::jsonb")
	require.Contains(t, sql, "groups_advanced_scheduler_overrides_object_check")
	require.Contains(t, sql, "conrelid = 'groups'::regclass")
	require.Contains(t, sql, "contype = 'c'")
	require.Contains(t, sql, "check (jsonb_typeof(advanced_scheduler_overrides) = 'object')")
	require.Contains(t, sql, "分组高级调度器稀疏覆盖")
}
