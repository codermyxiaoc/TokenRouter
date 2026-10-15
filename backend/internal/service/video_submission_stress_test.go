package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 压测只替换上游与存储边界，选号重试、状态推进、预算合同和用量生成均使用真实协调器。
// 金额原子性与真实租约互斥由 PostgreSQL 集成测试负责，不能用此内存夹具替代数据库验证。
type videoStressTransport struct {
	mu               sync.Mutex
	template         *VideoUpstreamSelection
	scenario         string
	calls            map[string][]int64
	polls            int
	firstCalls       int
	firstGate        chan struct{}
	gateSize         int
	activePosts      int
	peakPosts        int
	selected         atomic.Int64
	released         atomic.Int64
	duplicateRelease atomic.Int64
}

type videoStressCancelKey struct{}

func (u *videoStressTransport) SelectAccount(ctx context.Context, _ *APIKey, req VideoTaskSubmitRequest) (*VideoUpstreamSelection, error) {
	for id := int64(4); id <= 6; id++ {
		if _, excluded := req.ExcludedAccountIDs[id]; excluded {
			continue
		}
		selection := *u.template
		account := *u.template.Account
		account.ID = id
		rate := float64(id - 3)
		account.RateMultiplier = &rate
		selection.Account = &account
		selection.Target.AccountID = id
		selection.Target.Model = fmt.Sprintf("mapped-model-%d", id)
		selection.Metadata.Model = selection.Target.Model
		if u.scenario == "endpoint_mismatch" && id == 5 {
			selection.Target.Endpoint = VideoEndpointSeedance
		}
		selection.Body = append([]byte(nil), req.Body...)
		u.selected.Add(1)
		var released atomic.Bool
		selection.Release = func() {
			if !released.CompareAndSwap(false, true) {
				u.duplicateRelease.Add(1)
				return
			}
			u.released.Add(1)
		}
		if u.scenario == "cancel_during_selection" && id == 5 {
			if cancel, ok := ctx.Value(videoStressCancelKey{}).(context.CancelFunc); ok {
				cancel()
			}
		}
		return &selection, nil
	}
	return nil, ErrNoAvailableAccounts
}

func videoStressTaskID(body []byte) string {
	hash := sha256.Sum256(body)
	return "stress-" + hex.EncodeToString(hash[:8])
}

func (u *videoStressTransport) Submit(ctx context.Context, selection *VideoUpstreamSelection) (*VideoUpstreamResponse, error) {
	job := string(selection.Body)
	u.mu.Lock()
	u.calls[job] = append(u.calls[job], selection.Account.ID)
	first := len(u.calls[job]) == 1
	u.activePosts++
	u.peakPosts = max(u.peakPosts, u.activePosts)
	if first && u.firstGate != nil {
		u.firstCalls++
		if u.firstCalls == u.gateSize {
			close(u.firstGate)
		}
	}
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.activePosts--
		u.mu.Unlock()
	}()
	if first && u.firstGate != nil {
		select {
		case <-u.firstGate:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return nil, errors.New("压测首轮屏障未达到预期并发数")
		}
	}
	id := videoStressTaskID(selection.Body)
	switch u.scenario {
	case "reject_then_accept", "cancel_during_selection", "endpoint_mismatch", "capacity_busy":
		if selection.Account.ID == 4 {
			return retryVideoResponse(429, `{"error":{"code":"QuotaExceeded"}}`), nil
		}
		if selection.Account.ID == 5 {
			return retryVideoResponse(503, `{"error":{"code":"ServiceUnavailable"}}`), nil
		}
	case "all_rejected":
		return retryVideoResponse(429, `{"error":{"code":"QuotaExceeded"}}`), nil
	case "transport_unknown":
		return nil, errors.New("connection lost after request body was written")
	case "generic_500":
		return retryVideoResponse(500, `{"error":{"code":"internal_error"}}`), nil
	case "html_502":
		return retryVideoResponse(502, `<html>Bad Gateway</html>`), nil
	case "upstream_timeout":
		return retryVideoResponse(504, `{"error":{"message":"request timed out"}}`), nil
	case "parameter_error":
		return retryVideoResponse(400, `{"error":{"message":"invalid duration"}}`), nil
	case "moderation_error":
		return retryVideoResponse(403, `{"error":{"message":"content safety rejection"}}`), nil
	case "accepted_error":
		return retryVideoResponse(503, fmt.Sprintf(`{"id":%q,"error":{"code":"ServiceUnavailable"}}`, id)), nil
	case "conflicting_ids":
		return retryVideoResponse(503, fmt.Sprintf(`{"id":%q,"task_id":"different-task","error":{"code":"ServiceUnavailable"}}`, id)), nil
	case "positive_usage":
		return retryVideoResponse(503, `{"usage":{"completion_tokens":17},"error":{"code":"ServiceUnavailable"}}`), nil
	case "media_without_id":
		return retryVideoResponse(503, `{"data":[{"url":"https://cdn.example/video.mp4"}],"error":{"code":"ServiceUnavailable"}}`), nil
	case "success_without_id":
		return retryVideoResponse(200, `{"status":"queued"}`), nil
	}
	return retryVideoResponse(200, fmt.Sprintf(`{"id":%q,"status":"queued"}`, id)), nil
}

