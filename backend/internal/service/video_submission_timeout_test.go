package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 响应读取阶段即使报超时也不能被误归类成没有收到响应。
func TestVideoSubmissionTimeoutClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"deadline", context.DeadlineExceeded, "timeout"},
		{"wrapped deadline", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), "timeout"},
		{"network timeout", &net.DNSError{IsTimeout: true}, "timeout"},
		{"read timeout", &VideoSubmissionResponseError{Err: context.DeadlineExceeded}, "response"},
		{"wrapped read timeout", fmt.Errorf("wrapped: %w", &VideoSubmissionResponseError{Err: context.DeadlineExceeded}), "response"},
		{"identity", errVideoTaskResponseMismatch, "response"},
		{"connection reset", errors.New("connection reset"), "transport"},
		{"cancelled", context.Canceled, "transport"},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, videoSubmissionFailureKind(tc.err)) })
	}
}

type videoDeadlineUpstream struct {
	*videoLifecycleUpstream
	t       *testing.T
	repo    VideoTaskRepository
	taskID  string
	succeed bool
}

// 等到真正的请求上下文过期再返回，重现截止边界成功响应或错误无法落盘的问题。
func (u *videoDeadlineUpstream) Submit(ctx context.Context, selection *VideoUpstreamSelection) (*VideoUpstreamResponse, error) {
	u.submitted++
	deadline, ok := ctx.Deadline()
	require.True(u.t, ok)
	underlying := u.repo.(*videoContextCheckingRepo).VideoTaskRepository.(*videoLifecycleRepo)
	underlying.mu.Lock()
	var task *VideoTaskRecord
	for _, record := range underlying.tasks {
		task = cloneVideoLifecycleTask(record)
	}
	underlying.mu.Unlock()
	require.NotNil(u.t, task)
	u.taskID = task.ID
	require.True(u.t, task.LeaseUntil.After(deadline.Add(29*time.Second)))
	require.WithinDuration(u.t, deadline, *task.SubmissionDeadlineAt, time.Millisecond)
	<-ctx.Done()
	if u.succeed {
		return &VideoUpstreamResponse{TaskID: "deadline-task", StatusCode: 200, Status: "completed",
			Body: []byte(`{"id":"deadline-task","status":"completed"}`), Metadata: VideoRequestMetadata{DurationSeconds: 4, Resolution: "720p"}}, nil
	}
	return nil, ctx.Err()
}

type videoContextCheckingRepo struct {
	VideoTaskRepository
	expiredSaves int
}

func (r *videoContextCheckingRepo) Save(ctx context.Context, task *VideoTaskRecord, release bool) error {
	if err := ctx.Err(); err != nil {
		r.expiredSaves++
		return err
	}
	return r.VideoTaskRepository.Save(ctx, task, release)
}

func TestVideoSubmissionTimeoutUsesLiveCleanupContext(t *testing.T) {
	for _, succeed := range []bool{false, true} {
		t.Run(fmt.Sprint(succeed), func(t *testing.T) {
			s, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideo)
			s.cfg = &config.Config{Gateway: config.GatewayConfig{VideoCreateTimeoutSeconds: 1}}
			upstream.selection.Metadata.DurationSeconds = 5
			repo := &videoContextCheckingRepo{VideoTaskRepository: s.repo}
			s.repo = repo
			u := &videoDeadlineUpstream{videoLifecycleUpstream: upstream, t: t, repo: repo, succeed: succeed}
			s.upstream = u
			response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
			require.NoError(t, err)
			require.Zero(t, repo.expiredSaves)
			task, err := s.repo.Get(context.Background(), response.LocalTaskID)
			require.NoError(t, err)
			if succeed {
				require.Equal(t, "deadline-task", task.UpstreamTaskID)
				require.Equal(t, "completed", task.Status)
				require.Equal(t, "settled", task.BillingStatus)
				require.True(t, task.EffectsDone)
				require.Equal(t, 1, billing.captures)
				require.InDelta(t, 8, task.BillingResult.ActualAmountUSD, 1e-9)
				require.Len(t, logs.logs, 1)
			} else {
				require.Equal(t, "submission_unknown", task.Status)
				require.Equal(t, "timeout", task.SubmissionFailureKind)
				require.NotNil(t, task.SubmissionUnknownSince)
				require.WithinDuration(t, task.SubmissionUnknownSince.Add(15*time.Minute), task.NextPollAt, time.Millisecond)
				require.Zero(t, billing.releases)
			}
		})
	}
}

