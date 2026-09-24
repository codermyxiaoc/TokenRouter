//go:build unit

package service

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/claude"
	"github.com/stretchr/testify/require"
)

// 环境配置在包初始化时冻结，子进程验证真实升级场景，避免测试修改生产快照。
func TestClaudeCodeVersionEnvironmentPin(t *testing.T) {
	if os.Getenv("TOKENROUTER_TEST_CLAUDE_PIN") == "1" {
		require.Equal(t, "2.1.280", claude.CLIEnvironmentOverride())
		repo := &authSourceDefaultsRepoStub{values: map[string]string{
			SettingKeyClaudeCodeClientVersionSynced: "2.1.281",
		}}
		svc := NewSettingService(repo, &config.Config{})
		require.Equal(t, "2.1.280", svc.GetClaudeCodeClientVersion(context.Background()), "同步结果不能覆盖已有环境固定值")
		repo.values[SettingKeyClaudeCodeClientVersion] = "2.1.282"
		svc.InvalidateClaudeCodeClientVersionCache()
		require.Equal(t, "2.1.282", svc.GetClaudeCodeClientVersion(context.Background()), "显式面板配置可覆盖环境固定值")
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestClaudeCodeVersionEnvironmentPin$")
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, claude.CLIVersionEnv+"=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, claude.CLIVersionEnv+"=2.1.280", "TOKENROUTER_TEST_CLAUDE_PIN=1")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
}

func TestGetClaudeCodeClientVersionPriority(t *testing.T) {
	for _, tt := range []struct {
		name   string
		manual string
		synced string
		want   string
	}{
		{name: "手动值优先", manual: "2.1.280", synced: "2.1.281", want: "2.1.280"},
		{name: "归一化手动值", manual: " v2.1.280 ", synced: "2.1.281", want: "2.1.280"},
		{name: "跟随同步值", synced: "2.1.281", want: "2.1.281"},
		{name: "无效手动值回退", manual: "invalid", synced: "2.1.281", want: "2.1.281"},
		{name: "无效同步值回退", synced: "2.1.9", want: claude.CLIVersion()},
		{name: "未配置时回退", want: claude.CLIVersion()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &authSourceDefaultsRepoStub{values: map[string]string{
				SettingKeyClaudeCodeClientVersion:       tt.manual,
				SettingKeyClaudeCodeClientVersionSynced: tt.synced,
			}}
			svc := NewSettingService(repo, &config.Config{})
			require.Equal(t, tt.want, svc.GetClaudeCodeClientVersion(context.Background()))
		})
	}
}

func TestUpdateSettingsClaudeCodeVersionTakesEffectImmediately(t *testing.T) {
	ctx := context.Background()
	repo := &authSourceDefaultsRepoStub{values: map[string]string{
		SettingKeyClaudeCodeClientVersionSynced: "2.1.281",
	}}
	svc := NewSettingService(repo, &config.Config{})
	resetGatewayForwardingSettingsCacheForTest(t)
	defer svc.refreshCachedSettings(&SystemSettings{})
	require.Equal(t, "2.1.281", svc.GetClaudeCodeClientVersion(ctx))

	settings := &SystemSettings{
		ClaudeCodeClientVersion:          "2.1.280",
		ClaudeCodeVersionAutoSyncEnabled: true,
	}
	require.NoError(t, svc.UpdateSettings(ctx, settings))
	require.Equal(t, "2.1.280", svc.GetClaudeCodeClientVersion(ctx), "保存手动版本后应立即生效")
	require.NotContains(t, repo.updates, SettingKeyClaudeCodeClientVersionSynced)
	require.Equal(t, "2.1.281", repo.values[SettingKeyClaudeCodeClientVersionSynced])

	// 清空手动值并关闭后续同步时，仍保留并使用已有同步值。
	settings.ClaudeCodeClientVersion = ""
	settings.ClaudeCodeVersionAutoSyncEnabled = false
	require.NoError(t, svc.UpdateSettings(ctx, settings))
	require.Equal(t, "2.1.281", svc.GetClaudeCodeClientVersion(ctx))
	require.Equal(t, "false", repo.values[SettingKeyClaudeCodeVersionAutoSyncEnabled])
}
