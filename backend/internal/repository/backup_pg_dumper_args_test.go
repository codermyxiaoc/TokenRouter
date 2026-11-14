//go:build unit

package repository

import (
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 排除项只能生成 --exclude-table-data：表结构必须始终导出，不能出现排除整表的参数。
func TestPgDumpArgsExcludeDataOnly(t *testing.T) {
	cfg := &config.DatabaseConfig{Host: "db", Port: 5432, User: "u", DBName: "app"}
	tables := []string{
		"public.ops_request_details",
		"public.content_moderation_logs",
		"public.content_moderation_media",
		"public.content_moderation_cyber_warnings",
		"public.usage_logs_*",
	}
	args := pgDumpArgs(cfg, service.BackupDumpOptions{ExcludeTableData: tables})

	var dataExcludes []string
	for _, arg := range args {
		require.NotEqual(t, "-T", arg)
		require.NotEqual(t, "-t", arg)
		require.False(t, strings.HasPrefix(arg, "--exclude-table="), arg)
		require.False(t, strings.HasPrefix(arg, "--table="), arg)
		require.NotEqual(t, "--data-only", arg)
		require.NotEqual(t, "--schema-only", arg)
		if value, ok := strings.CutPrefix(arg, "--exclude-table-data="); ok {
			dataExcludes = append(dataExcludes, value)
		}
	}
	require.Equal(t, tables, dataExcludes)
	require.Contains(t, args, "--clean")
	require.Contains(t, args, "--if-exists")
}

// 全部选中时不追加任何排除参数。
func TestPgDumpArgsNoExcludes(t *testing.T) {
	args := pgDumpArgs(&config.DatabaseConfig{Host: "db", Port: 5432, User: "u", DBName: "app"}, service.BackupDumpOptions{})
	for _, arg := range args {
		require.False(t, strings.HasPrefix(arg, "--exclude"), arg)
	}
}

// 恢复必须在首个 SQL 错误时以非零退出，否则单事务回滚后 psql 仍返回 0，上层会误判恢复成功。
func TestPsqlRestoreArgsStopOnError(t *testing.T) {
	args := psqlRestoreArgs(&config.DatabaseConfig{Host: "db", Port: 5432, User: "u", DBName: "app"})
	joined := strings.Join(args, " ")
	require.Contains(t, joined, "-v ON_ERROR_STOP=1")
	require.Contains(t, args, "--single-transaction")
	require.Equal(t, []string{"-h", "db", "-p", "5432", "-U", "u", "-d", "app"}, args[:8])
}

// 写入错误信息的 psql 输出只保留末尾且有长度上限。
func TestRestoreErrorOutputTruncated(t *testing.T) {
	require.Equal(t, "ERROR: boom", restoreErrorOutput([]byte("  ERROR: boom\n")))
	long := strings.Repeat("x", maxRestoreErrorOutput*2) + "ERROR: tail"
	out := restoreErrorOutput([]byte(long))
	require.True(t, strings.HasPrefix(out, "..."))
	require.True(t, strings.HasSuffix(out, "ERROR: tail"))
	require.Len(t, out, maxRestoreErrorOutput+3)
}
