//go:build integration

package repository

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 长创建请求必须独占租约；到期后只有一个恢复者接管，旧执行者不能改写归属和终态。
func TestVideoSubmissionLeaseLongBudgetAndConcurrentRecovery(t *testing.T) {
	for _, seconds := range []int{210, 630} {
		t.Run((time.Duration(seconds) * time.Second).String(), func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(100)
			ctx := context.Background()
			task := f.reserve(f.task(10, 1, 1))
			task.Status = "submitting"
			deadline := time.Now().UTC().Add(time.Duration(seconds) * time.Second)
			task.LeaseUntil = deadline
			submissionDeadline := deadline.Add(-30 * time.Second)
			task.SubmissionDeadlineAt = &submissionDeadline
			require.NoError(t, f.tasks.Save(ctx, task, false))
			fresh, err := f.tasks.Get(ctx, task.ID)
			require.NoError(t, err)
			require.WithinDuration(t, deadline, fresh.LeaseUntil, time.Millisecond)
			// 后续不带长租约的状态保存不能把已持有的租约缩短。
			fresh.LeaseUntil = time.Now().Add(time.Minute)
			require.NoError(t, f.tasks.Save(ctx, fresh, false))
			fresh, err = f.tasks.Get(ctx, task.ID)
			require.NoError(t, err)
			require.WithinDuration(t, deadline, fresh.LeaseUntil, time.Millisecond)
			var workers sync.WaitGroup
			errs := make(chan error, 64)
			for range 64 {
				workers.Add(1)
				go func() { defer workers.Done(); _, e := f.tasks.Claim(ctx, task.ID, 3*time.Minute); errs <- e }()
			}
			workers.Wait()
			close(errs)
			for err := range errs {
				require.ErrorIs(t, err, service.ErrVideoTaskNotFound)
			}
			f.money(990, 10, 10, 0)
			f.quotaUsage(10, 10, 10)
			// 模拟旧进程退出且租约到期，不调整金额或幂等账本。
			_, err = integrationDB.ExecContext(ctx, "UPDATE video_tasks SET lease_until=NOW()-INTERVAL '1 second' WHERE id=$1", task.ID)
			require.NoError(t, err)
			type claim struct {
				task *service.VideoTaskRecord
				err  error
			}
			claims := make(chan claim, 64)
			for range 64 {
				workers.Add(1)
				go func() {
					defer workers.Done()
					v, e := f.tasks.Claim(ctx, task.ID, 3*time.Minute)
					claims <- claim{v, e}
				}()
			}
			workers.Wait()
			close(claims)
			var winner *service.VideoTaskRecord
			claimed := 0
			for result := range claims {
				if result.err == nil {
					claimed++
					winner = result.task
				} else {
					require.ErrorIs(t, result.err, service.ErrVideoTaskNotFound)
				}
			}
			require.Equal(t, 1, claimed)
			require.NotEqual(t, task.LeaseToken, winner.LeaseToken)
			require.ErrorIs(t, f.tasks.Save(ctx, task, false), service.ErrVideoTaskConflict)
			winner.Status, winner.BillingStatus = "failed", "reconciliation"
			require.NoError(t, f.tasks.Save(ctx, winner, false))
			command := winner.Hold
			command.RequestID = "video_release:" + winner.ID
			command.RequestFingerprint = ""
			var applied atomic.Int64
			errs = make(chan error, 64)
			for range 64 {
				workers.Add(1)
				go func() {
					defer workers.Done()
					local := command
					result, e := f.repo.ReleaseBatchImageBalance(ctx, &local)
					if e == nil && result.Applied {
						applied.Add(1)
					}
					errs <- e
				}()
			}
			workers.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			require.EqualValues(t, 1, applied.Load())
			f.money(1000, 0, 0, 0)
			f.quotaUsage(0, 0, 0)
			var releases, captures int
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FILTER(WHERE request_id=$2),count(*) FILTER(WHERE request_id=$3) FROM usage_billing_dedup WHERE api_key_id=$1", task.APIKeyID, command.RequestID, "video_capture:"+task.ID).Scan(&releases, &captures))
			require.Equal(t, 1, releases)
			require.Zero(t, captures)
			fresh, err = f.tasks.Get(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, "released", fresh.BillingStatus)
			require.Equal(t, "failed", fresh.Status)
		})
	}
}

// 安全换号不会把十分钟创建预算的有效租约缩回三分钟。
func TestVideoSubmissionLeaseSurvivesRejectedAccountSwitch(t *testing.T) {
	f := newVideoBillingFixture(t)
	ctx := context.Background()
	task := newRejectedVideoForRetry(t, f)
	deadline := time.Now().UTC().Add(630 * time.Second)
	task.LeaseUntil = deadline
	require.NoError(t, f.tasks.Save(ctx, task, false))
	next := newVideoRetryTarget(t)
	moved, err := f.tasks.(service.VideoTaskRetryRepository).SwitchRejected(ctx, task.ID, task.LeaseToken, videoRetryTarget(next), task.Metadata, 2, 10)
	require.NoError(t, err)
	fresh, err := f.tasks.Get(ctx, moved.ID)
	require.NoError(t, err)
	require.WithinDuration(t, deadline, fresh.LeaseUntil, time.Millisecond)
	_, err = f.tasks.Claim(ctx, moved.ID, 3*time.Minute)
	require.ErrorIs(t, err, service.ErrVideoTaskNotFound)
	f.money(990, 10, 10, 0)
}

// 仓储也校验参数合同，不能绕过协调器把 H3 档位或收费元数据换成另一种含义。
func TestVideoSubmissionLeaseRetryRejectsContractChange(t *testing.T) {
	for _, kind := range []string{"profile", "seconds", "size", "resolution", "reference_images"} {
		t.Run(kind, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			task := newRejectedVideoForRetry(t, f)
			target := videoRetryTarget(newVideoRetryTarget(t))
			metadata := task.Metadata
			switch kind {
			case "profile":
				// H3 只属于 OpenAI Videos，使用真实适用端点验证转换合同差异。
				task.Target.Endpoint = service.VideoEndpointOpenAIVideos
				target.Endpoint = service.VideoEndpointOpenAIVideos
				require.NoError(t, f.tasks.Save(context.Background(), task, false))
				target.OpenAIProfile = service.VideoOpenAIProfileMetasoH3
			case "seconds":
				metadata.DurationSeconds = 8
			case "size":
				metadata.Size = "1280x720"
			case "resolution":
				metadata.Resolution = "768p"
			case "reference_images":
				count := 7
				metadata.ReferenceImageCount = &count
			}
			_, err := f.tasks.(service.VideoTaskRetryRepository).SwitchRejected(context.Background(), task.ID, task.LeaseToken, target, metadata, 1, 10)
			require.ErrorIs(t, err, service.ErrVideoTaskConflict)
			fresh, err := f.tasks.Get(context.Background(), task.ID)
			require.NoError(t, err)
			require.Equal(t, task.AccountID, fresh.AccountID)
			require.Empty(t, fresh.RejectedAttempts)
			f.money(990, 10, 10, 0)
		})
	}
}
