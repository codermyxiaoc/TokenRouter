//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
)

// 默认迁移只影响省略字段的新行，无法识别来源的旧60秒与用户显式值必须全部保留。
func TestMigration319ChangesOnlyFutureSmartRoutingDefaults(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	_, err := tx.ExecContext(ctx, `CREATE TEMP TABLE api_keys (id BIGSERIAL PRIMARY KEY, smart_routing_cooldown_seconds INTEGER NOT NULL DEFAULT 60 CHECK (smart_routing_cooldown_seconds BETWEEN 0 AND 3600)) ON COMMIT DROP`)
	require.NoError(t, err)
	for _, value := range []int{0, 10, 60, 120, 3600} {
		_, err := tx.ExecContext(ctx, `INSERT INTO api_keys (smart_routing_cooldown_seconds) VALUES ($1)`, value)
		require.NoError(t, err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO api_keys DEFAULT VALUES`)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("319_smart_routing_default_cooldown.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT smart_routing_cooldown_seconds FROM api_keys ORDER BY id`)
	require.NoError(t, err)
	var values []int
	for rows.Next() {
		var value int
		require.NoError(t, rows.Scan(&value))
		values = append(values, value)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, []int{0, 10, 60, 120, 3600, 60}, values)
	var defaultValue int
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO api_keys DEFAULT VALUES RETURNING smart_routing_cooldown_seconds`).Scan(&defaultValue))
	require.Equal(t, 10, defaultValue)
}