// 首次提交也要区分尚未发送与已经受理：先完成的预算事务不能授权取消后的新付费请求。
type videoStressContextRepo struct {
	*videoRetryLifecycleRepo
	cancelAfterSubmittingSave bool
}

func (r *videoStressContextRepo) Get(ctx context.Context, id string) (*VideoTaskRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.videoRetryLifecycleRepo.Get(ctx, id)
}

func (r *videoStressContextRepo) Save(ctx context.Context, task *VideoTaskRecord, release bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := r.videoRetryLifecycleRepo.Save(ctx, task, release)
	if err == nil && r.cancelAfterSubmittingSave && task.Status == "submitting" {
		if cancel, ok := ctx.Value(videoStressCancelKey{}).(context.CancelFunc); ok {
			cancel()
		}
	}
	return err
}

type videoStressCancelReserve struct {
	UsageBillingRepository
	cancelAfterReserve bool
}

func (b videoStressCancelReserve) ReserveBatchImageBalance(ctx context.Context, command *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error) {
	result, err := b.UsageBillingRepository.ReserveBatchImageBalance(ctx, command)
	if err == nil && b.cancelAfterReserve {
		if cancel, ok := ctx.Value(videoStressCancelKey{}).(context.CancelFunc); ok {
			cancel()
		}
	}
	return result, err
}

func TestVideoSubmissionStressCancellationBeforeFirstPOST(t *testing.T) {
	const count = 64
	for _, stage := range []string{"reserve_committed", "submitting_save_committed"} {
		t.Run(stage, func(t *testing.T) {
			s, key, u, billing, logs, repo := newVideoStressFixture(BillingModeVideo, "direct_accept", 0)
			s.repo = &videoStressContextRepo{videoRetryLifecycleRepo: repo, cancelAfterSubmittingSave: stage == "submitting_save_committed"}
			s.billing = videoStressCancelReserve{UsageBillingRepository: billing, cancelAfterReserve: stage == "reserve_committed"}
			errs := make([]error, count)
			runVideoStressWorkers(count, func(i int) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				ctx = context.WithValue(ctx, videoStressCancelKey{}, context.CancelFunc(cancel))
				_, errs[i] = s.Submit(ctx, key, VideoTaskSubmitRequest{Body: []byte(fmt.Sprintf(`{"model":"video-model","prompt":"cancel-first-%d"}`, i)), IdempotencyKey: fmt.Sprintf("cancel-first-%d", i)})
			})
			posts := 0
			for _, attempts := range u.calls {
				posts += len(attempts)
			}
			t.Logf("cancel_stage=%s logical=%d post=%d reserve=%d capture=%d release_before_recovery=%d", stage, count, posts, billing.reserves, billing.captures, billing.releases)
			require.Zero(t, posts, "尚未发送上游请求时取消，不应继续产生付费任务")
			require.Equal(t, count, billing.reserves)
			// 已提交的预算可能要由恢复器清理，但恢复不能把提交前中断任务变成未知受理。
			for id := range repo.tasks {
				task, err := repo.Get(context.Background(), id)
				require.NoError(t, err)
				s.advance(context.Background(), task)
			}
			require.Equal(t, count, billing.releases)
			require.Zero(t, billing.captures)
			require.Empty(t, logs.logs)
			require.Equal(t, u.selected.Load(), u.released.Load())
			require.Zero(t, u.duplicateRelease.Load())
		})
	}
}

