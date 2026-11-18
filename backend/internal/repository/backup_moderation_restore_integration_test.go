//go:build integration

package repository

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os/exec"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// 恢复测试需要与本机 pg_dump 主版本一致的服务端，因此不复用 PG 18 的共享夹具，单独启动 PG 17。
const backupRestorePostgresImage = "postgres:17-alpine"

// 备份范围中受开关控制的快照表；运维组控制请求快照，审计组统一控制审核三表。
var backupRestoreScopedTables = []string{
	"public.ops_request_details",
	"public.content_moderation_logs",
	"public.content_moderation_media",
	"public.content_moderation_cyber_warnings",
}

// 用真实 pg_dump/psql 验证：开启时审核父子记录完整恢复；关闭时表结构存在但数据为空，其余业务数据不受影响。
func TestBackupRestoreModerationSnapshotScope(t *testing.T) {
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Skip("pg_dump not installed")
	}
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(ctx, backupRestorePostgresImage,
		tcpostgres.WithDatabase("backup_source"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(), loopbackPortBinding("5432/tcp"),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	host, err := backupRestoreContainerHost(ctx, container)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	dumperFor := func(dbName string) *PgDumper {
		return &PgDumper{cfg: &config.DatabaseConfig{
			Host: host, Port: port.Int(), User: "postgres", Password: "postgres", DBName: dbName, SSLMode: "disable",
		}}
	}
	open := func(dbName string) *sql.DB {
		db, err := sql.Open("postgres", fmt.Sprintf("host=%s port=%d user=postgres password=postgres dbname=%s sslmode=disable", host, port.Int(), dbName))
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		return db
	}

	source := open("backup_source")
	require.NoError(t, ApplyMigrations(ctx, source))
	seedBackupScopeData(t, ctx, source)

	cases := []struct {
		name     string
		excluded []string
	}{
		{"enabled", nil},
		{"disabled", backupRestoreScopedTables},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := "restore_" + tc.name
			_, err := source.ExecContext(ctx, "CREATE DATABASE "+target)
			require.NoError(t, err)

			reader, err := dumperFor("backup_source").Dump(ctx, service.BackupDumpOptions{ExcludeTableData: tc.excluded})
			require.NoError(t, err)
			dump, err := io.ReadAll(reader)
			require.NoError(t, err)
			require.NoError(t, reader.Close())
			require.NoError(t, dumperFor(target).Restore(ctx, bytes.NewReader(dump)))

			restored := open(target)
			// 表结构在两种情况下都必须存在。
			for _, table := range backupRestoreScopedTables {
				var regclass sql.NullString
				require.NoError(t, restored.QueryRowContext(ctx, "SELECT to_regclass($1)::text", table).Scan(&regclass))
				require.True(t, regclass.Valid, "table %s missing after restore", table)
			}
			// 其他业务数据不受开关影响。
			require.Equal(t, 1, countRows(t, ctx, restored, "SELECT COUNT(*) FROM settings WHERE key = 'backup_scope_probe'"))

			if tc.excluded != nil {
				for _, table := range backupRestoreScopedTables {
					require.Zero(t, countRows(t, ctx, restored, "SELECT COUNT(*) FROM "+table), table)
				}
				return
			}
			require.Equal(t, 2, countRows(t, ctx, restored, "SELECT COUNT(*) FROM ops_request_details WHERE request_id LIKE 'backup-scope-%'"))
			require.Equal(t, 1, countRows(t, ctx, restored, "SELECT COUNT(*) FROM content_moderation_logs WHERE request_id = 'backup-scope-log'"))
			require.Equal(t, 1, countRows(t, ctx, restored, "SELECT COUNT(*) FROM content_moderation_cyber_warnings WHERE request_id = 'backup-scope-cyber'"))
			// 媒体行分别挂在审核日志与 Cyber 警告下，恢复后父记录与图片内容都要完整。
			require.Equal(t, 1, countRows(t, ctx, restored, `SELECT COUNT(*) FROM content_moderation_media m
				JOIN content_moderation_logs l ON l.id = m.log_id
				WHERE l.request_id = 'backup-scope-log' AND m.content = '\x89504e47'::bytea`))
			require.Equal(t, 1, countRows(t, ctx, restored, `SELECT COUNT(*) FROM content_moderation_media m
				JOIN content_moderation_cyber_warnings w ON w.id = m.cyber_warning_id
				WHERE w.request_id = 'backup-scope-cyber' AND m.sha256 = 'cyber-media'`))
		})
	}
}

