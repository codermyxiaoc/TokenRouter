package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCreativeRunsWorkspaceMigration 校验创作台任务的浏览器工作区隔离迁移。
func TestCreativeRunsWorkspaceMigration(t *testing.T) {
	content, err := FS.ReadFile("256_creative_runs_workspace.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	require.Contains(t, sql, "alter table creative_runs add column if not exists workspace_id varchar(64)")
	require.Contains(t, sql, "drop index if exists creative_runs_idempotency_key_uq")
	require.Contains(t, sql, "create unique index if not exists creative_runs_user_workspace_idempotency_key_uq")
	require.Contains(t, sql, "on creative_runs (user_id, workspace_id, idempotency_key)")
	require.Contains(t, sql, "workspace_id is not null")
	require.Contains(t, sql, "create index if not exists creative_runs_user_workspace_created_at_idx")
}
