package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRemoveDataSharingMigrationContract 锁定数据共享的表、列、设置与备份字段清理边界。
func TestRemoveDataSharingMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("265_remove_data_sharing.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	for _, fragment := range []string{
		"drop table if exists data_share_export_artifacts",
		"drop table if exists data_share_sessions",
		"drop index if exists idx_groups_data_sharing_enabled",
		"drop column if exists data_sharing_enabled",
		"drop index if exists idx_api_keys_data_sharing_confirmed_group_id",
		"drop column if exists data_sharing_notice_version",
		"drop column if exists data_sharing_confirmed_group_id",
		"drop column if exists data_sharing_confirmed_at",
		"backup_json - 'include_data_share_sessions'",
		"when data_exception or program_limit_exceeded",
	} {
		require.Contains(t, sql, fragment)
	}
	require.NotContains(t, sql, "when others")

	for _, key := range []string{
		"data_sharing_enabled",
		"data_sharing_notice_content",
		"data_sharing_notice_version",
		"data_sharing_capture_skip_rules",
		"data_sharing_export_ticket_key",
		"data_sharing_export_remote_config",
		"data_sharing_export_remote_prefix",
		"data_sharing_storage_limit_bytes",
		"data_sharing_capture_runtime",
	} {
		require.Contains(t, sql, "'"+key+"'")
	}
}
