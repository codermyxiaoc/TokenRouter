//go:build unit

package repository

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/enttest"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 使用真实持久数据库重开连接，证明平台快照来自任务行而非进程缓存或当前分组配置。
func TestCreativeQuotaPlatformPersistsAcrossRepositoryRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "creative-quota-platform.db"))
	open := func() *dbent.Client {
		db, err := sql.Open("sqlite", "file:"+path)
		require.NoError(t, err)
		// modernc 的 sqlite 驱动不使用 sqlite3 的 _fk 参数；固定连接并显式启用外键。
		// 在迁移前登记关闭，即使 enttest 的迁移断言失败也能释放 Windows 文件句柄。
		t.Cleanup(func() { _ = db.Close() })
		db.SetMaxOpenConns(1)
		_, err = db.ExecContext(ctx, "PRAGMA foreign_keys = ON")
		require.NoError(t, err)
		client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	client := open()
	group, err := client.Group.Create().SetName("creative-quota-origin").SetPlatform(service.PlatformGemini).Save(ctx)
	require.NoError(t, err)
	params := creativeRepoWorkspaceRunParams("crun_quota_persisted", creativeRepoWorkspaceA, nil)
	params.GroupID, params.QuotaPlatform = group.ID, group.Platform
	created, err := NewCreativeRunRepository(client).CreateCreativeRun(ctx, params)
	require.NoError(t, err)
	require.Equal(t, service.PlatformGemini, created.QuotaPlatform)
	_, err = client.Group.UpdateOneID(group.ID).SetPlatform(service.PlatformOpenAI).Save(ctx)
	require.NoError(t, err)
	// 旧格式任务参数不带平台值，兼容保存为空，不能推测填写当前分组平台。
	legacyParams := creativeRepoWorkspaceRunParams("crun_quota_legacy", creativeRepoWorkspaceA, nil)
	legacyParams.GroupID = group.ID
	legacy, err := NewCreativeRunRepository(client).CreateCreativeRun(ctx, legacyParams)
	require.NoError(t, err)
	require.Empty(t, legacy.QuotaPlatform)
	require.NoError(t, client.Close())

	reopened := open()
	repo := NewCreativeRunRepository(reopened)
	loaded, err := repo.GetCreativeRunByRunID(ctx, created.RunID)
	require.NoError(t, err)
	require.Equal(t, service.PlatformGemini, loaded.QuotaPlatform)
	currentGroup, err := reopened.Group.Get(ctx, group.ID)
	require.NoError(t, err)
	require.Equal(t, service.PlatformOpenAI, currentGroup.Platform)
	loadedLegacy, err := repo.GetCreativeRunByRunID(ctx, legacy.RunID)
	require.NoError(t, err)
	require.Empty(t, loadedLegacy.QuotaPlatform)
	require.Equal(t, group.ID, loadedLegacy.GroupID)
}
