package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTeamMigrationContainsIsolationAndAttributionConstraints(t *testing.T) {
	content, err := FS.ReadFile("221_add_teams.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// 这些部分唯一索引共同保证单团队关系和单 Owner 约束。
	require.Contains(t, sql, "team_memberships_active_user_uq")
	require.Contains(t, sql, "on team_memberships (user_id) where left_at is null")
	require.Contains(t, sql, "team_memberships_active_owner_uq")
	require.Contains(t, sql, "where left_at is null and role = 'owner'")

	// Key、普通用量和批量图片作业都必须保留团队及付款人归因。
	require.Contains(t, sql, "api_keys_team_id_fkey")
	require.Contains(t, sql, "usage_logs_billing_user_id_fkey")
	require.Contains(t, sql, "usage_logs_team_id_fkey")
	require.Contains(t, sql, "batch_image_jobs_billing_user_id_fkey")
	require.Contains(t, sql, "batch_image_jobs_team_id_fkey")

	// 删除 Owner 前必须先转让所有权或解散团队。
	require.Contains(t, sql, "prevent_active_team_owner_deletion")
	require.Contains(t, sql, "users_prevent_active_team_owner_soft_delete")
	require.Contains(t, sql, "users_prevent_active_team_owner_hard_delete")
}

func TestTeamDefaultMemberLimitsMigration(t *testing.T) {
	content, err := FS.ReadFile("222_add_team_default_member_limits.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// 三种自然周期默认限额必须同时存在，并由数据库阻止负数。
	require.Contains(t, sql, "default_daily_limit_usd")
	require.Contains(t, sql, "default_weekly_limit_usd")
	require.Contains(t, sql, "default_monthly_limit_usd")
	require.Contains(t, sql, "teams_default_member_limits_check")
}

func TestTeamLifecycleAndAllowanceMigration(t *testing.T) {
	content, err := FS.ReadFile("223_harden_team_lifecycle_and_allowance.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// 两个轻量字段分别承载 Owner 锁定和批任务额度预记，不引入额外预留表。
	require.Contains(t, sql, "team_owner_disabled")
	require.Contains(t, sql, "allowance_reserved")
	require.NotContains(t, sql, "batch_image_billing_reservations")

	// 删除用户必须同时处理 Owner 保护、Member 离队和团队 Key 禁用。
	require.Contains(t, sql, "team_owner_transfer_required")
	require.Contains(t, sql, "update team_memberships")
	require.Contains(t, sql, "update api_keys")
}

func TestContentModerationTeamAttributionMigration(t *testing.T) {
	content, err := FS.ReadFile("225_content_moderation_team_attribution.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// 两类风控记录都必须新增付款人和团队字段。
	require.Equal(t, 2, strings.Count(sql, "add column if not exists billing_user_id bigint"))
	require.Equal(t, 2, strings.Count(sql, "add column if not exists team_id bigint"))

	// 删除关联对象后应保留审计记录本身。
	require.Contains(t, sql, "content_moderation_logs_billing_user_id_fkey")
	require.Contains(t, sql, "content_moderation_logs_team_id_fkey")
	require.Contains(t, sql, "content_moderation_cyber_warnings_billing_user_id_fkey")
	require.Contains(t, sql, "content_moderation_cyber_warnings_team_id_fkey")
	require.Equal(t, 4, strings.Count(sql, "on delete set null"))

	// 列表查询需要按付款人和团队使用时间索引。
	require.Contains(t, sql, "idx_content_moderation_logs_billing_user_created_at")
	require.Contains(t, sql, "idx_content_moderation_logs_team_created_at")
	require.Contains(t, sql, "idx_content_moderation_cyber_warnings_billing_user_created_at")
	require.Contains(t, sql, "idx_content_moderation_cyber_warnings_team_created_at")

	// 本地审计可回填原付款人，Cyber 必须优先使用用量记录的准确归属。
	require.Contains(t, sql, "set billing_user_id = user_id")
	require.Contains(t, sql, "set team_id = keys.team_id")
	require.Contains(t, sql, "from usage_logs as usage")
	require.Contains(t, sql, "set billing_user_id = usage.billing_user_id")
	require.Contains(t, sql, "keys.team_id is null")
	require.NotContains(t, sql, "update content_moderation_cyber_warnings\nset billing_user_id = user_id")

	// 无论是否能完整恢复归属，都不能改写历史处置对象。
	require.NotContains(t, sql, "set user_id =")
}