// seedBackupScopeData 写入普通与错误请求快照、审核日志、Cyber 警告及分属两者的图片快照。
func seedBackupScopeData(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	stmts := []string{
		`INSERT INTO settings (key, value) VALUES ('backup_scope_probe', '1')`,
		`INSERT INTO ops_request_details (request_id, kind, method, path, status_code) VALUES
			('backup-scope-normal', 'normal', 'POST', '/v1/chat/completions', 200),
			('backup-scope-error', 'error', 'POST', '/v1/chat/completions', 500)`,
		`INSERT INTO content_moderation_logs (request_id, flagged) VALUES ('backup-scope-log', TRUE)`,
		`INSERT INTO content_moderation_cyber_warnings (request_id) VALUES ('backup-scope-cyber')`,
		`INSERT INTO content_moderation_media (log_id, mime_type, sha256, content)
			SELECT id, 'image/png', 'log-media', '\x89504e47'::bytea FROM content_moderation_logs WHERE request_id = 'backup-scope-log'`,
		`INSERT INTO content_moderation_media (cyber_warning_id, mime_type, sha256, content)
			SELECT id, 'image/png', 'cyber-media', '\x89504e47'::bytea FROM content_moderation_cyber_warnings WHERE request_id = 'backup-scope-cyber'`,
	}
	for _, stmt := range stmts {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}
}

func countRows(t *testing.T, ctx context.Context, db *sql.DB, query string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRowContext(ctx, query).Scan(&n))
	return n
}

// 恢复 SQL 中途出错时整段事务回滚，Restore 必须返回错误，不能把失败恢复当成成功。
func TestBackupRestoreFailsOnSQLError(t *testing.T) {
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(ctx, backupRestorePostgresImage,
		tcpostgres.WithDatabase("restore_error"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(), loopbackPortBinding("5432/tcp"),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	host, err := backupRestoreContainerHost(ctx, container)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	dumper := &PgDumper{cfg: &config.DatabaseConfig{
		Host: host, Port: port.Int(), User: "postgres", Password: "postgres", DBName: "restore_error", SSLMode: "disable",
	}}

	// Windows 下 Docker Desktop 端口转发在容器刚启动时可能断开连接；先确认数据库可连接，再交给 psql。
	db, err := openSQLWithRetry(ctx, fmt.Sprintf("host=%s port=%d user=postgres password=postgres dbname=restore_error sslmode=disable", host, port.Int()), 60*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	script := `CREATE TABLE restore_sentinel (id int);
INSERT INTO restore_sentinel VALUES (1);
SELECT * FROM restore_missing_table;
INSERT INTO restore_sentinel VALUES (2);
`
	err = dumper.Restore(ctx, bytes.NewReader([]byte(script)))
	require.Error(t, err)
	require.Contains(t, err.Error(), "restore_missing_table")

	// 单事务整体回滚：哨兵表不应存在。
	var regclass sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, "SELECT to_regclass('public.restore_sentinel')::text").Scan(&regclass))
	require.False(t, regclass.Valid)

	// 正常脚本仍能成功恢复。
	require.NoError(t, dumper.Restore(ctx, bytes.NewReader([]byte("CREATE TABLE restore_ok (id int); INSERT INTO restore_ok VALUES (1);"))))
	require.Equal(t, 1, countRows(t, ctx, db, "SELECT COUNT(*) FROM restore_ok"))
}

// backupRestoreContainerHost 返回宿主机 psql 可用的容器地址。Windows 下 localhost 会先解析为 ::1，
// Docker Desktop 的 IPv6 转发可能直接断开连接，导致恢复在执行 SQL 之前就失败，因此固定使用 IPv4。
func backupRestoreContainerHost(ctx context.Context, container *tcpostgres.PostgresContainer) (string, error) {
	host, err := container.Host(ctx)
	if err != nil {
		return "", err
	}
	if host == "localhost" {
		return "127.0.0.1", nil
	}
	return host, nil
}
