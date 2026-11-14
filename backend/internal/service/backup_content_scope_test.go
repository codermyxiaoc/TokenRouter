//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 本次纳入备份选项管理的三类快照表。
const (
	backupTableRequestDetails  = "public.ops_request_details"
	backupTableModerationLogs  = "public.content_moderation_logs"
	backupTableModerationMedia = "public.content_moderation_media"
	backupTableCyberWarnings   = "public.content_moderation_cyber_warnings"
)

var backupModerationTables = []string{backupTableModerationLogs, backupTableModerationMedia, backupTableCyberWarnings}

// 核心业务表始终导出数据，任何勾选组合都不能出现在排除列表中。
var backupCoreTables = []string{
	"public.users",
	"public.user_subscriptions",
	"public.payment_orders",
	"public.accounts",
	"public.groups",
	"public.api_keys",
	"public.settings",
	"public.redeem_codes",
	"public.daily_activity_reward_items",
}

// 修改前各组的排除表（不含本次新增），用于确认其他表范围不变。
var backupPreviousGroupTables = map[string][]string{
	"usage_records": {
		"public.usage_logs", "public.usage_logs_*", "public.billing_usage_entries", "public.usage_billing_dedup",
		"public.usage_billing_dedup_archive", "public.usage_dashboard_hourly", "public.usage_dashboard_daily",
		"public.usage_dashboard_hourly_users", "public.usage_dashboard_daily_users",
		"public.usage_dashboard_aggregation_watermark", "public.usage_analytics_hourly",
		"public.usage_analytics_daily", "public.usage_analytics_aggregation_state",
	},
	"ops_logs": {
		"public.ops_system_logs", "public.ops_error_logs", "public.ops_retry_attempts", "public.ops_system_metrics",
		"public.ops_metrics_hourly", "public.ops_metrics_daily", "public.ops_alert_events",
		"public.ops_job_heartbeats", "public.ops_system_log_cleanup_audits",
	},
	"audit_logs": {
		"public.payment_audit_logs", backupTableModerationLogs, "public.announcement_reads",
		"public.orphan_allowed_groups_audit", "public.auth_identity_migration_reports",
	},
	"runtime_data": {
		"public.idempotency_records", "public.scheduler_outbox", "public.pending_auth_sessions",
		"public.identity_adoption_decisions", "public.usage_cleanup_tasks", "public.scheduled_test_results",
	},
}

// 四项全部关闭时，新增的请求快照、审核图片与 Cyber 警告都只导出结构。
func TestBackupContentScope_AllOffExcludesNewSnapshotTables(t *testing.T) {
	svc := newTestBackupService(t, newMockSettingRepo(), &mockDumper{dumpData: []byte("data")}, newMockObjectStore())
	cfg, err := svc.GetContentConfig(context.Background())
	require.NoError(t, err)
	require.Contains(t, cfg.ExcludedTableData, backupTableRequestDetails)
	for _, table := range backupModerationTables {
		require.Contains(t, cfg.ExcludedTableData, table)
	}
	// 原 33 项加新增 3 项。
	require.Len(t, cfg.ExcludedTableData, 36)
}

// 逐一覆盖四个开关的全部 16 种组合：新增表只随所属组变化，其余表与修改前完全一致，核心表始终导出。
func TestBackupContentScope_AllCombinations(t *testing.T) {
	for mask := 0; mask < 16; mask++ {
		cfg := BackupContentConfig{
			IncludeUsageRecords: mask&1 != 0,
			IncludeOpsLogs:      mask&2 != 0,
			IncludeAuditLogs:    mask&4 != 0,
			IncludeRuntimeData:  mask&8 != 0,
		}
		svc := newTestBackupService(t, newMockSettingRepo(), &mockDumper{dumpData: []byte("data")}, newMockObjectStore())
		saved, err := svc.UpdateContentConfig(context.Background(), cfg)
		require.NoError(t, err)

		included := map[string]bool{
			"usage_records": cfg.IncludeUsageRecords,
			"ops_logs":      cfg.IncludeOpsLogs,
			"audit_logs":    cfg.IncludeAuditLogs,
			"runtime_data":  cfg.IncludeRuntimeData,
		}
		var expected []string
		for group, tables := range backupPreviousGroupTables {
			if !included[group] {
				expected = append(expected, tables...)
			}
		}
		if !cfg.IncludeOpsLogs {
			expected = append(expected, backupTableRequestDetails)
		}
		if !cfg.IncludeAuditLogs {
			expected = append(expected, backupTableModerationMedia, backupTableCyberWarnings)
		}
		require.ElementsMatch(t, expected, saved.ExcludedTableData, "mask=%d", mask)

		// 三张审核表同进同出，避免恢复后父子记录不完整。
		for _, table := range backupModerationTables {
			require.Equal(t, !cfg.IncludeAuditLogs, contains(saved.ExcludedTableData, table), "mask=%d table=%s", mask, table)
		}
		require.Equal(t, !cfg.IncludeOpsLogs, contains(saved.ExcludedTableData, backupTableRequestDetails), "mask=%d", mask)
		for _, table := range backupCoreTables {
			require.NotContains(t, saved.ExcludedTableData, table, "mask=%d", mask)
		}
	}
}