func videoTimeoutTask(t *testing.T, mode BillingMode) (*VideoTaskService, *APIKey, *videoLifecycleUpstream, *videoLifecycleBilling, *VideoTaskRecord) {
	t.Helper()
	s, key, u, b, _ := newVideoLifecycleFixture(mode)
	u.submit = nil
	u.submitErr = context.DeadlineExceeded
	result, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`), IdempotencyKey: "timeout-once"})
	require.NoError(t, err)
	task, err := s.repo.Get(context.Background(), result.LocalTaskID)
	require.NoError(t, err)
	return s, key, u, b, task
}

// 核对窗口边界精确到持久起点，重复恢复或重复客户端创建不会重扣或重发。
func TestVideoSubmissionTimeoutGraceAndIdempotentRelease(t *testing.T) {
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoToken, BillingModeVideoPerRequest} {
		t.Run(string(mode), func(t *testing.T) {
			s, key, u, b, task := videoTimeoutTask(t, mode)
			anchor := *task.SubmissionUnknownSince
			require.True(t, s.recoverUnknownVideoSubmission(context.Background(), task, anchor.Add(15*time.Minute-time.Nanosecond)))
			require.Zero(t, b.releases)
			require.Equal(t, anchor, *task.SubmissionUnknownSince)
			require.Equal(t, anchor.Add(15*time.Minute), task.NextPollAt)
			require.True(t, s.recoverUnknownVideoSubmission(context.Background(), task, anchor.Add(15*time.Minute)))
			require.Equal(t, "failed", task.Status)
			require.Equal(t, "released", task.BillingStatus)
			require.True(t, task.EffectsDone)
			s.advance(context.Background(), task)
			_, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`), IdempotencyKey: "timeout-once"})
			require.NoError(t, err)
			require.Equal(t, 1, b.releases)
			require.Zero(t, b.captures)
			require.Equal(t, 1, u.submitted)
			require.Zero(t, u.polls)
		})
	}
}

// 有任何归属或响应证据以及旧未知记录均不得进入新的无 ID 超时退款策略。
func TestVideoSubmissionTimeoutDoesNotReleaseResponseEvidence(t *testing.T) {
	cases := map[string]func(*VideoTaskRecord){
		"known id":           func(v *VideoTaskRecord) { v.UpstreamTaskID = "known" },
		"response status":    func(v *VideoTaskRecord) { v.ResponseStatus = 200 },
		"create status":      func(v *VideoTaskRecord) { v.CreateResponseStatus = 200 },
		"success without id": func(v *VideoTaskRecord) { v.CreateResponse = []byte(`{"status":"completed"}`) },
		"invalid id":         func(v *VideoTaskRecord) { v.CreateResponse = []byte(`{"id":42}`) },
		"conflicting ids":    func(v *VideoTaskRecord) { v.RawResponse = []byte(`{"id":"a","task_id":"b"}`) },
		"media":              func(v *VideoTaskRecord) { v.VideoURL = "https://media.example/a.mp4" },
		"usage":              func(v *VideoTaskRecord) { value := int64(1); v.Metadata.Tokens = &value },
		"accepted":           func(v *VideoTaskRecord) { v.UpstreamStatus = "queued" },
		"read failure":       func(v *VideoTaskRecord) { v.SubmissionFailureKind = "response" },
		"connection failure": func(v *VideoTaskRecord) { v.SubmissionFailureKind = "transport" },
		"legacy unknown":     func(v *VideoTaskRecord) { v.SubmissionFailureKind = ""; v.SubmissionUnknownSince = nil },
		"legacy submitting": func(v *VideoTaskRecord) {
			v.Status = "submitting"
			v.SubmissionFailureKind = ""
			v.SubmissionStartedAt = nil
			v.SubmissionDeadlineAt = nil
		},
		"invalid origin": func(v *VideoTaskRecord) { v.SubmissionUnknownSince = &time.Time{} },
		"completed":      func(v *VideoTaskRecord) { v.Status = "completed" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, _, _, b, task := videoTimeoutTask(t, BillingModeVideo)
			mutate(task)
			require.False(t, s.recoverUnknownVideoSubmission(context.Background(), task, time.Now().Add(24*time.Hour)))
			require.Zero(t, b.releases)
		})
	}
}

