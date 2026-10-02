//go:build integration

package repository

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 历史受理不明任务必须先持久化失败决定，再经正式账本释放；模式变化不能绕过这道状态保护。
func TestVideoTaskHistoricalSubmissionFailureRelease(t *testing.T) {
	for _, mode := range []service.BillingMode{service.BillingModeVideo, service.BillingModeVideoPerRequest, service.BillingModeVideoToken} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := context.Background()
			f := newVideoBillingFixture(t)
			f.quota(100)

			// 先完成另一条任务，验证退款不会连同已有消费及账号成本一起抹除。
			other := f.reserve(f.task(1, 1, 1))
			otherCommand := f.command(other, "completed", 1)
			_, err := f.repo.CaptureBatchImageBalance(ctx, &otherCommand)
			require.NoError(t, err)

			var task *service.VideoTaskRecord
			var wantHold float64
			switch mode {
			case service.BillingModeVideo:
				task, wantHold = fixedVideoTestTask(t, f, 2.4, 3, .3, false), 7.5
			case service.BillingModeVideoPerRequest:
				task, _ = perRequestVideoBillingTask(t, f)
				wantHold = 6.675
			case service.BillingModeVideoToken:
				// Token 预扣固定秒价及图片费不乘视频的 3 倍倍率。
				task, wantHold = tokenPrepayTestTask(t, f, 3, .3), 2.7
			}
			task = f.reserve(task)
			require.InDelta(t, wantHold, task.Hold.HoldAmount, 1e-8)
			f.money(999-wantHold, wantHold, 1+wantHold, 1)
			f.quotaUsage(1+wantHold, 1+wantHold, 1+wantHold)

			// 模拟旧版本保存的创建错误；只使用隔离测试库，不发送上游请求。
			body := []byte(`{"error":{"message":"Service temporarily unavailable","type":"api_error"}}`)
			task.Status, task.BillingStatus = "submission_unknown", "reconciliation"
			task.UpstreamTaskID = ""
			task.ResponseStatus, task.CreateResponseStatus = http.StatusServiceUnavailable, http.StatusServiceUnavailable
			task.RawResponse, task.CreateResponse = body, body
			require.NoError(t, f.tasks.Save(ctx, task, true))
			task, err = f.tasks.Get(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, "reconciliation", task.BillingStatus)
			require.Empty(t, task.LeaseToken)

			command := task.Hold
			command.RequestID, command.RequestFingerprint = "video_release:"+task.ID, ""
			_, err = f.repo.ReleaseBatchImageBalance(ctx, &command)
			require.ErrorIs(t, err, service.ErrVideoTaskConflict, "退款策略不能让账本直接释放未决任务")
			var releaseCount int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1 AND request_id=$2`, f.keyID, command.RequestID).Scan(&releaseCount))
			require.Zero(t, releaseCount, "被状态保护拒绝的退款不得占用幂等键")
			f.money(999-wantHold, wantHold, 1+wantHold, 1)

			// 恢复器领取新租约并保存失败决定，重放后仍保留原始创建响应作为核对证据。
			task, err = f.tasks.Claim(ctx, task.ID, time.Minute)
			require.NoError(t, err)
			completed := time.Now().UTC()
			task.Status, task.CompletedAt = "failed", &completed
			require.NoError(t, f.tasks.Save(ctx, task, false))
			command = task.Hold
			command.RequestID, command.RequestFingerprint = "video_release:"+task.ID, ""

			var wg sync.WaitGroup
			var applied atomic.Int64
			errs := make(chan error, 16)
			for range 16 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					local := command
					result, releaseErr := f.repo.ReleaseBatchImageBalance(ctx, &local)
					if releaseErr == nil && result.Applied {
						applied.Add(1)
					}
					errs <- releaseErr
				}()
			}
			wg.Wait()
			close(errs)
			for releaseErr := range errs {
				require.NoError(t, releaseErr)
			}
			require.EqualValues(t, 1, applied.Load())
			replayed, err := f.repo.ReleaseBatchImageBalance(ctx, &command)
			require.NoError(t, err)
			require.False(t, replayed.Applied)

			// 金额、平台限额及 Key 各窗口都只保留另一条成功任务的消费。
			f.money(999, 0, 1, 1)
			f.quotaUsage(1, 1, 1)
			var usage5h, usage1d, usage7d float64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT usage_5h,usage_1d,usage_7d FROM api_keys WHERE id=$1`, f.keyID).Scan(&usage5h, &usage1d, &usage7d))
			for _, usage := range []float64{usage5h, usage1d, usage7d} {
				require.InDelta(t, 1, usage, 1e-8)
			}
			stored, err := f.tasks.Get(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, "failed", stored.Status)
			require.Equal(t, "released", stored.BillingStatus)
			require.NotNil(t, stored.CompletedAt)
			require.Equal(t, body, stored.CreateResponse)
			require.NotNil(t, stored.BillingResult)
			require.Zero(t, stored.BillingResult.ActualAmountUSD)
			require.InDelta(t, wantHold, stored.Hold.HoldAmount, 1e-8, "历史预留金额仍保留，不代表余额继续冻结")

			// 退款后不能再捕获，冲突事务也不能留下捕获幂等记录。
			capture := command
			capture.RequestID, capture.ActualBaseAmountUSD = "video_capture:"+task.ID, 1
			_, err = f.repo.CaptureBatchImageBalance(ctx, &capture)
			require.ErrorIs(t, err, service.ErrVideoTaskConflict)
			var captureCount int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE request_id=$2),count(*) FILTER(WHERE request_id=$3) FROM usage_billing_dedup WHERE api_key_id=$1`, f.keyID, command.RequestID, capture.RequestID).Scan(&releaseCount, &captureCount))
			require.Equal(t, 1, releaseCount)
			require.Zero(t, captureCount)
			f.money(999, 0, 1, 1)
		})
	}
}