func TestVideoSubmissionStressEndpointAndCapacityIsolation(t *testing.T) {
	const count = 64
	for _, scenario := range []string{"endpoint_mismatch", "capacity_busy"} {
		t.Run(scenario, func(t *testing.T) {
			s, key, u, billing, logs, repo := newVideoStressFixture(BillingModeVideo, scenario, count)
			if scenario == "capacity_busy" {
				repo.busyAccount = 5
			}
			errs := make([]error, count)
			responses := make([]*VideoTaskResponse, count)
			runVideoStressWorkers(count, func(i int) {
				responses[i], errs[i] = s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(fmt.Sprintf(`{"model":"video-model","prompt":"isolation-%d"}`, i)), IdempotencyKey: fmt.Sprintf("isolation-%d", i)})
			})
			for i, err := range errs {
				require.NoError(t, err)
				require.Less(t, responses[i].StatusCode, 400)
				task, err := repo.Get(context.Background(), responses[i].LocalTaskID)
				require.NoError(t, err)
				require.Equal(t, int64(6), task.AccountID)
				require.Equal(t, VideoEndpointCompat, task.Target.Endpoint)
				require.Len(t, task.RejectedAttempts, 1)
			}
			for _, attempts := range u.calls {
				require.Equal(t, []int64{4, 6}, attempts)
			}
			require.Equal(t, count, billing.reserves)
			require.Zero(t, billing.releases)
			require.Zero(t, billing.captures)
			require.Empty(t, logs.logs)
			require.Equal(t, int64(count*3), u.selected.Load())
			require.Equal(t, u.selected.Load(), u.released.Load())
			require.Zero(t, u.duplicateRelease.Load())
			t.Logf("logical=%d post=%d skipped_incompatible_or_full=%d reserve=%d slots=%d/%d", count, count*2, count, billing.reserves, u.released.Load(), u.selected.Load())
		})
	}
}

func (u *videoStressTransport) Poll(_ context.Context, _ *Account, _ VideoUpstreamTarget, id string) (*VideoUpstreamResponse, error) {
	u.mu.Lock()
	u.polls++
	u.mu.Unlock()
	response := retryVideoResponse(200, fmt.Sprintf(`{"id":%q,"status":"completed","duration":8,"resolution":"720p","usage":{"completion_tokens":250000}}`, id))
	tokens := int64(250_000)
	response.Metadata = VideoRequestMetadata{DurationSeconds: 8, Resolution: "720p", Tokens: &tokens}
	return response, nil
}

func (u *videoStressTransport) Cancel(_ context.Context, _ *Account, _ VideoUpstreamTarget, id string) (*VideoUpstreamResponse, error) {
	return retryVideoResponse(200, fmt.Sprintf(`{"id":%q,"status":"cancelled"}`, id)), nil
}

type videoStressLogs struct {
	UsageLogRepository
	mu   sync.Mutex
	logs map[string]*UsageLog
}

func (l *videoStressLogs) Create(_ context.Context, log *UsageLog) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, exists := l.logs[log.RequestID]
	l.logs[log.RequestID] = log
	return !exists, nil
}

func newVideoStressFixture(mode BillingMode, scenario string, gateSize int) (*VideoTaskService, *APIKey, *videoStressTransport, *videoLifecycleBilling, *videoStressLogs, *videoRetryLifecycleRepo) {
	s, key, base, billing, _ := newVideoLifecycleFixture(mode)
	enableVideoLifecycleImagePricing(key, base, 7, 5, .15)
	if mode == BillingModeVideoToken {
		enableVideoLifecycleTokenPrepay(key, .3)
	}
	u := &videoStressTransport{template: base.selection, scenario: scenario, calls: map[string][]int64{}, gateSize: gateSize}
	if gateSize > 0 {
		u.firstGate = make(chan struct{})
	}
	logs := &videoStressLogs{logs: map[string]*UsageLog{}}
	repo := &videoRetryLifecycleRepo{videoLifecycleRepo: billing.repo}
	s.repo, s.upstream, s.logs = repo, u, logs
	return s, key, u, billing, logs, repo
}

