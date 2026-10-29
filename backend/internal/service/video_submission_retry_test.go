package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 拒绝证据必须区分可换号的准入失败与可能已经产生费用的未知结果。
func TestVideoCreationDefinitelyRejected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"credential", 401, `{"error":{"message":"invalid API key"}}`, true},
		{"quota", 429, `{"error":{"code":"QuotaExceeded"}}`, true},
		{"busy", 503, `{"error":{"code":"ServiceUnavailable","message":"server busy"}}`, true},
		{"wan trace is not task", 503, `{"request_id":"trace","code":"ServiceUnavailable"}`, true},
		{"model unavailable", 404, `{"error":{"code":"model_not_found"}}`, true},
		{"validation", 400, `{"error":{"message":"invalid duration"}}`, false},
		{"moderation", 403, `{"error":{"message":"content safety rejection"}}`, false},
		{"html proxy", 502, `<html>Bad Gateway</html>`, false},
		{"unknown server", 500, `{"error":{"code":"internal_error"}}`, false},
		{"timeout", 504, `{"error":{"message":"request timed out"}}`, false},
		{"accepted", 429, `{"id":"task-1","error":{"code":"QuotaExceeded"}}`, false},
		{"accepted invalid id", 429, `{"task_id":42}`, false},
		{"minimax accepted", 503, `{"task":{"id":"task-1"},"error":{"code":"ServiceUnavailable"}}`, false},
		{"accepted no id", 503, `{"accepted":true,"error":{"code":"ServiceUnavailable"}}`, false},
		{"queued no id", 503, `{"status":"queued","error":{"code":"ServiceUnavailable"}}`, false},
		{"minimax status no id", 503, `{"task":{"status":"completed"},"error":{"code":"server_busy"}}`, false},
		{"success no id", 503, `{"status":"success","error":{"code":"server_busy"}}`, false},
		{"wan succeeded no id", 503, `{"output":{"task_status":"SUCCEEDED"},"error":{"code":"server_busy"}}`, false},
		{"kling result no id", 503, `{"data":{"task_result":{"videos":[{"url":"https://cdn.example/video.mp4"}]}},"error":{"code":"server_busy"}}`, false},
		{"minimax media no id", 503, `{"task":{"content":{"url":"https://cdn.example/video.mp4"}},"error":{"code":"server_busy"}}`, false},
		{"compat result no id", 503, `{"data":[{"url":"https://cdn.example/video.mp4"}],"error":{"code":"server_busy"}}`, false},
		{"minimax usage no id", 503, `{"task":{"usage":{"output_seconds":5}},"error":{"code":"server_busy"}}`, false},
		{"nested task no id", 503, `{"data":{"task":{"status":"running"}},"error":{"code":"server_busy"}}`, false},
		{"nested task id", 503, `{"data":{"task":{"id":"accepted"}},"error":{"code":"server_busy"}}`, false},
		{"wan usage no id", 503, `{"output":{"usage":{"completion_tokens":10}},"error":{"code":"server_busy"}}`, false},
		{"input usage no id", 429, `{"usage":{"prompt_tokens":10},"error":{"code":"QuotaExceeded"}}`, false},
		{"zero usage is placeholder", 503, `{"usage":{"completion_tokens":0},"error":{"code":"server_busy"}}`, true},
		{"prompt echo is not evidence", 503, `{"input":{"status":"completed","video_url":"https://input.example/video.mp4"},"prompt":{"task":{"status":"completed"}},"error":{"code":"server_busy"}}`, true},
		{"success missing id", 200, `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, VideoCreationDefinitelyRejected(tc.status, []byte(tc.body)))
		})
	}
}

// 夹具只替换持久归属切换，真实容量锁和财务事务由仓储集成测试覆盖。
type videoRetryLifecycleRepo struct {
	*videoLifecycleRepo
	switchErr       error
	commitThenError bool
	busyAccount     int64
	afterSwitch     func()
	afterSave       func(*VideoTaskRecord)
}

func (r *videoRetryLifecycleRepo) Save(ctx context.Context, task *VideoTaskRecord, release bool) error {
	err := r.videoLifecycleRepo.Save(ctx, task, release)
	if err == nil && r.afterSave != nil {
		r.afterSave(task)
	}
	return err
}

func (r *videoRetryLifecycleRepo) SwitchRejected(_ context.Context, id, lease string, target VideoUpstreamTarget, meta VideoRequestMetadata, rate float64, _ int) (*VideoTaskRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.tasks[id]
	if t == nil || t.LeaseToken != lease || !VideoTaskCanRetrySubmission(t) {
		return nil, ErrVideoTaskConflict
	}
	if target.AccountID == r.busyAccount {
		return nil, ErrVideoTaskBusy
	}
	if r.switchErr != nil && !r.commitThenError {
		return nil, r.switchErr
	}
	t.RejectedAttempts = append(t.RejectedAttempts, VideoRejectedAttempt{Target: t.Target, StatusCode: t.CreateResponseStatus})
	t.AccountID, t.Target, t.Metadata, t.AccountRateMultiplier = target.AccountID, target, meta, rate
	t.Hold.VideoAccountID = target.AccountID
	t.Status, t.UpstreamStatus, t.ErrorMessage = "prepared", "", ""
	t.CompletedAt, t.CreateResponse, t.RawResponse = nil, nil, nil
	t.CreateResponseStatus, t.ResponseStatus = 0, 0
	if r.afterSwitch != nil {
		r.afterSwitch()
	}
	return cloneVideoLifecycleTask(t), r.switchErr
}

type videoRetryUpstream struct {
	*videoLifecycleUpstream
	selections  []*VideoUpstreamSelection
	responses   []*VideoUpstreamResponse
	errors      []error
	attempted   []int64
	afterSelect func()
}

func (u *videoRetryUpstream) SelectAccount(_ context.Context, _ *APIKey, r VideoTaskSubmitRequest) (*VideoUpstreamSelection, error) {
	for _, s := range u.selections {
		if _, excluded := r.ExcludedAccountIDs[s.Account.ID]; !excluded {
			if len(u.attempted) > 0 && u.afterSelect != nil {
				u.afterSelect()
			}
			return s, nil
		}
	}
	return nil, ErrNoAvailableAccounts
}
func (u *videoRetryUpstream) Submit(_ context.Context, s *VideoUpstreamSelection) (*VideoUpstreamResponse, error) {
	i := len(u.attempted)
	u.attempted = append(u.attempted, s.Account.ID)
	if i < len(u.errors) && u.errors[i] != nil {
		return nil, u.errors[i]
	}
	return u.responses[i], nil
}
func newVideoRetryFixture() (*VideoTaskService, *APIKey, *videoRetryUpstream, *videoLifecycleBilling, *videoLifecycleLogs, *videoRetryLifecycleRepo) {
	s, k, base, b, l := newVideoLifecycleFixture(BillingModeVideo)
	r := &videoRetryLifecycleRepo{videoLifecycleRepo: b.repo}
	u := &videoRetryUpstream{videoLifecycleUpstream: base}
	for i := 0; i < 3; i++ {
		v := *base.selection
		a := *base.selection.Account
		a.ID += int64(i)
		v.Account = &a
		v.Target.AccountID = a.ID
		u.selections = append(u.selections, &v)
	}
	s.repo, s.upstream = r, u
	return s, k, u, b, l, r
}
func retryVideoResponse(status int, body string) *VideoUpstreamResponse {
	r := ExtractVideoUpstreamResponse(VideoEndpointCompat, []byte(body), "")
	r.StatusCode, r.Body = status, []byte(body)
	return r
}

func TestVideoTaskRejectedFailoverKeepsOneHoldAndOneSettlement(t *testing.T) {
	s, k, u, b, logs, r := newVideoRetryFixture()
	// 新账号允许映射到不同上游模型，但用户原始模型和首个报价不能因换号改变。
	final := u.selections[2]
	final.Target.Model, final.Metadata.Model, final.BillingModel = "final-mapped-model", "final-mapped-model", "final-mapped-model"
	final.Body = []byte(`{"model":"final-mapped-model"}`)
	finalRate := 3.0
	final.Account.RateMultiplier = &finalRate
	u.responses = []*VideoUpstreamResponse{retryVideoResponse(429, `{"error":{"code":"QuotaExceeded"}}`), retryVideoResponse(503, `{"error":{"code":"ServiceUnavailable"}}`), retryVideoResponse(200, `{"id":"accepted-3","status":"queued"}`)}
	req := VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model"}`), IdempotencyKey: "one-user-task"}
	response, err := s.Submit(context.Background(), k, req)
	require.NoError(t, err)
	require.Equal(t, []int64{4, 5, 6}, u.attempted)
	require.Equal(t, 1, b.reserves)
	require.Zero(t, b.releases)
	task, err := r.Get(context.Background(), response.LocalTaskID)
	require.NoError(t, err)
	require.Equal(t, int64(6), task.AccountID)
	require.Equal(t, int64(6), task.Hold.VideoAccountID)
	require.Len(t, task.RejectedAttempts, 2)
	require.Equal(t, float64(16), task.Hold.HoldAmount)
	require.Equal(t, float64(1), task.Quote.UnitPrice)
	require.Equal(t, "video-model", task.InternalModel)
	require.Equal(t, "video-model", task.RequestedModel)
	require.Equal(t, "final-mapped-model", task.Target.Model)
	require.Equal(t, task.Target.Model, task.Metadata.Model)
	require.Equal(t, finalRate, task.AccountRateMultiplier)
	u.poll = retryVideoResponse(200, `{"id":"accepted-3","status":"completed","video":{"url":"https://cdn.example.test/video.mp4"},"duration":8}`)
	s.advance(context.Background(), task)
	s.advance(context.Background(), task)
	require.Equal(t, 1, b.captures)
	require.Equal(t, int64(6), b.captureCommands[0].VideoAccountID)
	require.Equal(t, float64(24), b.captureCommands[0].VideoAccountQuotaCost)
	require.Len(t, logs.logs, 1)
	log := logs.logs["video_capture:"+task.ID]
	require.Equal(t, int64(6), log.AccountID)
	require.Equal(t, "video-model", log.Model)
	require.Equal(t, "video-model", log.RequestedModel)
	require.Equal(t, "final-mapped-model", *log.UpstreamModel)
	require.Equal(t, finalRate, *log.AccountRateMultiplier)
	require.Equal(t, float64(16), log.ActualCost)
	_, err = s.Submit(context.Background(), k, req)
	require.NoError(t, err)
	require.Len(t, u.attempted, 3)
	require.Len(t, r.tasks, 1)
}

