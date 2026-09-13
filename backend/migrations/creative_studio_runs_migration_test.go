package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCreativeStudioRunsMigration 校验创作台任务元数据迁移的结构：
// api_keys.managed_by 约束、creative_runs 元数据列、creative_run_outputs 唯一约束与隐私红线。
func TestCreativeStudioRunsMigration(t *testing.T) {
	content, err := FS.ReadFile("253_creative_studio_runs.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// api_keys 增加 managed_by 列、普通索引与取值约束。
	require.Contains(t, sql, "alter table api_keys add column if not exists managed_by varchar(32)")
	require.Contains(t, sql, "create index if not exists api_keys_managed_by_idx on api_keys (managed_by)")
	require.Contains(t, sql, "managed_by is null or managed_by = 'creative_studio'")

	// creative_runs 只保存任务元数据。
	require.Contains(t, sql, "create table if not exists creative_runs")
	require.Contains(t, sql, "run_id varchar(64) not null unique")
	require.Contains(t, sql, "operation varchar(16) not null")
	require.Contains(t, sql, "prompt_hash varchar(64) not null")
	require.Contains(t, sql, "request_fingerprint varchar(64) not null unique")
	require.Contains(t, sql, "status varchar(20) not null default 'queued'")
	require.Contains(t, sql, "balance_hold_amount decimal(20,10)")
	require.Contains(t, sql, "subscription_hold_allocations jsonb")
	require.Contains(t, sql, "version bigint not null default 1")
	// 外键：user 级联删除、执行 Key 与分组约束。
	require.Contains(t, sql, "user_id bigint not null references users(id) on delete cascade")
	require.Contains(t, sql, "api_key_id bigint not null references api_keys(id)")
	require.Contains(t, sql, "group_id bigint not null references groups(id)")
	// 用户列表索引与活跃状态部分索引。
	require.Contains(t, sql, "create index if not exists creative_runs_user_created_at_idx on creative_runs (user_id, created_at desc)")
	require.Contains(t, sql, "where status in ('queued', 'running')")
	// 幂等键部分唯一索引。
	require.Contains(t, sql, "create unique index if not exists creative_runs_idempotency_key_uq on creative_runs (user_id, idempotency_key)")

	// creative_run_outputs 输出元数据与 (run_id, output_index) 唯一约束。
	require.Contains(t, sql, "create table if not exists creative_run_outputs")
	require.Contains(t, sql, "references creative_runs(run_id) on delete cascade")
	require.Contains(t, sql, "unique (run_id, output_index)")
	require.Contains(t, sql, "transient_expires_at timestamptz")
	require.Contains(t, sql, "acked_at timestamptz")

	// 隐私红线：迁移不得包含任何保存图片字节、mask 或 prompt 明文的列。
	for _, forbidden := range []string{"prompt text", "image_bytes", "mask_bytes", "image_data", "source_image", "response_body"} {
		require.NotContains(t, sql, forbidden)
	}
}
