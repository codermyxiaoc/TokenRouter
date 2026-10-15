//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 真实数据库验证换号只迁移归属，不重复预扣、突破容量或允许过期执行者重放。
func newRejectedVideoForRetry(t *testing.T, f *videoBillingFixture) *service.VideoTaskRecord {
	t.Helper()
	record := f.reserve(f.task(10, 1, 1))
	record.Status = "failed"
	record.CreateResponseStatus = 429
	record.CreateResponse = []byte(`{"error":{"code":"QuotaExceeded"}}`)
	record.RawResponse = record.CreateResponse
	require.NoError(t, f.tasks.Save(context.Background(), record, false))
	return record
}
func newVideoRetryTarget(t *testing.T) *service.Account {
	t.Helper()
	return mustCreateAccount(t, integrationEntClient, &service.Account{Name: "video-retry-" + uuid.NewString(), Platform: service.PlatformVideo, Type: service.AccountTypeAPIKey})
}
func videoRetryTarget(a *service.Account) service.VideoUpstreamTarget {
	return service.VideoUpstreamTarget{Version: 1, AccountID: a.ID, Endpoint: service.VideoEndpointSeedance, BaseURL: "https://video.example.com", Model: "mapped-model"}
}
func TestVideoTaskRetryPreservesBudgetAndChargesFinalAccountOnce(t *testing.T) {
	f := newVideoBillingFixture(t)
	ctx := context.Background()
	task := newRejectedVideoForRetry(t, f)
	next := newVideoRetryTarget(t)
	retryRepo := f.tasks.(service.VideoTaskRetryRepository)
	// 预扣从可用余额移到冻结余额，并暂占 Key 额度；换号不能再次改变这三项。
	f.money(990, 10, 10, 0)
	moved, err := retryRepo.SwitchRejected(ctx, task.ID, task.LeaseToken, videoRetryTarget(next), task.Metadata, 2, 10)
	require.NoError(t, err)
	require.Equal(t, task.ID, moved.ID)
	require.Equal(t, next.ID, moved.AccountID)
	require.Equal(t, next.ID, moved.Hold.VideoAccountID)
	require.Equal(t, task.Hold.HoldAmount, moved.Hold.HoldAmount)
	require.Equal(t, task.Hold.ReservedAt, moved.Hold.ReservedAt)
	require.Equal(t, task.Hold.RequestID, moved.Hold.RequestID)
	require.Equal(t, "prepared", moved.Status)
	require.Len(t, moved.RejectedAttempts, 1)
	f.money(990, 10, 10, 0)
	var owner int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT account_id FROM video_tasks WHERE id=$1", task.ID).Scan(&owner))
	require.Equal(t, next.ID, owner)
	// 重启后从持久快照结算，最后账号计费一次，原账号不承担不存在的生成费用。
	moved, err = NewVideoTaskRepository(integrationDB).Get(ctx, task.ID)
	require.NoError(t, err)
	cmd := f.command(moved, "completed", 8)
	cmd.VideoAccountQuotaCost = cmd.ActualBaseAmountUSD * moved.AccountRateMultiplier
	applied := 0
	for range 3 {
		var result *service.BatchImageBalanceHoldResult
		result, err = f.repo.CaptureBatchImageBalance(ctx, &cmd)
		require.NoError(t, err)
		if result.Applied {
			applied++
		}
	}
	require.Equal(t, 1, applied, "恢复后重复提交相同结算只能首次生效")
	f.money(992, 0, 8, 0)
	var cost float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COALESCE((extra->>'quota_used')::numeric,0) FROM accounts WHERE id=$1", next.ID).Scan(&cost))
	require.Equal(t, float64(16), cost, "最终账号按迁移后的 2 倍倍率统计一次成本")
	var captureCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1 AND request_id=$2", task.APIKeyID, cmd.RequestID).Scan(&captureCount))
	require.Equal(t, 1, captureCount)
	_, err = retryRepo.SwitchRejected(ctx, task.ID, task.LeaseToken, videoRetryTarget(newVideoRetryTarget(t)), task.Metadata, 1, 10)
	require.ErrorIs(t, err, service.ErrVideoTaskConflict)
}
func TestVideoTaskRetryConcurrentSingleTransition(t *testing.T) {
	f := newVideoBillingFixture(t)
	task := newRejectedVideoForRetry(t, f)
	next := newVideoRetryTarget(t)
	repo := f.tasks.(service.VideoTaskRetryRepository)
	results := make(chan error, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.SwitchRejected(context.Background(), task.ID, task.LeaseToken, videoRetryTarget(next), task.Metadata, 1, 10)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		if err == nil {
			ok++
		} else {
			require.ErrorIs(t, err, service.ErrVideoTaskConflict)
		}
	}
	require.Equal(t, 1, ok)
	f.money(990, 10, 10, 0)
}
func TestVideoTaskRetryCapacitySerializesConcurrentAdmissions(t *testing.T) {
	f := newVideoBillingFixture(t)
	next := newVideoRetryTarget(t)
	repo := f.tasks.(service.VideoTaskRetryRepository)
	tasks := make([]*service.VideoTaskRecord, 6)
	for i := range tasks {
		tasks[i] = newRejectedVideoForRetry(t, f)
	}
	results := make(chan error, len(tasks))
	var wg sync.WaitGroup
	for _, task := range tasks {
		wg.Add(1)
		go func(task *service.VideoTaskRecord) {
			defer wg.Done()
			_, err := repo.SwitchRejected(context.Background(), task.ID, task.LeaseToken, videoRetryTarget(next), task.Metadata, 1, 1)
			results <- err
		}(task)
	}
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		if err == nil {
			ok++
		} else {
			require.ErrorIs(t, err, service.ErrVideoTaskBusy)
		}
	}
	require.Equal(t, 1, ok)
	f.money(940, 60, 60, 0)
}
func TestVideoTaskRetryRejectsUnknownAcceptedExpiredAndReleasedTasks(t *testing.T) {
	for _, scenario := range []string{"unknown", "task_id", "expired_lease", "wrong_lease", "released", "different_protocol"} {
		t.Run(scenario, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			task := newRejectedVideoForRetry(t, f)
			next := newVideoRetryTarget(t)
			target := videoRetryTarget(next)
			lease := task.LeaseToken
			switch scenario {
			case "unknown":
				task.CreateResponseStatus = 502
				task.CreateResponse = []byte(`<html>Bad Gateway</html>`)
				require.NoError(t, f.tasks.Save(context.Background(), task, false))
			case "task_id":
				task.UpstreamTaskID = "accepted"
				require.NoError(t, f.tasks.Save(context.Background(), task, false))
			case "expired_lease":
				_, err := integrationDB.ExecContext(context.Background(), "UPDATE video_tasks SET lease_until=NOW()-INTERVAL '1 second' WHERE id=$1", task.ID)
				require.NoError(t, err)
			case "wrong_lease":
				lease = "other-worker"
			case "released":
				cmd := f.command(task, "failed", 0)
				_, err := f.repo.ReleaseBatchImageBalance(context.Background(), &cmd)
				require.NoError(t, err)
			case "different_protocol":
				target.Endpoint = service.VideoEndpointCompat
			}
			_, err := f.tasks.(service.VideoTaskRetryRepository).SwitchRejected(context.Background(), task.ID, lease, target, task.Metadata, 1, 10)
			require.ErrorIs(t, err, service.ErrVideoTaskConflict)
			stored, err := f.tasks.Get(context.Background(), task.ID)
			require.NoError(t, err)
			require.Equal(t, f.accountID, stored.AccountID)
		})
	}
}