func TestVideoSubmissionTimeoutCrashRecoveryKeepsDeadline(t *testing.T) {
	s, _, u, b, task := videoTimeoutTask(t, BillingModeVideo)
	deadline := time.Now().UTC().Add(-10 * time.Minute)
	started := deadline.Add(-180 * time.Second)
	task.Status, task.SubmissionFailureKind, task.SubmissionUnknownSince = "submitting", "", nil
	task.SubmissionStartedAt = &started
	task.SubmissionDeadlineAt = &deadline
	require.NoError(t, s.repo.Save(context.Background(), task, false))
	s.advance(context.Background(), task)
	require.Equal(t, "interrupted", task.SubmissionFailureKind)
	require.Equal(t, deadline, *task.SubmissionUnknownSince)
	require.Equal(t, deadline.Add(15*time.Minute), task.NextPollAt)
	require.Zero(t, b.releases)
	fresh, err := s.repo.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.True(t, s.recoverUnknownVideoSubmission(context.Background(), fresh, deadline.Add(15*time.Minute)))
	require.Equal(t, 1, b.releases)
	require.Equal(t, 1, u.submitted)
	require.Zero(t, u.polls)
}

func TestVideoSubmissionTimeoutReleaseFailureAndStateSaveRecovery(t *testing.T) {
	for _, failSave := range []bool{false, true} {
		t.Run(fmt.Sprint(failSave), func(t *testing.T) {
			s, _, u, b, task := videoTimeoutTask(t, BillingModeVideo)
			past := time.Now().UTC().Add(-16 * time.Minute)
			task.SubmissionUnknownSince = &past
			require.NoError(t, s.repo.Save(context.Background(), task, false))
			if failSave {
				s.repo = &videoSubmissionSaveFailureRepo{VideoTaskRepository: s.repo}
			} else {
				b.releaseErr = errors.New("temporary billing error")
			}
			s.advance(context.Background(), task)
			require.Zero(t, b.releases)
			fresh, err := s.repo.Get(context.Background(), task.ID)
			require.NoError(t, err)
			require.Equal(t, "failed", fresh.Status)
			require.Equal(t, "reconciliation", fresh.BillingStatus)
			require.False(t, fresh.EffectsDone)
			b.releaseErr = nil
			s.advance(context.Background(), fresh)
			s.advance(context.Background(), fresh)
			require.Equal(t, 1, b.releases)
			require.Equal(t, "released", fresh.BillingStatus)
			require.True(t, fresh.EffectsDone)
			require.Equal(t, 1, u.submitted)
		})
	}
}

// 多个恢复者持有独立副本时重复关闭超时任务，资金仍只释放一次；真实租约并发另由仓储覆盖。
func TestVideoSubmissionTimeoutConcurrentRecovery(t *testing.T) {
	s, _, u, b, task := videoTimeoutTask(t, BillingModeVideo)
	past := time.Now().UTC().Add(-16 * time.Minute)
	task.SubmissionUnknownSince = &past
	require.NoError(t, s.repo.Save(context.Background(), task, false))
	var workers sync.WaitGroup
	for i := 0; i < 64; i++ {
		copy := cloneVideoLifecycleTask(task)
		workers.Add(1)
		go func() { defer workers.Done(); s.advance(context.Background(), copy) }()
	}
	workers.Wait()
	fresh, err := s.repo.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, "released", fresh.BillingStatus)
	require.True(t, fresh.EffectsDone)
	require.Equal(t, 1, b.releases)
	require.Zero(t, b.captures)
	require.Equal(t, 1, u.submitted)
}