// 所有请求先在同一屏障起跑，上游首轮再等齐 64 个请求，确保并发不是顺序循环。
func runVideoStressWorkers(count int, run func(int)) {
	var workers sync.WaitGroup
	start := make(chan struct{})
	workers.Add(count)
	for i := 0; i < count; i++ {
		go func(index int) {
			defer workers.Done()
			<-start
			run(index)
		}(i)
	}
	close(start)
	workers.Wait()
}

// 一次明确拒绝后的换号属于合法尝试；压测按逻辑任务核对受理次数、预算和最终费用。
func TestVideoSubmissionStressConcurrentFailoverAndSettlement(t *testing.T) {
	const count = 64
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoToken, BillingModeVideoPerRequest} {
		t.Run(string(mode), func(t *testing.T) {
			s, key, u, billing, logs, repo := newVideoStressFixture(mode, "reject_then_accept", count)
			requests := make([]VideoTaskSubmitRequest, count)
			responses := make([]*VideoTaskResponse, count)
			errs := make([]error, count)
			runVideoStressWorkers(count, func(i int) {
				requests[i] = VideoTaskSubmitRequest{Body: []byte(fmt.Sprintf(`{"model":"video-model","prompt":"job-%d"}`, i)), IdempotencyKey: fmt.Sprintf("job-%d", i)}
				responses[i], errs[i] = s.Submit(context.Background(), key, requests[i])
			})
			for i, err := range errs {
				require.NoError(t, err, "logical request %d", i)
				require.Less(t, responses[i].StatusCode, 400)
				require.Equal(t, []int64{4, 5, 6}, u.calls[string(requests[i].Body)])
				task, err := repo.Get(context.Background(), responses[i].LocalTaskID)
				require.NoError(t, err)
				require.Equal(t, "video-model", task.RequestedModel)
				require.Equal(t, "video-model", task.InternalModel)
				require.Equal(t, int64(6), task.AccountID)
				require.Equal(t, int64(6), task.Hold.VideoAccountID)
				require.Equal(t, float64(1), task.Quote.UnitPrice)
				require.Equal(t, float64(2), task.Hold.BalanceRateMultiplier)
				require.Equal(t, float64(3), task.AccountRateMultiplier)
				require.Len(t, task.RejectedAttempts, 2)
				wantHold := 16.3
				if mode == BillingModeVideoToken {
					wantHold = 2.7
				} else if mode == BillingModeVideoPerRequest {
					wantHold = 2.3
				}
				require.InDelta(t, wantHold, task.Hold.HoldAmount, 1e-9)
			}
			require.Equal(t, count, billing.reserves)
			require.Zero(t, billing.captures)
			require.Zero(t, billing.releases)
			// 仅持久状态共享，使用新协调器实例模拟进程恢复；恢复不得重新 POST。
			restarted := &VideoTaskService{repo: repo, upstream: u, billing: billing, logs: logs, accounts: s.accounts}
			runVideoStressWorkers(count, func(i int) {
				task, err := repo.Get(context.Background(), responses[i].LocalTaskID)
				if err != nil {
					errs[i] = err
					return
				}
				restarted.advance(context.Background(), task)
			})
			wantActual, wantBase := 16.3, 8.0
			if mode == BillingModeVideoToken {
				wantActual, wantBase = .8, .25
			} else if mode == BillingModeVideoPerRequest {
				wantActual, wantBase = 2.3, 1
			}
			for i, err := range errs {
				require.NoError(t, err)
				task, err := repo.Get(context.Background(), responses[i].LocalTaskID)
				require.NoError(t, err)
				require.Equal(t, "completed", task.Status)
				require.Equal(t, "settled", task.BillingStatus)
				require.True(t, task.EffectsDone)
				require.InDelta(t, wantActual, task.BillingResult.ActualAmountUSD, 1e-9)
				log := logs.logs["video_capture:"+task.ID]
				require.NotNil(t, log)
				require.InDelta(t, wantActual, log.ActualCost, 1e-9)
				require.InDelta(t, .3, log.ImageInputCost, 1e-9)
				require.Equal(t, int64(6), log.AccountID)
			}
			for _, capture := range billing.captureCommands {
				require.InDelta(t, wantBase, capture.ActualBaseAmountUSD, 1e-9)
				require.InDelta(t, (wantBase+.3)*3, capture.VideoAccountQuotaCost, 1e-9)
			}
			// 重放、查询与重复恢复交错运行；每个任务有四个并发客户端，均不得再次生成或扣款。
			replayErrs := make([]error, count*4)
			runVideoStressWorkers(count*4, func(index int) {
				i := index / 4
				if index%2 == 0 {
					_, replayErrs[index] = s.Submit(context.Background(), key, requests[i])
				} else {
					_, replayErrs[index] = s.Query(context.Background(), key, responses[i].LocalTaskID, "compat")
				}
			})
			for _, err := range replayErrs {
				require.NoError(t, err)
			}
			require.Len(t, repo.tasks, count)
			require.Equal(t, count, billing.reserves)
			require.Equal(t, count, billing.captures)
			require.Zero(t, billing.releases)
			require.Len(t, logs.logs, count)
			require.Equal(t, count, u.polls)
			for _, attempts := range u.calls {
				require.Equal(t, []int64{4, 5, 6}, attempts)
			}
			require.Equal(t, int64(count*3), u.selected.Load())
			require.Equal(t, u.selected.Load(), u.released.Load())
			require.Zero(t, u.duplicateRelease.Load())
			require.GreaterOrEqual(t, u.peakPosts, count)
			t.Logf("logical=%d rejected_attempts=%d accepted=%d user_errors=0 post=%d reserve=%d capture=%d release=%d usage=%d peak_upstream=%d", count, count*2, count, count*3, billing.reserves, billing.captures, billing.releases, len(logs.logs), u.peakPosts)
		})
	}
}

