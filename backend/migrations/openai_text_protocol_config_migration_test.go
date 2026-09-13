package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpenAITextProtocolConfigMigration 锁定迁移范围、新字段和旧键清理契约。
func TestOpenAITextProtocolConfigMigration(t *testing.T) {
	sqlBytes, err := FS.ReadFile("247_migrate_openai_text_protocol_config.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(sqlBytes))

	require.Contains(t, sql, "where platform = 'openai'")
	require.Contains(t, sql, "and type = 'apikey'")
	require.Contains(t, sql, "openai_workload_capabilities")
	require.Contains(t, sql, "openai_text_route_mode")
	require.Contains(t, sql, "openai_responses_probe_status")
	require.Contains(t, sql, "- 'openai_capabilities'")
	require.Contains(t, sql, "- 'openai_responses_mode' - 'openai_responses_supported'")
	require.Contains(t, sql, "drop function if exists")
}
