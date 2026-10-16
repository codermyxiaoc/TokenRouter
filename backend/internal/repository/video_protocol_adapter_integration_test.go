//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 新快照使用原有 JSON 记录与预算事务；这里通过真实 PostgreSQL 重启读取验证字段不会丢失。
func videoAdapterSnapshotTask(t *testing.T, f *videoBillingFixture, task *service.VideoTaskRecord, adapter string) *service.VideoTaskRecord {
	t.Helper()
	task.Target.Version = 2
	task.Target.ProtocolAdapter = adapter
	task.Target.OpenAIProfile = ""
	task.Target.Endpoint = service.VideoEndpointOpenAIVideos
	task.Target.AccountID = f.accountID
	task.Target.BaseURL = "https://frozen.example"
	task.Target.CreatePath = "/v1/videos"
	if task.Target.Model == "" {
		task.Target.Model = "video-model"
	}
	require.NoError(t, f.tasks.Save(context.Background(), task, false))
	reloaded, err := NewVideoTaskRepository(integrationDB).Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, task.Target, reloaded.Target)
	return reloaded
}

// 并发完成和失败恢复仍只能捕获或释放一次；账号修改不会影响三类计费和固定图片费用。
func TestVideoProtocolAdapterSnapshotConcurrentSettlement(t *testing.T) {
	for _, mode := range videoProfileModes() {
		for _, status := range []string{"completed", "failed"} {
			name := fmt.Sprintf("%s/deferred_%t/%s", mode.mode, mode.deferToken, status)
			t.Run(name, func(t *testing.T) {
				f := newVideoBillingFixture(t)
				f.quota(1000)
				c := mode
				c.profile, c.image = service.VideoOpenAIProfilePassthrough, true
				task := videoAdapterSnapshotTask(t, f, videoProfileBillingTask(t, f, c), "openai_v1")
				frozen := task.Target
				task = f.reserve(task)
				hold, due, cost := videoProfileExpected(c, false)
				require.InDelta(t, hold, task.Hold.HoldAmount, 1e-9)
				f.money(1000-hold, hold, hold, 0)
				// 模拟管理员改账号后工作进程崩溃：新仓储实例只能读原任务冻结的协议。
				_, err := integrationDB.ExecContext(context.Background(), `UPDATE accounts SET credentials=$2::jsonb WHERE id=$1`, f.accountID,
					`{"video_endpoints":["minimax"],"video_endpoint_adapters":{"minimax":"minimax_v2"},"base_url":"https://changed.example"}`)
				require.NoError(t, err)
				task, err = NewVideoTaskRepository(integrationDB).Get(context.Background(), task.ID)
				require.NoError(t, err)
				require.Equal(t, frozen, task.Target)
				command, _ := videoProfileFinalCommand(t, f, task, status, 5, 100000)
				var applied atomic.Int64
				var wg sync.WaitGroup
				errs := make(chan error, 40)
				for range 40 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						local := command
						apply := f.repo.CaptureBatchImageBalance
						if status == "failed" {
							apply = f.repo.ReleaseBatchImageBalance
						}
						result, err := apply(context.Background(), &local)
						if err == nil && result.Applied {
							applied.Add(1)
						}
						errs <- err
					}()
				}
				wg.Wait()
				close(errs)
				for err := range errs {
					require.NoError(t, err)
				}
				require.EqualValues(t, 1, applied.Load())
				wantStatus := "settled"
				if status == "failed" {
					due, cost, wantStatus = 0, 0, "released"
				}
				f.money(1000-due, 0, due, cost)
				f.quotaUsage(due, due, due)
				fresh, err := NewVideoTaskRepository(integrationDB).Get(context.Background(), task.ID)
				require.NoError(t, err)
				require.Equal(t, frozen, fresh.Target)
				require.Equal(t, wantStatus, fresh.BillingStatus)
				var count int
				require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1 AND request_id=$2`, f.keyID, command.RequestID).Scan(&count))
				require.Equal(t, 1, count)
			})
		}
	}
}

// 换号事务同时检查旧合同与新协议，不能由协调器外部直接调用绕过适配器隔离。
func TestVideoProtocolAdapterRetryTransactionBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, adapter string
		version       int
		allowed       bool
	}{
		{name: "same_versioned_adapter", adapter: "openai_v1", version: 2, allowed: true},
		{name: "different_adapter", adapter: "metaso_h3_v1", version: 2},
		{name: "downgrade_to_legacy", version: 1},
		{name: "unknown_adapter", adapter: "openai_v999", version: 2},
		{name: "future_snapshot", adapter: "openai_v1", version: 99},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			task := f.task(10, 1, 1)
			task = videoAdapterSnapshotTask(t, f, task, "openai_v1")
			task = f.reserve(task)
			task.Status = "failed"
			task.CreateResponseStatus = 429
			task.CreateResponse = []byte(`{"error":{"code":"QuotaExceeded"}}`)
			task.RawResponse = task.CreateResponse
			require.NoError(t, f.tasks.Save(context.Background(), task, false))
			next := newVideoRetryTarget(t)
			target := task.Target
			target.AccountID, target.Version, target.ProtocolAdapter = next.ID, tc.version, tc.adapter
			moved, err := f.tasks.(service.VideoTaskRetryRepository).SwitchRejected(context.Background(), task.ID, task.LeaseToken, target, task.Metadata, 2, 10)
			if !tc.allowed {
				require.ErrorIs(t, err, service.ErrVideoTaskConflict)
				fresh, err := NewVideoTaskRepository(integrationDB).Get(context.Background(), task.ID)
				require.NoError(t, err)
				require.Equal(t, task.Target, fresh.Target)
				require.Empty(t, fresh.RejectedAttempts)
				f.money(990, 10, 10, 0)
				return
			}
			require.NoError(t, err)
			require.Equal(t, target, moved.Target)
			require.Len(t, moved.RejectedAttempts, 1)
			require.Equal(t, task.Target, moved.RejectedAttempts[0].Target)
			f.money(990, 10, 10, 0)
			// 再次迁移不能重复提交；重启后按最终账号结算且保留原预算。
			_, err = f.tasks.(service.VideoTaskRetryRepository).SwitchRejected(context.Background(), task.ID, task.LeaseToken, target, task.Metadata, 2, 10)
			require.ErrorIs(t, err, service.ErrVideoTaskConflict)
			fresh, err := NewVideoTaskRepository(integrationDB).Get(context.Background(), task.ID)
			require.NoError(t, err)
			cmd := f.command(fresh, "completed", 8)
			cmd.VideoAccountQuotaCost = 16
			result, err := f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.NoError(t, err)
			require.True(t, result.Applied)
			result, err = f.repo.CaptureBatchImageBalance(context.Background(), &cmd)
			require.NoError(t, err)
			require.False(t, result.Applied)
			f.money(992, 0, 8, 0)
			var accountCost float64
			require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT (extra->>'quota_used')::numeric FROM accounts WHERE id=$1`, next.ID).Scan(&accountCost))
			require.Equal(t, 16., accountCost)
		})
	}
}
