//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 创建链路只能从服务端已验证分组取得平台，后续分组变更不能污染已保存的任务命令。
func TestCreativeCreateFreezesQuotaPlatform(t *testing.T) {
	for _, platform := range []string{PlatformGemini, PlatformOpenAI, PlatformGrok} {
		t.Run(platform, func(t *testing.T) {
			svc := newCreativeTestService()
			params := validCreateParams()
			switch platform {
			case PlatformOpenAI:
				configureOpenAICreativeTestService(svc)
				params.Model = "gpt-image-2"
			case PlatformGrok:
				configureGrok2CreativeTestService(svc)
				params.Model = "grok-imagine-image-2.0"
			}
			created, err := svc.CreateRun(context.Background(), testCreativeScope(7), params, "quota-platform")
			require.NoError(t, err)
			repo := svc.Repo.(*creativeFakeRunRepo)
			require.Len(t, repo.createParams, 1)
			require.Equal(t, platform, repo.createParams[0].QuotaPlatform)
			run, err := repo.GetCreativeRunByRunID(context.Background(), created.ID)
			require.NoError(t, err)
			require.Equal(t, platform, run.QuotaPlatform)
			// 模拟配置调整后从仓储恢复：命令只依赖任务快照，不再读取分组平台。
			svc.GroupRepo.(*creativeFakeGroupRepo).byID[run.GroupID].Platform = PlatformAnthropic
			for _, id := range []string{CreativeHoldRequestID(run.RunID), CreativeCaptureRequestID(run.RunID), CreativeReleaseRequestID(run.RunID)} {
				cmd, err := buildCreativeHoldCommand(run, id, run.BaseUnitPrice)
				require.NoError(t, err)
				require.Equal(t, platform, cmd.QuotaPlatform)
				require.Equal(t, run.GroupID, *cmd.GroupID)
			}
		})
	}
}

// 旧任务没有平台证据时保留空快照和原分组 ID，交给仓储兼容路径解析，不能猜测默认平台。
func TestCreativeLegacyQuotaPlatformKeepsRepositoryFallback(t *testing.T) {
	run := &CreativeRun{RunID: "legacy-quota-platform", UserID: 7, GroupID: 12, APIKeyID: 100,
		RequestedOutputCount: 1, EstimatedCost: 0.12, BaseUnitPrice: 0.12, BalanceRateMultiplier: 1}
	for _, id := range []string{CreativeHoldRequestID(run.RunID), CreativeCaptureRequestID(run.RunID), CreativeReleaseRequestID(run.RunID)} {
		cmd, err := buildCreativeHoldCommand(run, id, 0.12)
		require.NoError(t, err)
		require.Empty(t, cmd.QuotaPlatform)
		require.Equal(t, run.GroupID, *cmd.GroupID)
	}
}
