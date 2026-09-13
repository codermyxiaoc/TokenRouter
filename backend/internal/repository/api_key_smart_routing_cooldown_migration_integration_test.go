//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestMigration271And272SmartRoutingCooldownDefaultsAndReplay(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	// 只操作集成测试容器中的临时表，覆盖旧记录升级而不修改真实 API Key 数据。
	_, err := tx.ExecContext(ctx, `CREATE TEMP TABLE api_keys (id BIGSERIAL PRIMARY KEY) ON COMMIT DROP`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO api_keys DEFAULT VALUES`)
	require.NoError(t, err)
	migrationSQL, err := migrations.FS.ReadFile("271_api_key_smart_routing_cooldown.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	// 271 保留已发布的按约束名判断；272 在临时表上补齐按目标表限定的约束。
	outboxSQL, err := migrations.FS.ReadFile("272_api_key_smart_routing_cooldown_outbox.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(outboxSQL))
	require.NoError(t, err)
	var value int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT smart_routing_cooldown_seconds FROM api_keys WHERE id = 1`).Scan(&value))
	require.Equal(t, 60, value)
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO api_keys DEFAULT VALUES RETURNING smart_routing_cooldown_seconds`).Scan(&value))
	require.Equal(t, 60, value)
	_, err = tx.ExecContext(ctx, `UPDATE api_keys SET smart_routing_cooldown_seconds = 3600 WHERE id = 1`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(outboxSQL))
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT smart_routing_cooldown_seconds FROM api_keys WHERE id = 1`).Scan(&value))
	require.Equal(t, 3600, value, "重复执行不能覆盖用户配置")
	for _, invalid := range []any{-1, 3601, nil} {
		_, err = tx.ExecContext(ctx, `SAVEPOINT invalid_cooldown`)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `INSERT INTO api_keys (smart_routing_cooldown_seconds) VALUES ($1)`, invalid)
		require.Error(t, err)
		_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT invalid_cooldown`)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `RELEASE SAVEPOINT invalid_cooldown`)
		require.NoError(t, err)
	}
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO api_keys (smart_routing_cooldown_seconds) VALUES (0) RETURNING smart_routing_cooldown_seconds`).Scan(&value))
	require.Zero(t, value)
}

func TestMigration272UpgradesPublished271WithoutChangingCooldownValues(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// 新建专用容器，从空数据库执行真实 runner，避免修改共享测试库或本地业务数据库。
	container, err := tcpostgres.Run(ctx, selectDockerImage(ctx, postgresImageTag),
		tcpostgres.WithDatabase("cooldown_upgrade_test"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := openSQLWithRetry(ctx, dsn, 30*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	const legacyName = "271_api_key_smart_routing_cooldown.sql"
	const outboxName = "272_api_key_smart_routing_cooldown_outbox.sql"
	const legacyChecksum = "c600119f5da453b541b264d51203e425ebe23ac89abe87b6e33d8f44d8d7a7f0"
	files, err := fs.Glob(migrations.FS, "*.sql")
	require.NoError(t, err)
	history := fstest.MapFS{}
	for _, filename := range files {
		if filename > legacyName {
			continue
		}
		content, err := migrations.FS.ReadFile(filename)
		require.NoError(t, err)
		history[filename] = &fstest.MapFile{Data: content}
	}
	require.NoError(t, applyMigrationsFS(ctx, db, history))
	var storedChecksum string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT checksum FROM schema_migrations WHERE filename = $1`, legacyName).Scan(&storedChecksum))
	require.Equal(t, legacyChecksum, storedChecksum)
	var userID int64
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO users (email, password_hash) VALUES ('cooldown-upgrade@example.com', 'test-hash') RETURNING id`).Scan(&userID))
	// 包含显式关闭和自定义时长，升级不能重置成默认 60。
	values := []int{0, 137, 3600}
	keyIDs := make([]int64, len(values))
	for index, value := range values {
		require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO api_keys (user_id, key, name, smart_routing, smart_routing_cooldown_seconds) VALUES ($1, $2, 'upgrade', TRUE, $3) RETURNING id`, userID, fmt.Sprintf("sk-cooldown-upgrade-%d", value), value).Scan(&keyIDs[index]))
	}
	outboxSQL, err := migrations.FS.ReadFile(outboxName)
	require.NoError(t, err)
	history[outboxName] = &fstest.MapFile{Data: outboxSQL}
	// 再次启动必须先接受原版 271 的已保存 checksum，再执行独立的 272。
	require.NoError(t, applyMigrationsFS(ctx, db, history))
	require.NoError(t, applyMigrationsFS(ctx, db, history))
	for index, expected := range values {
		var stored int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT smart_routing_cooldown_seconds FROM api_keys WHERE id = $1`, keyIDs[index]).Scan(&stored))
		require.Equal(t, expected, stored)
	}
	require.NoError(t, db.QueryRowContext(ctx, `SELECT checksum FROM schema_migrations WHERE filename = $1`, legacyName).Scan(&storedChecksum))
	require.Equal(t, legacyChecksum, storedChecksum)
	var outboxMigrations int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE filename = $1`, outboxName).Scan(&outboxMigrations))
	require.Equal(t, 1, outboxMigrations)
	_, err = db.ExecContext(ctx, `UPDATE api_keys SET smart_routing_cooldown_seconds = 45 WHERE id = $1`, keyIDs[0])
	require.NoError(t, err)
	sum := sha256.Sum256([]byte("sk-cooldown-upgrade-0"))
	var events int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_cache_invalidation_outbox WHERE cache_key = $1`, hex.EncodeToString(sum[:])).Scan(&events))
	require.Equal(t, 1, events, "272 必须保留冷却配置的持久化失效保障")
}
