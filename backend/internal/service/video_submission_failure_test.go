package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 创建错误且无上游归属时按平台退款策略处理，HTTP 正文与状态继续原样返回客户端。
func TestVideoSubmissionHTTPFailureReleasesWithoutRepost(t *testing.T) {
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoPerRequest, BillingModeVideoToken} {
		for _, status := range []int{400, 408, 429, 500, 502, 503, 504, 524} {
			t.Run(fmt.Sprintf("%s/%d", mode, status), func(t *testing.T) {
				s, key, u, b, logs := newVideoLifecycleFixture(mode)
				body := []byte(`{"error":{"message":"Service temporarily unavailable","type":"api_error"}}`)
				if status == 502 {
					body = []byte("<html>Bad Gateway</html>")
				} else if status == 504 {
					body = nil
				}
				u.submit = &VideoUpstreamResponse{StatusCode: status, Body: body}
				req := VideoTaskSubmitRequest{Body: []byte(`{}`), InboundProtocol: "openai_videos", IdempotencyKey: "failure-once"}
				response, err := s.Submit(context.Background(), key, req)
				require.NoError(t, err)
				require.Equal(t, status, response.StatusCode)
				require.Equal(t, body, response.Body)
				task, err := s.repo.Get(context.Background(), response.LocalTaskID)
				require.NoError(t, err)
				require.Equal(t, "failed", task.Status)
				require.Equal(t, "released", task.BillingStatus)
				require.NotNil(t, task.CompletedAt)
				require.True(t, task.EffectsDone)
				require.Equal(t, 1, b.releases)
				// 重复创建、恢复、查询只重放结果，不能再次发起生成或重复退款。
				_, err = s.Submit(context.Background(), key, req)
				require.NoError(t, err)
				s.advance(context.Background(), task)
				_, err = s.Query(context.Background(), key, task.ID, "openai_videos")
				require.NoError(t, err)
				require.Equal(t, 1, b.releases)
				require.Equal(t, 1, u.submitted)
				require.Zero(t, u.polls)
				require.Zero(t, b.captures)
				require.Empty(t, logs.logs)
			})
		}
	}
}

// 已提供任务 ID 的错误包不能丢弃归属，查询错误也不能改成创建失败并退款。
func TestVideoSubmissionErrorWithTaskIDKeepsPolling(t *testing.T) {
	for _, status := range []int{400, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideoPerRequest)
			u.submit.StatusCode = status
			response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
			require.NoError(t, err)
			task, err := s.repo.Get(context.Background(), response.LocalTaskID)
			require.NoError(t, err)
			require.Equal(t, "upstream-task", task.UpstreamTaskID)
			require.Equal(t, "submission_unknown", task.Status)
			u.poll = &VideoUpstreamResponse{StatusCode: 503}
			s.advance(context.Background(), task)
			require.Zero(t, b.releases)
			u.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "failed"}
			s.advance(context.Background(), task)
			require.Equal(t, "failed", task.Status)
			require.Equal(t, "released", task.BillingStatus)
			require.Equal(t, 1, b.releases)
			require.Equal(t, 1, u.submitted)
		})
	}
}

// 失败与取消是明确的任务终态，不能被非 2xx HTTP 或空 ID 分支覆盖。
func TestVideoSubmissionTerminalRejectionReleases(t *testing.T) {
	for _, status := range []string{"failed", "cancelled"} {
		for _, code := range []int{200, 503} {
			t.Run(fmt.Sprintf("%s/%d", status, code), func(t *testing.T) {
				s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideo)
				u.submit = &VideoUpstreamResponse{StatusCode: code, Status: status}
				response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
				require.NoError(t, err)
				task, err := s.repo.Get(context.Background(), response.LocalTaskID)
				require.NoError(t, err)
				require.Equal(t, status, task.Status)
				require.Equal(t, "released", task.BillingStatus)
				require.Equal(t, 1, b.releases)
			})
		}
	}
}