// 供应商最终用量无效时不能沿用请求五秒扣费；后续合法四秒响应可以恢复并退差。
func TestVideoCompletionMetadataErrorRecoversBeforeCharge(t *testing.T) {
	s, key, u, b, logs := newVideoLifecycleFixture(BillingModeVideo)
	u.selection.Metadata.DurationSeconds = 5
	result, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	task, err := s.repo.Get(context.Background(), result.LocalTaskID)
	require.NoError(t, err)
	u.poll = &VideoUpstreamResponse{StatusCode: 200, TaskID: "upstream-task", Status: "completed", MetadataError: "invalid seconds",
		Metadata: VideoRequestMetadata{DurationSeconds: 999, Resolution: "720p"}}
	s.advance(context.Background(), task)
	require.Equal(t, "reconciliation", task.BillingStatus)
	require.True(t, task.PricingMismatch)
	require.Equal(t, float64(5), task.Metadata.DurationSeconds)
	require.Zero(t, b.captures)
	// 下一次只给终态而无可信元数据，不能用创建时五秒补成一次错误结算。
	u.poll = &VideoUpstreamResponse{StatusCode: 200, TaskID: "upstream-task", Status: "completed"}
	s.advance(context.Background(), task)
	require.True(t, task.PricingMismatch)
	require.NotEmpty(t, task.ResponseMetadataError)
	require.Zero(t, b.captures)
	u.poll.MetadataError = ""
	u.poll.Metadata.DurationSeconds = 4
	u.poll.Metadata.Resolution = "720p"
	s.advance(context.Background(), task)
	require.Equal(t, "settled", task.BillingStatus)
	require.False(t, task.PricingMismatch)
	require.Equal(t, float64(4), task.Metadata.DurationSeconds)
	require.InDelta(t, 8, task.BillingResult.ActualAmountUSD, 1e-9)
	require.Equal(t, 1, b.captures)
	require.Len(t, logs.logs, 1)
}

// 已存在真实档位冲突时，后续格式错误及缺字段包不能把历史高价产物伪装成请求低价档位。
func TestVideoCompletionMetadataErrorDoesNotErasePriorPriceMismatch(t *testing.T) {
	s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideo)
	result, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	task, err := s.repo.Get(context.Background(), result.LocalTaskID)
	require.NoError(t, err)
	for _, response := range []*VideoUpstreamResponse{
		{Status: "completed", Metadata: VideoRequestMetadata{Resolution: "2k", DurationSeconds: 5}},
		{Status: "completed", MetadataError: "invalid size"},
		{Status: "completed", Metadata: VideoRequestMetadata{DurationSeconds: 5}},
	} {
		response.StatusCode = 200
		response.TaskID = "upstream-task"
		u.poll = response
		s.advance(context.Background(), task)
		require.True(t, task.PricingMismatch)
		require.Equal(t, "reconciliation", task.BillingStatus)
		require.Zero(t, b.captures)
	}
}

// 带有响应阶段错误或响应证据的超时只能核对，不能在十五分钟后自动退回。
func TestVideoSubmissionTimeoutReadFailureNeverAutoReleases(t *testing.T) {
	for _, withResponse := range []bool{false, true} {
		t.Run(fmt.Sprint(withResponse), func(t *testing.T) {
			s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideo)
			u.submitErr = &VideoSubmissionResponseError{Err: context.DeadlineExceeded}
			u.submit = nil
			if withResponse {
				u.submit = &VideoUpstreamResponse{StatusCode: 200, TaskID: "accepted", Body: []byte(`{"id":"accepted"}`)}
			}
			result, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
			require.NoError(t, err)
			task, err := s.repo.Get(context.Background(), result.LocalTaskID)
			require.NoError(t, err)
			require.Equal(t, "response", task.SubmissionFailureKind)
			if withResponse {
				require.Equal(t, "accepted", task.UpstreamTaskID)
				require.WithinDuration(t, time.Now().Add(15*time.Second), task.NextPollAt, time.Second)
			}
			require.False(t, s.recoverUnknownVideoSubmission(context.Background(), task, time.Now().Add(24*time.Hour)))
			require.Zero(t, b.releases)
		})
	}
}

// 配置越长，创建租约必须一起延长；短预算也保留原三分钟最低租约。
func TestVideoSubmissionTimeoutConfigurationAndLease(t *testing.T) {
	for _, tc := range []struct {
		seconds       int
		budget, lease time.Duration
	}{
		{0, 180 * time.Second, 210 * time.Second}, {1, time.Second, 3 * time.Minute},
		{300, 5 * time.Minute, 330 * time.Second}, {600, 10 * time.Minute, 630 * time.Second},
	} {
		s := &VideoTaskService{cfg: &config.Config{Gateway: config.GatewayConfig{VideoCreateTimeoutSeconds: tc.seconds}}}
		require.Equal(t, tc.budget, s.videoCreateTimeout())
		require.Equal(t, tc.lease, s.videoSubmissionLease())
	}
}
