package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGeneralizeAdvancedSchedulerMigration 锁定调度器类型约束的目标表与类型。
func TestGeneralizeAdvancedSchedulerMigration(t *testing.T) {
	content, err := FS.ReadFile("238_generalize_advanced_scheduler.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	require.Contains(t, sql, "groups_scheduler_type_check")
	require.Contains(t, sql, "conrelid = 'groups'::regclass")
	require.Contains(t, sql, "contype = 'c'")
	require.Contains(t, sql, "check (scheduler_type in ('basic', 'advanced'))")
}
