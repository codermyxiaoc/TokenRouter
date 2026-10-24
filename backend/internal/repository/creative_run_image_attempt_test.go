//go:build unit

package repository

import (
	"context"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

// 队列冒烟夹具同步实现新增的持久预算接口，仍由真实服务执行完整账务流程。
func (r *smokeFakeRunRepo) BeginCreativeImageAttempt(ctx context.Context, id string, accountID int64, budget time.Duration, now time.Time) (*service.CreativeRun, error) {
	run, err := r.GetCreativeRunByRunID(ctx, id)
	if err != nil {
		return nil, err
	}
	ids, deadline, err := service.CreativeImageAttemptBudget(run, accountID, budget, now)
	if err != nil {
		return nil, err
	}
	run.ImageAttemptAccountIDs, run.ImageExecutionDeadlineAt, run.AccountID = ids, &deadline, &accountID
	run.ProvisioningPhase = service.CreativeProvisioningPhaseProviderInflight
	run.Version++
	copy := *run
	copy.ImageAttemptAccountIDs = append([]int64(nil), ids...)
	return &copy, nil
}

func (r *smokeFakeRunRepo) AuthorizeCreativeImageRetry(ctx context.Context, id string, accountID int64) error {
	run, err := r.GetCreativeRunByRunID(ctx, id)
	if err != nil {
		return err
	}
	if run.Status != service.CreativeRunStatusRunning || run.ProvisioningPhase != service.CreativeProvisioningPhaseProviderInflight || run.ProviderResultRecordedAt != nil || run.AccountID == nil || *run.AccountID != accountID {
		return service.ErrCreativeInvalidTransition
	}
	run.ProvisioningPhase = service.CreativeProvisioningPhaseProviderRetry
	run.Version++
	return nil
}

// 真实仓储验证持久账号集合、deadline和inflight栅栏；不访问业务数据库。
func TestCreativeImageAttemptPersistenceAndDeadline(t *testing.T) {
	repo, cleanup := openCreativeRunRepositoryForTest(t)
	t.Cleanup(cleanup)
	attempts, ok := repo.(service.CreativeImageAttemptRepository)
	require.True(t, ok)
	ctx := context.Background()
	now := time.Now().UTC()
	params := creativeRepoWorkspaceRunParams("crun_image_attempt", creativeRepoWorkspaceA, nil)
	_, err := repo.CreateCreativeRun(ctx, params)
	require.NoError(t, err)
	require.NoError(t, repo.MarkCreativeRunRunning(ctx, params.RunID, 0, now))
	first, err := attempts.BeginCreativeImageAttempt(ctx, params.RunID, 55, 5*time.Minute, now)
	require.NoError(t, err)
	require.Equal(t, []int64{55}, first.ImageAttemptAccountIDs)
	require.WithinDuration(t, now.Add(5*time.Minute), *first.ImageExecutionDeadlineAt, time.Millisecond)
	_, err = attempts.BeginCreativeImageAttempt(ctx, params.RunID, 56, time.Hour, now)
	require.ErrorIs(t, err, service.ErrCreativeInvalidTransition)
	require.ErrorIs(t, attempts.AuthorizeCreativeImageRetry(ctx, params.RunID, 99), service.ErrCreativeInvalidTransition)
	require.NoError(t, attempts.AuthorizeCreativeImageRetry(ctx, params.RunID, 55))
	_, err = attempts.BeginCreativeImageAttempt(ctx, params.RunID, 55, time.Hour, now)
	require.ErrorIs(t, err, service.ErrCreativeImageBudgetExhausted)
	second, err := attempts.BeginCreativeImageAttempt(ctx, params.RunID, 56, time.Hour, now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, []int64{55, 56}, second.ImageAttemptAccountIDs)
	require.Equal(t, *first.ImageExecutionDeadlineAt, *second.ImageExecutionDeadlineAt)
	require.NoError(t, attempts.AuthorizeCreativeImageRetry(ctx, params.RunID, 56))
	_, err = attempts.BeginCreativeImageAttempt(ctx, params.RunID, 57, time.Hour, now)
	require.ErrorIs(t, err, service.ErrCreativeImageBudgetExhausted)
}

func TestCreativeImageAttemptDeadlineCannotBeRenewedAfterExpiry(t *testing.T) {
	repo, cleanup := openCreativeRunRepositoryForTest(t)
	t.Cleanup(cleanup)
	attempts := repo.(service.CreativeImageAttemptRepository)
	ctx := context.Background()
	now := time.Now().UTC()
	params := creativeRepoWorkspaceRunParams("crun_image_deadline", creativeRepoWorkspaceA, nil)
	_, err := repo.CreateCreativeRun(ctx, params)
	require.NoError(t, err)
	require.NoError(t, repo.MarkCreativeRunRunning(ctx, params.RunID, 0, now))
	_, err = attempts.BeginCreativeImageAttempt(ctx, params.RunID, 55, time.Second, now)
	require.NoError(t, err)
	require.NoError(t, attempts.AuthorizeCreativeImageRetry(ctx, params.RunID, 55))
	_, err = attempts.BeginCreativeImageAttempt(ctx, params.RunID, 56, time.Hour, now.Add(2*time.Second))
	require.ErrorIs(t, err, service.ErrCreativeImageBudgetExhausted)
	run, err := repo.GetCreativeRunByRunID(ctx, params.RunID)
	require.NoError(t, err)
	require.Equal(t, []int64{55}, run.ImageAttemptAccountIDs)
}

// 重复领取竞争同一任务时，只有一个账号取得持久栅栏；失败者不能增加预算或覆盖账号。
func TestCreativeImageAttemptConcurrentClaimHasOneWinner(t *testing.T) {
	repo, cleanup := openCreativeRunRepositoryForTest(t)
	t.Cleanup(cleanup)
	attempts := repo.(service.CreativeImageAttemptRepository)
	ctx := context.Background()
	now := time.Now().UTC()
	params := creativeRepoWorkspaceRunParams("crun_image_concurrent", creativeRepoWorkspaceA, nil)
	_, err := repo.CreateCreativeRun(ctx, params)
	require.NoError(t, err)
	require.NoError(t, repo.MarkCreativeRunRunning(ctx, params.RunID, 0, now))
	start := make(chan struct{})
	winners := make(chan int64, 8)
	var wg sync.WaitGroup
	for id := int64(1); id <= 8; id++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := attempts.BeginCreativeImageAttempt(ctx, params.RunID, id, 5*time.Minute, now); err == nil {
				winners <- id
			}
		}()
	}
	close(start)
	wg.Wait()
	close(winners)
	require.Len(t, winners, 1)
	winner := <-winners
	run, err := repo.GetCreativeRunByRunID(ctx, params.RunID)
	require.NoError(t, err)
	require.Equal(t, []int64{winner}, run.ImageAttemptAccountIDs)
	require.Equal(t, winner, *run.AccountID)
	require.Equal(t, service.CreativeProvisioningPhaseProviderInflight, run.ProvisioningPhase)
}
