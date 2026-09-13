package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRegistrationEmailNormalizationIndexMigration 固定非事务索引及供应商感知归一化规则。
func TestRegistrationEmailNormalizationIndexMigration(t *testing.T) {
	content, err := FS.ReadFile("220_users_registration_email_normalized_index_notx.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))
	require.Contains(t, sql, "create index concurrently if not exists idx_users_registration_email_normalized")
	require.Contains(t, sql, "googlemail.com")
	require.Contains(t, sql, "rtrim(split_part(lower(btrim(email)), '@', 2), '.')")
	require.Contains(t, sql, "strpos(split_part(lower(btrim(email)), '@', 1), '+') > 1")
	require.Contains(t, sql, "where deleted_at is null")
}
