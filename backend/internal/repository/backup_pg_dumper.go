package repository

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

// PgDumper implements service.DBDumper using pg_dump/psql
type PgDumper struct {
	cfg *config.DatabaseConfig
}

// NewPgDumper creates a new PgDumper
func NewPgDumper(cfg *config.Config) service.DBDumper {
	return &PgDumper{cfg: &cfg.Database}
}

// pgDumpArgs 构造 pg_dump 参数；排除项只使用 --exclude-table-data，表结构始终导出。
func pgDumpArgs(cfg *config.DatabaseConfig, opts service.BackupDumpOptions) []string {
	args := []string{
		"-h", cfg.Host,
		"-p", fmt.Sprintf("%d", cfg.Port),
		"-U", cfg.User,
		"-d", cfg.DBName,
		"--no-owner",
		"--no-acl",
		"--clean",
		"--if-exists",
	}
	for _, tablePattern := range opts.ExcludeTableData {
		// 只跳过表数据，保留结构、约束和索引，避免恢复后缺表。
		args = append(args, "--exclude-table-data="+tablePattern)
	}

	return args
}

// Dump executes pg_dump and returns a streaming reader of the output
func (d *PgDumper) Dump(ctx context.Context, opts service.BackupDumpOptions) (io.ReadCloser, error) {
	args := pgDumpArgs(d.cfg, opts)

	cmd := exec.CommandContext(ctx, "pg_dump", args...)
	if d.cfg.Password != "" {
		cmd.Env = append(cmd.Environ(), "PGPASSWORD="+d.cfg.Password)
	}
	if d.cfg.SSLMode != "" {
		cmd.Env = append(cmd.Environ(), "PGSSLMODE="+d.cfg.SSLMode)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start pg_dump: %w", err)
	}

	// 返回一个 ReadCloser：读 stdout，关闭时等待进程退出
	return &cmdReadCloser{ReadCloser: stdout, cmd: cmd}, nil
}

// psqlRestoreArgs 构造恢复参数。psql 默认遇到 SQL 错误会继续执行并以 0 退出，
// 在 --single-transaction 下整段事务已回滚却被当作成功；ON_ERROR_STOP 让首个错误即以非零退出。
func psqlRestoreArgs(cfg *config.DatabaseConfig) []string {
	return []string{
		"-h", cfg.Host,
		"-p", fmt.Sprintf("%d", cfg.Port),
		"-U", cfg.User,
		"-d", cfg.DBName,
		"-v", "ON_ERROR_STOP=1",
		"--single-transaction",
	}
}

// maxRestoreErrorOutput 限制写入备份记录的 psql 输出长度，避免把大段 SQL 原文带进错误信息。
const maxRestoreErrorOutput = 2048

// restoreErrorOutput 只保留 psql 输出末尾（错误行通常在最后），超长时截断。
func restoreErrorOutput(output []byte) string {
	text := strings.TrimSpace(string(output))
	if len(text) > maxRestoreErrorOutput {
		text = "..." + text[len(text)-maxRestoreErrorOutput:]
	}
	return text
}

// Restore executes psql to restore from a streaming reader
func (d *PgDumper) Restore(ctx context.Context, data io.Reader) error {
	args := psqlRestoreArgs(d.cfg)

	cmd := exec.CommandContext(ctx, "psql", args...)
	if d.cfg.Password != "" {
		cmd.Env = append(cmd.Environ(), "PGPASSWORD="+d.cfg.Password)
	}
	if d.cfg.SSLMode != "" {
		cmd.Env = append(cmd.Environ(), "PGSSLMODE="+d.cfg.SSLMode)
	}

	cmd.Stdin = data

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("psql restore failed: %v: %s", err, restoreErrorOutput(output))
	}
	return nil
}

// cmdReadCloser wraps a command stdout pipe and waits for the process on Close
type cmdReadCloser struct {
	io.ReadCloser
	cmd *exec.Cmd
}

func (c *cmdReadCloser) Close() error {
	// Close the pipe first
	_ = c.ReadCloser.Close()
	// Wait for the process to exit
	if err := c.cmd.Wait(); err != nil {
		return fmt.Errorf("pg_dump exited with error: %w", err)
	}
	return nil
}