// 零配置沿用现有 OpenAI 网关默认值；明确配置的切换预算不能被媒体重试绕过。
func TestVideoTaskRejectedFailoverRespectsSwitchBudget(t *testing.T) {
	for _, tc := range []struct {
		name, body       string
		budget, attempts int
	}{
		{"default zero", `{"error":"quota"}`, 0, 3},
		{"one switch", `{"error":"quota"}`, 1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, k, u, b, _, _ := newVideoRetryFixture()
			s.cfg = &config.Config{}
			s.cfg.Gateway.MaxAccountSwitches = tc.budget
			u.responses = []*VideoUpstreamResponse{retryVideoResponse(429, tc.body), retryVideoResponse(429, tc.body), retryVideoResponse(429, tc.body)}
			_, err := s.Submit(context.Background(), k, VideoTaskSubmitRequest{Body: []byte(`{}`)})
			require.NoError(t, err)
			require.Len(t, u.attempted, tc.attempts)
			require.Equal(t, 1, b.reserves)
			require.Equal(t, 1, b.releases)
		})
	}
}

func TestVideoTaskRejectedFailoverExhaustedRefundsOnlyOnce(t *testing.T) {
	s, k, u, b, _, r := newVideoRetryFixture()
	u.responses = []*VideoUpstreamResponse{retryVideoResponse(429, `{"error":"quota"}`), retryVideoResponse(429, `{"error":"quota"}`), retryVideoResponse(429, `{"error":"quota"}`)}
	response, err := s.Submit(context.Background(), k, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	require.Equal(t, 429, response.StatusCode)
	require.Equal(t, 1, b.reserves)
	require.Equal(t, 1, b.releases)
	task, err := r.Get(context.Background(), response.LocalTaskID)
	require.NoError(t, err)
	s.advance(context.Background(), task)
	require.Equal(t, 1, b.releases)
	require.Len(t, u.attempted, 3)
	require.Len(t, r.tasks, 1)
}

func TestVideoTaskRejectedFailoverUnknownSecondAttemptNeverReposts(t *testing.T) {
	s, k, u, b, _, r := newVideoRetryFixture()
	u.responses = []*VideoUpstreamResponse{retryVideoResponse(429, `{"error":"quota"}`)}
	u.errors = []error{nil, errors.New("connection lost after write")}
	response, err := s.Submit(context.Background(), k, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	task, err := r.Get(context.Background(), response.LocalTaskID)
	require.NoError(t, err)
	require.Equal(t, "submission_unknown", task.Status)
	require.Equal(t, "reconciliation", task.BillingStatus)
	require.Equal(t, int64(5), task.AccountID)
	s.advance(context.Background(), task)
	require.Len(t, u.attempted, 2)
	require.Equal(t, 1, b.reserves)
	require.Zero(t, b.releases)
}

func TestVideoTaskRejectedFailoverCapacitySkipsWithoutAdditionalHold(t *testing.T) {
	s, k, u, b, _, r := newVideoRetryFixture()
	r.busyAccount = 5
	u.responses = []*VideoUpstreamResponse{retryVideoResponse(401, `{"error":"bad credential"}`), retryVideoResponse(200, `{"id":"accepted"}`)}
	_, err := s.Submit(context.Background(), k, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	require.Equal(t, []int64{4, 6}, u.attempted)
	require.Equal(t, 1, b.reserves)
}

// 产物和用量与 HTTP 拒绝冲突时，保持既定无 ID 退款合同，但绝不能通过换号重复生成。
func TestVideoTaskRejectedFailoverAcceptanceEvidenceNeverReposts(t *testing.T) {
	for _, fragment := range []string{
		`"task":{"status":"completed","content":{"url":"https://cdn.example/video.mp4"},"usage":{"output_seconds":5}}`,
		`"task":{"status":"running"}`,
		`"status":"success"`,
		`"output":{"task_status":"SUCCEEDED"}`,
		`"data":{"task_result":{"videos":[{"url":"https://cdn.example/video.mp4"}]}}`,
		`"task":{"content":{"url":"https://cdn.example/video.mp4"}}`,
		`"task":{"usage":{"output_seconds":5}}`,
		`"output":{"usage":{"completion_tokens":10}}`,
	} {
		t.Run(fragment, func(t *testing.T) {
			s, k, u, b, _, r := newVideoRetryFixture()
			body := `{"error":{"code":"server_busy"},` + fragment + `}`
			u.responses = []*VideoUpstreamResponse{retryVideoResponse(503, body)}
			response, err := s.Submit(context.Background(), k, VideoTaskSubmitRequest{Body: []byte(`{}`)})
			require.NoError(t, err)
			require.Equal(t, 503, response.StatusCode)
			require.Equal(t, []int64{4}, u.attempted)
			require.Equal(t, 1, b.reserves)
			require.Equal(t, 1, b.releases)
			require.Zero(t, b.captures)
			task, err := r.Get(context.Background(), response.LocalTaskID)
			require.NoError(t, err)
			require.Equal(t, "released", task.BillingStatus)
			require.Empty(t, task.RejectedAttempts)
			s.advance(context.Background(), task)
			require.Equal(t, 1, b.releases)
			require.Equal(t, []int64{4}, u.attempted)
		})
	}
}

// 客户端在重新选号、容量事务或提交状态写入期间取消，均不得生成第二份产物。
func TestVideoTaskRejectedFailoverCancellationBeforeSecondPOSTReleasesHold(t *testing.T) {
	for _, stage := range []string{"selection", "migration", "submitting save"} {
		t.Run(stage, func(t *testing.T) {
			s, k, u, b, _, r := newVideoRetryFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch stage {
			case "selection":
				u.afterSelect = cancel
			case "migration":
				r.afterSwitch = cancel
			case "submitting save":
				r.afterSave = func(task *VideoTaskRecord) {
					if task.AccountID == 5 && task.Status == "submitting" {
						cancel()
					}
				}
			}
			releasedSlots := 0
			for _, selected := range u.selections {
				selected.Release = func() { releasedSlots++ }
			}
			u.responses = []*VideoUpstreamResponse{retryVideoResponse(429, `{"error":"quota"}`)}
			req := VideoTaskSubmitRequest{Body: []byte(`{}`), IdempotencyKey: "cancel-before-retry"}
			response, err := s.Submit(ctx, k, req)
			require.NoError(t, err)
			require.ErrorIs(t, ctx.Err(), context.Canceled)
			require.Equal(t, 429, response.StatusCode)
			require.Equal(t, []int64{4}, u.attempted)
			require.Equal(t, 2, releasedSlots)
			task, err := r.Get(context.Background(), response.LocalTaskID)
			require.NoError(t, err)
			require.Equal(t, int64(5), task.AccountID)
			require.Equal(t, "failed", task.Status)
			require.Equal(t, "released", task.BillingStatus)
			require.Empty(t, task.UpstreamTaskID)
			require.NotNil(t, task.CompletedAt)
			require.Equal(t, 1, b.reserves)
			require.Equal(t, 1, b.releases)
			require.Zero(t, b.captures)
			s.advance(context.Background(), task)
			_, err = s.Submit(context.Background(), k, req)
			require.NoError(t, err)
			require.Equal(t, 1, b.reserves)
			require.Equal(t, 1, b.releases)
			require.Equal(t, []int64{4}, u.attempted)
		})
	}
}

func TestVideoTaskRejectedFailoverCommitUncertaintyNeverSendsSecondPOST(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "committed"}[committed], func(t *testing.T) {
			s, k, u, b, _, r := newVideoRetryFixture()
			r.switchErr = errors.New("database connection lost")
			r.commitThenError = committed
			u.responses = []*VideoUpstreamResponse{retryVideoResponse(429, `{"error":"quota"}`)}
			response, err := s.Submit(context.Background(), k, VideoTaskSubmitRequest{Body: []byte(`{}`)})
			require.NoError(t, err)
			require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
			require.Len(t, u.attempted, 1)
			task, err := r.Get(context.Background(), response.LocalTaskID)
			require.NoError(t, err)
			s.advance(context.Background(), task)
			require.Equal(t, 1, b.reserves)
			require.Equal(t, 1, b.releases)
		})
	}
}