// 单组、混合与全选时，实际传给 dumper 的排除项与配置接口返回值一致。
func TestBackupContentScope_DumpOptionsFollowSavedConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  BackupContentConfig
	}{
		{"只选运维", BackupContentConfig{IncludeOpsLogs: true}},
		{"只选审计", BackupContentConfig{IncludeAuditLogs: true}},
		{"运维加用量", BackupContentConfig{IncludeOpsLogs: true, IncludeUsageRecords: true}},
		{"审计加运行时", BackupContentConfig{IncludeAuditLogs: true, IncludeRuntimeData: true}},
		{"全部选中", BackupContentConfig{IncludeUsageRecords: true, IncludeOpsLogs: true, IncludeAuditLogs: true, IncludeRuntimeData: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dumper := &mockDumper{dumpData: []byte("data")}
			svc := newTestBackupService(t, newMockSettingRepo(), dumper, newMockObjectStore())
			saved, err := svc.UpdateContentConfig(context.Background(), tc.cfg)
			require.NoError(t, err)
			_, err = svc.CreateBackup(context.Background(), "manual", 14)
			require.NoError(t, err)
			require.ElementsMatch(t, saved.ExcludedTableData, dumper.opts.ExcludeTableData)
			require.Equal(t, !tc.cfg.IncludeOpsLogs, contains(dumper.opts.ExcludeTableData, backupTableRequestDetails))
			for _, table := range backupModerationTables {
				require.Equal(t, !tc.cfg.IncludeAuditLogs, contains(dumper.opts.ExcludeTableData, table))
			}
		})
	}
	// 全部选中时不排除任何表数据。
	dumper := &mockDumper{dumpData: []byte("data")}
	svc := newTestBackupService(t, newMockSettingRepo(), dumper, newMockObjectStore())
	_, err := svc.UpdateContentConfig(context.Background(), BackupContentConfig{IncludeUsageRecords: true, IncludeOpsLogs: true, IncludeAuditLogs: true, IncludeRuntimeData: true})
	require.NoError(t, err)
	_, err = svc.CreateBackup(context.Background(), "manual", 14)
	require.NoError(t, err)
	require.Empty(t, dumper.opts.ExcludeTableData)
}

// 已保存的旧配置缺少字段时按默认关闭处理，已有勾选保持不变，保存后读回一致，且不写入新字段。
func TestBackupContentScope_LegacyConfigKeepsSelections(t *testing.T) {
	repo := newMockSettingRepo()
	repo.data[settingKeyBackupContentConfig] = `{"include_audit_logs":true}`
	svc := newTestBackupService(t, repo, &mockDumper{dumpData: []byte("data")}, newMockObjectStore())

	cfg, err := svc.GetContentConfig(context.Background())
	require.NoError(t, err)
	require.False(t, cfg.IncludeUsageRecords)
	require.False(t, cfg.IncludeOpsLogs)
	require.True(t, cfg.IncludeAuditLogs)
	require.False(t, cfg.IncludeRuntimeData)
	require.Contains(t, cfg.ExcludedTableData, backupTableRequestDetails)
	for _, table := range backupModerationTables {
		require.NotContains(t, cfg.ExcludedTableData, table)
	}

	_, err = svc.UpdateContentConfig(context.Background(), BackupContentConfig{IncludeOpsLogs: true, IncludeAuditLogs: true})
	require.NoError(t, err)
	require.JSONEq(t, `{"include_usage_records":false,"include_ops_logs":true,"include_audit_logs":true,"include_runtime_data":false}`, repo.data[settingKeyBackupContentConfig])
	reloaded, err := svc.GetContentConfig(context.Background())
	require.NoError(t, err)
	require.True(t, reloaded.IncludeOpsLogs)
	require.True(t, reloaded.IncludeAuditLogs)
	require.NotContains(t, reloaded.ExcludedTableData, backupTableRequestDetails)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
