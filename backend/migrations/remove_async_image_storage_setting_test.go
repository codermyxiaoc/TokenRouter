package migrations

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// TestRemoveAsyncImageStorageSettingMigration 约束迁移只清理已废弃的运行时设置。
func TestRemoveAsyncImageStorageSettingMigration(t *testing.T) {
	content, err := FS.ReadFile("234_remove_async_image_storage_setting.sql")
	require.NoError(t, err)

	var statements []string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		statements = append(statements, line)
	}

	require.Equal(t, []string{"DELETE FROM settings WHERE key = 'image_storage_config';"}, statements)
}

// TestRemoveAsyncImageStorageSettingMigrationPreservesBackupSettings 验证迁移只删除废弃键。
func TestRemoveAsyncImageStorageSettingMigrationPreservesBackupSettings(t *testing.T) {
	content, err := FS.ReadFile("234_remove_async_image_storage_setting.sql")
	require.NoError(t, err)

	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`)
	require.NoError(t, err)

	preserved := map[string]string{
		"backup_s3_config":      `{"bucket":"backups"}`,
		"backup_storage_config": `{"type":"s3"}`,
		"backup_content_config": `{"include_settings":true}`,
		"backup_schedule":       `{"enabled":true}`,
	}
	for key, value := range preserved {
		_, err = db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, key, value)
		require.NoError(t, err)
	}
	_, err = db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, "image_storage_config", `{"enabled":true}`)
	require.NoError(t, err)

	// DELETE 本身应可重复执行，确保迁移保持幂等。
	_, err = db.Exec(string(content))
	require.NoError(t, err)
	_, err = db.Exec(string(content))
	require.NoError(t, err)

	var removedCount int
	err = db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = ?`, "image_storage_config").Scan(&removedCount)
	require.NoError(t, err)
	require.Zero(t, removedCount)

	for key, wantValue := range preserved {
		var gotValue string
		err = db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&gotValue)
		require.NoError(t, err, "设置 %s 应保留", key)
		require.Equal(t, wantValue, gotValue)
	}
}