func TestVideoSubmissionStressConcurrentUnsafeResponsesNeverRepost(t *testing.T) {
	const count = 64
	for _, scenario := range []string{"transport_unknown", "generic_500", "html_502", "upstream_timeout", "parameter_error", "moderation_error", "accepted_error", "conflicting_ids", "positive_usage", "media_without_id", "success_without_id", "all_rejected", "cancel_during_selection"} {
		t.Run(scenario, func(t *testing.T) {
			s, key, u, billing, logs, repo := newVideoStressFixture(BillingModeVideo, scenario, count)
			responses := make([]*VideoTaskResponse, count)
			errs := make([]error, count)
			requests := make([]VideoTaskSubmitRequest, count)
			runVideoStressWorkers(count, func(i int) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				ctx = context.WithValue(ctx, videoStressCancelKey{}, context.CancelFunc(cancel))
				requests[i] = VideoTaskSubmitRequest{Body: []byte(fmt.Sprintf(`{"model":"video-model","prompt":"unsafe-%d"}`, i)), IdempotencyKey: fmt.Sprintf("unsafe-%d", i)}
				responses[i], errs[i] = s.Submit(ctx, key, requests[i])
			})
			unknown := scenario == "transport_unknown" || scenario == "conflicting_ids" || scenario == "success_without_id"
			accepted := scenario == "accepted_error"
			for i, err := range errs {
				require.NoError(t, err)
				require.NotNil(t, responses[i])
				task, err := repo.Get(context.Background(), responses[i].LocalTaskID)
				require.NoError(t, err)
				if unknown || accepted {
					require.Equal(t, "reconciliation", task.BillingStatus)
				} else {
					require.Equal(t, "released", task.BillingStatus)
				}
				// 所有恢复动作只推进既有任务；受理冲突不能绕过查询门禁。
				s.advance(context.Background(), task)
				_, replayErr := s.Submit(context.Background(), key, requests[i])
				_, queryErr := s.Query(context.Background(), key, task.ID, "compat")
				if scenario == "conflicting_ids" {
					require.Error(t, replayErr)
					require.Error(t, queryErr)
				} else {
					require.NoError(t, replayErr)
					require.NoError(t, queryErr)
				}
			}
			wantAttempts := 1
			if scenario == "all_rejected" {
				wantAttempts = 3
			}
			for _, attempts := range u.calls {
				require.Len(t, attempts, wantAttempts)
				seen := map[int64]bool{}
				for _, id := range attempts {
					require.False(t, seen[id], "同一逻辑任务不能重复请求已拒绝账号")
					seen[id] = true
				}
			}
			require.Equal(t, count, billing.reserves)
			if unknown {
				require.Zero(t, billing.captures)
				require.Zero(t, billing.releases)
				require.Empty(t, logs.logs)
			} else if accepted {
				require.Equal(t, count, billing.captures)
				require.Zero(t, billing.releases)
				require.Len(t, logs.logs, count)
			} else {
				require.Zero(t, billing.captures)
				require.Equal(t, count, billing.releases)
				require.Empty(t, logs.logs)
			}
			require.Len(t, repo.tasks, count)
			require.Equal(t, u.selected.Load(), u.released.Load())
			require.Zero(t, u.duplicateRelease.Load())
			require.GreaterOrEqual(t, u.peakPosts, count)
			t.Logf("logical=%d post=%d reserve=%d capture=%d release=%d usage=%d slots=%d/%d", count, count*wantAttempts, billing.reserves, billing.captures, billing.releases, len(logs.logs), u.released.Load(), u.selected.Load())
		})
	}
}

