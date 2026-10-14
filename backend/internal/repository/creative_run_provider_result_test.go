//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 真实 Ent 事务验证：半批输出元数据失败时不能留下成功证据，执行栅栏仍用于防止重发。
func TestCreativeProviderResultTransactionAndExecutionFence(t *testing.T) {
	repo, cleanup := openCreativeRunRepositoryForTest(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	params := creativeRepoWorkspaceRunParams("crun_provider_atomic", creativeRepoWorkspaceA, nil)
	params.RequestedOutputCount = 2
	_, err := repo.CreateCreativeRun(ctx, params)
	require.NoError(t, err)
	require.NoError(t, repo.MarkCreativeRunRunning(ctx, params.RunID, 55, time.Now()))
	require.NoError(t, repo.SetCreativeRunProvisioningPhase(ctx, params.RunID, service.CreativeProvisioningPhaseProviderInflight))
	// 延迟到达的创建补偿不允许覆盖已经开始执行的事实。
	require.NoError(t, repo.SetCreativeRunProvisioningPhase(ctx, params.RunID, service.CreativeProvisioningPhaseComplete))
	run, err := repo.GetCreativeRunByRunID(ctx, params.RunID)
	require.NoError(t, err)
	require.Equal(t, service.CreativeProvisioningPhaseProviderInflight, run.ProvisioningPhase)
	require.ErrorIs(t, repo.SetCreativeRunProvisioningPhase(ctx, params.RunID, service.CreativeProvisioningPhaseProviderInflight), service.ErrCreativeInvalidTransition)

	outputs := []service.CreativeRunOutput{
		{OutputIndex: 0, Status: service.CreativeRunOutputStatusSucceeded},
		{OutputIndex: 3, Status: service.CreativeRunOutputStatusSucceeded},
	}
	err = repo.RecordCreativeRunProviderResult(ctx, params.RunID, 55, outputs, time.Now())
	require.ErrorIs(t, err, service.ErrCreativeInvalidParams)
	gotOutputs, err := repo.ListCreativeRunOutputs(ctx, params.RunID)
	require.NoError(t, err)
	require.Len(t, gotOutputs, 2)
	for _, output := range gotOutputs {
		require.Equal(t, service.CreativeRunOutputStatusPending, output.Status)
	}
	run, err = repo.GetCreativeRunByRunID(ctx, params.RunID)
	require.NoError(t, err)
	require.Nil(t, run.ProviderResultRecordedAt)
	require.Equal(t, service.CreativeRunStatusRunning, run.Status)

	require.NoError(t, repo.MarkCreativeRunExecutionUnconfirmed(ctx, params.RunID, time.Now().Add(time.Hour)))
	run, err = repo.GetCreativeRunByRunID(ctx, params.RunID)
	require.NoError(t, err)
	require.Equal(t, "PROVIDER_RESULT_UNCONFIRMED", *run.ErrorCode)
	require.Nil(t, run.ActualCost)

	outputs[1].OutputIndex = 1
	require.NoError(t, repo.RecordCreativeRunProviderResult(ctx, params.RunID, 55, outputs, time.Now()))
	run, err = repo.GetCreativeRunByRunID(ctx, params.RunID)
	require.NoError(t, err)
	require.Equal(t, service.CreativeRunStatusProviderSucceeded, run.Status)
	require.NotNil(t, run.ProviderResultRecordedAt)
	require.Nil(t, run.ErrorCode)
	require.Nil(t, run.NextReconcileAt)
	require.NoError(t, repo.MarkCreativeRunResultLostAfterSettlement(ctx, params.RunID, 0.12, time.Now()))
	run, err = repo.GetCreativeRunByRunID(ctx, params.RunID)
	require.NoError(t, err)
	require.Equal(t, service.CreativeRunStatusResultLost, run.Status)
	require.Equal(t, 0.12, *run.ActualCost)
}
