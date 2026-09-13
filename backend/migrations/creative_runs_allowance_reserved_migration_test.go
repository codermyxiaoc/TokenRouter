package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCreativeRunsAllowanceReservedMigration 校验创作台额度预记标记迁移：
// creative_runs.allowance_reserved 与 batch_image_jobs.allowance_reserved 同语义。
func TestCreativeRunsAllowanceReservedMigration(t *testing.T) {
	content, err := FS.ReadFile("254_creative_runs_allowance_reserved.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// 新增额度预记标记列，缺省未预记。
	require.Contains(t, sql, "alter table creative_runs add column if not exists allowance_reserved boolean not null default false")
}