func TestVideoSubmissionStressConcurrentIdempotencyAndPriceGuards(t *testing.T) {
	const count = 128
	t.Run("identical_key_and_payload", func(t *testing.T) {
		s, key, u, billing, _, repo := newVideoStressFixture(BillingModeVideo, "reject_then_accept", 0)
		req := VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","prompt":"same generation"}`), IdempotencyKey: "one-generation"}
		responses := make([]*VideoTaskResponse, count)
		errs := make([]error, count)
		runVideoStressWorkers(count, func(i int) { responses[i], errs[i] = s.Submit(context.Background(), key, req) })
		for i, err := range errs {
			require.NoError(t, err)
			require.Equal(t, responses[0].LocalTaskID, responses[i].LocalTaskID)
		}
		require.Len(t, repo.tasks, 1)
		require.Equal(t, 1, billing.reserves)
		require.Equal(t, []int64{4, 5, 6}, u.calls[string(req.Body)])
		require.Equal(t, u.selected.Load(), u.released.Load())
		require.Zero(t, u.duplicateRelease.Load())
		req.Body = []byte(`{"model":"video-model","prompt":"different generation"}`)
		runVideoStressWorkers(count, func(i int) { _, errs[i] = s.Submit(context.Background(), key, req) })
		for _, err := range errs {
			require.ErrorIs(t, err, ErrVideoTaskConflict)
		}
		require.Equal(t, 1, billing.reserves)
		require.Len(t, u.calls, 1)
		t.Logf("duplicate_clients=%d logical=1 post=3 reserve=1 payload_conflicts=%d", count, count)
	})
	t.Run("missing_price_never_generates", func(t *testing.T) {
		s, key, u, billing, logs, repo := newVideoStressFixture(BillingModeVideo, "reject_then_accept", 0)
		key.Group.ModelPricing = nil
		errs := make([]error, count)
		runVideoStressWorkers(count, func(i int) {
			_, errs[i] = s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(fmt.Sprintf(`{"model":"video-model","prompt":"unpriced-%d"}`, i))})
		})
		for _, err := range errs {
			require.Error(t, err)
		}
		require.Empty(t, repo.tasks)
		require.Empty(t, u.calls)
		require.Empty(t, logs.logs)
		require.Zero(t, billing.reserves)
		require.Zero(t, billing.captures)
		require.Zero(t, billing.releases)
		require.Equal(t, u.selected.Load(), u.released.Load())
		require.Zero(t, u.duplicateRelease.Load())
	})
}