// 历史修复只信创建快照；后续查询 503 不能污染原本已成功受理但 ID 落库丢失的任务。
func TestVideoSubmissionHistoricalRecoveryUsesCreateResponse(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(fmt.Sprint(rejected), func(t *testing.T) {
			s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideoPerRequest)
			response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
			require.NoError(t, err)
			task, _ := s.repo.Get(context.Background(), response.LocalTaskID)
			task.Status, task.BillingStatus, task.UpstreamTaskID = "submission_unknown", "reconciliation", ""
			task.RawResponse, task.ResponseStatus = []byte(`{"error":{"message":"unavailable"}}`), 503
			if rejected {
				task.CreateResponse, task.CreateResponseStatus = task.RawResponse, 503
			}
			require.NoError(t, s.repo.Save(context.Background(), task, true))
			u.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "processing"}
			s.advance(context.Background(), task)
			if rejected {
				require.Equal(t, "failed", task.Status)
				require.Equal(t, "released", task.BillingStatus)
				require.Equal(t, 1, b.releases)
				require.Zero(t, u.polls)
			} else {
				require.Equal(t, "upstream-task", task.UpstreamTaskID)
				require.Equal(t, "processing", task.Status)
				require.Zero(t, b.releases)
				require.Equal(t, 1, u.polls)
			}
			require.Equal(t, 1, u.submitted)
		})
	}
}

// 退款失败必须留下可恢复的失败任务，重试只释放一次且不产生成功用量记录。
func TestVideoSubmissionReleaseFailureRetriesFromPersistedState(t *testing.T) {
	s, key, u, b, logs := newVideoLifecycleFixture(BillingModeVideoPerRequest)
	u.submit = &VideoUpstreamResponse{StatusCode: 503}
	b.releaseErr = errors.New("temporary database failure")
	response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	task, _ := s.repo.Get(context.Background(), response.LocalTaskID)
	require.Equal(t, "failed", task.Status)
	require.Equal(t, "reconciliation", task.BillingStatus)
	require.False(t, task.EffectsDone)
	b.releaseErr = nil
	task.NextPollAt = time.Now().Add(-time.Second)
	s.advance(context.Background(), task)
	require.Equal(t, "released", task.BillingStatus)
	require.True(t, task.EffectsDone)
	s.advance(context.Background(), task)
	require.Equal(t, 1, b.releases)
	require.Equal(t, 1, u.submitted)
	require.Empty(t, logs.logs)
}

type videoSubmissionSaveFailureRepo struct {
	VideoTaskRepository
	failed bool
}

// 模拟持久化失败决定时的短暂故障，收尾存储仍可成功但不能提前退款。
func (r *videoSubmissionSaveFailureRepo) Save(ctx context.Context, task *VideoTaskRecord, release bool) error {
	if !release && !r.failed {
		r.failed = true
		return errors.New("cannot persist submission decision")
	}
	return r.VideoTaskRepository.Save(ctx, task, release)
}

func TestVideoSubmissionRecoverySavesFailureBeforeRelease(t *testing.T) {
	s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideoPerRequest)
	response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	task, err := s.repo.Get(context.Background(), response.LocalTaskID)
	require.NoError(t, err)
	task.Status, task.BillingStatus, task.UpstreamTaskID = "submission_unknown", "reconciliation", ""
	task.CreateResponse, task.CreateResponseStatus = []byte(`{"error":{"message":"unavailable"}}`), 503
	require.NoError(t, s.repo.Save(context.Background(), task, true))
	s.repo = &videoSubmissionSaveFailureRepo{VideoTaskRepository: s.repo}
	s.advance(context.Background(), task)
	require.Zero(t, b.releases)
	fresh, err := s.repo.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, "reconciliation", fresh.BillingStatus)
	require.False(t, fresh.EffectsDone)
	s.advance(context.Background(), fresh)
	require.Equal(t, 1, b.releases)
	require.Equal(t, "released", fresh.BillingStatus)
	require.Equal(t, 1, u.submitted)
	require.Zero(t, u.polls)
}
