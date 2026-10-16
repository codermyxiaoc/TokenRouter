//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 真实事务只使用隔离测试容器；新 Vidu 选项不能改变旧可美任务冻结的协议、报价和预算。
func TestVideoViduAdapterFrozenSettlement(t *testing.T) {
	for _, adapter := range []string{"kemei_v1", "legacy_v1", "vidu_v2"} {
		for _, mode := range []service.BillingMode{service.BillingModeVideo, service.BillingModeVideoPerRequest} {
			for _, status := range []string{"completed", "failed"} {
				t.Run(adapter+"/"+string(mode)+"/"+status, func(t *testing.T) {
					f := newVideoBillingFixture(t)
					f.quota(1000)
					fixture := videoKemeiCases()[5]
					task := videoKemeiBillingTask(t, f, fixture, videoProfileBillingCase{mode: mode})
					task.Target.ProtocolAdapter = adapter
					_, metadata, err := service.PrepareVideoProtocolRequest([]byte(fixture.body), "", task.Target)
					require.NoError(t, err)
					require.Equal(t, task.Metadata, metadata)
					require.NoError(t, f.tasks.Save(context.Background(), task, false))
					task = f.reserve(task)
					frozen, frozenQuote := task.Target, *task.Quote
					next := "vidu_v2"
					if adapter == next {
						next = "legacy_v1"
					}
					credentials, err := json.Marshal(map[string]any{"base_url": "https://changed.example", "video_endpoints": []string{"vidu"},
						"video_endpoint_adapters": map[string]string{"vidu": next}, "video_vidu_model_paths": map[string]string{fixture.model: "/ent/v2/text2video"}})
					require.NoError(t, err)
					_, err = integrationDB.ExecContext(context.Background(), `UPDATE accounts SET credentials=$2::jsonb WHERE id=$1`, f.accountID, string(credentials))
					require.NoError(t, err)
					task, err = NewVideoTaskRepository(integrationDB).Get(context.Background(), task.ID)
					require.NoError(t, err)
					require.Equal(t, frozen, task.Target)
					require.Equal(t, frozenQuote, *task.Quote)
					path, err := service.VideoProtocolTaskPath(task.Target, "kemei-task")
					require.NoError(t, err)
					require.Equal(t, fixture.query, path)
					command, _ := videoProfileFinalCommand(t, f, task, status, 5, 0)
					_, due, cost := videoProfileExpected(videoProfileBillingCase{mode: mode, image: true}, false)
					apply, billingStatus := f.repo.CaptureBatchImageBalance, "settled"
					if status == "failed" {
						apply, due, cost, billingStatus = f.repo.ReleaseBatchImageBalance, 0, 0, "released"
					}
					for attempt := range 3 {
						local := command
						result, err := apply(context.Background(), &local)
						require.NoError(t, err)
						require.Equal(t, attempt == 0, result.Applied)
					}
					fresh, err := NewVideoTaskRepository(integrationDB).Get(context.Background(), task.ID)
					require.NoError(t, err)
					require.Equal(t, frozen, fresh.Target)
					require.Equal(t, billingStatus, fresh.BillingStatus)
					f.money(1000-due, 0, due, cost)
					f.quotaUsage(due, due, due)
				})
			}
		}
	}
}

// 账号换号事务不能跨可美、原样和官方 Vidu 合同重放已经预扣的创建请求。
func TestVideoViduAdapterRetryTransactionIsolation(t *testing.T) {
	for _, original := range []string{"kemei_v1", "legacy_v1", "vidu_v2"} {
		for _, replacement := range []string{"kemei_v1", "legacy_v1", "vidu_v2"} {
			t.Run(original+"/"+replacement, func(t *testing.T) {
				f := newVideoBillingFixture(t)
				task := videoKemeiBillingTask(t, f, videoKemeiCases()[5], videoProfileBillingCase{mode: service.BillingModeVideo})
				task.Target.ProtocolAdapter = original
				require.NoError(t, service.ValidateVideoProtocolTarget(task.Target))
				require.NoError(t, f.tasks.Save(context.Background(), task, false))
				task = f.reserve(task)
				task.Status, task.CreateResponseStatus = "failed", 429
				task.CreateResponse = []byte(`{"error":{"code":"QuotaExceeded"}}`)
				task.RawResponse = task.CreateResponse
				require.NoError(t, f.tasks.Save(context.Background(), task, false))
				target := task.Target
				target.AccountID, target.ProtocolAdapter = newVideoRetryTarget(t).ID, replacement
				moved, err := f.tasks.(service.VideoTaskRetryRepository).SwitchRejected(context.Background(), task.ID, task.LeaseToken, target, task.Metadata, 1, 10)
				if original == replacement {
					require.NoError(t, err)
					require.Equal(t, target, moved.Target)
					expectedHold := task.Hold
					expectedHold.VideoAccountID = target.AccountID
					require.Equal(t, expectedHold, moved.Hold)
					require.Equal(t, task.Quote, moved.Quote)
					require.Len(t, moved.RejectedAttempts, 1)
				} else {
					require.ErrorIs(t, err, service.ErrVideoTaskConflict)
					fresh, err := f.tasks.Get(context.Background(), task.ID)
					require.NoError(t, err)
					require.Equal(t, task.Target, fresh.Target)
					require.Empty(t, fresh.RejectedAttempts)
				}
				f.money(990.25, 9.75, 9.75, 0)
			})
		}
	}
}
