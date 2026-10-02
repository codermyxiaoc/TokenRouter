package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var videoIdentityEndpoints = []VideoEndpoint{VideoEndpointCompat, VideoEndpointOpenAIVideos, VideoEndpointSeedance, VideoEndpointKling, VideoEndpointWan, VideoEndpointMiniMax}

// 为六种协议生成带真实终态、用量和媒体地址的错包，测试不访问外部上游。
func videoIdentityBody(endpoint VideoEndpoint, id, status string) []byte {
	switch endpoint {
	case VideoEndpointWan:
		return []byte(fmt.Sprintf(`{"output":{"task_id":%q,"task_status":%q,"video_url":"https://cdn.example/foreign.mp4","duration":8},"usage":{"completion_tokens":80000}}`, id, status))
	case VideoEndpointKling:
		return []byte(fmt.Sprintf(`{"data":{"task_id":%q,"task_status":%q,"task_result":{"videos":[{"url":"https://cdn.example/foreign.mp4"}]}},"usage":{"completion_tokens":80000}}`, id, status))
	case VideoEndpointMiniMax:
		return []byte(fmt.Sprintf(`{"task":{"id":%q,"status":%q,"content":{"url":"https://cdn.example/foreign.mp4"},"usage":{"output_seconds":8,"completion_tokens":80000}}}`, id, status))
	default:
		return []byte(fmt.Sprintf(`{"id":%q,"status":%q,"duration":8,"usage":{"completion_tokens":80000},"data":[{"url":"https://cdn.example/foreign.mp4"}]}`, id, status))
	}
}

// 明确冲突的任务 ID 不能被调用方已知的 ID 覆盖，任何终态与计费字段都不得被应用。
func TestVideoTaskIdentityRejectsForeignResponse(t *testing.T) {
	for _, endpoint := range videoIdentityEndpoints {
		for _, status := range []string{"completed", "failed"} {
			t.Run(string(endpoint)+"/"+status, func(t *testing.T) {
				body := videoIdentityBody(endpoint, "foreign-task", status)
				require.False(t, videoResponseMatchesTask(endpoint, body, "owned"))
				got := ExtractVideoUpstreamResponse(endpoint, body, "owned")
				require.Equal(t, "owned", got.TaskID)
				require.Empty(t, got.Status)
				require.Empty(t, got.UpstreamStatus)
				require.Empty(t, got.VideoURL)
				require.Equal(t, VideoRequestMetadata{}, got.Metadata)
			})
		}
	}
}

// 混合列表整包拒绝，不能只过滤观测后继续返回含其它任务的原生正文。
func TestVideoTaskIdentityAliasesListsAndCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name     string
		endpoint VideoEndpoint
		body     string
		allowed  bool
	}{
		{"flat_minimax_foreign", VideoEndpointMiniMax, `{"task_id":"foreign","status":"failed"}`, false},
		{"compat_nested_foreign", VideoEndpointCompat, `{"data":{"task_id":"foreign","status":"failed"}}`, false},
		{"seedance_alias_conflict", VideoEndpointSeedance, `{"id":"owned","task_id":"foreign","status":"failed"}`, false},
		{"kling_list_foreign", VideoEndpointKling, `{"data":[{"task_id":"foreign","task_status":"failed"}]}`, false},
		{"kling_list_mixed", VideoEndpointKling, `{"data":[{"task_id":"owned","task_status":"processing"},{"task_id":"foreign","task_status":"failed"}]}`, false},
		{"kling_list_owned", VideoEndpointKling, `{"data":[{"task_id":"owned","task_status":"processing"}]}`, true},
		{"task_list_mixed", VideoEndpointCompat, `{"tasks":[{"id":"owned","status":"processing"},{"id":"foreign","status":"failed"}]}`, false},
		{"root_list_foreign", VideoEndpointSeedance, `[{"id":"foreign","status":"failed"}]`, false},
		{"null_alias_owned", VideoEndpointCompat, `{"id":null,"task_id":"owned","status":"processing"}`, true},
		{"empty_alias_owned", VideoEndpointCompat, `{"id":"owned","task_id":"","status":"processing"}`, true},
		{"only_invalid_id", VideoEndpointOpenAIVideos, `{"id":23,"status":"failed"}`, false},
		{"minimax_outer_trace", VideoEndpointMiniMax, `{"id":"outer-trace","task":{"id":"owned","status":"processing"}}`, true},
		{"minimax_no_nested_id", VideoEndpointMiniMax, `{"task":{"status":"processing"}}`, true},
		{"minimax_flat_fallback_conflict", VideoEndpointMiniMax, `{"task_id":"foreign","task":{"status":"failed"}}`, false},
		{"minimax_invalid_nested_id", VideoEndpointMiniMax, `{"task":{"id":null,"status":"failed"}}`, false},
		{"minimax_extension_is_not_task", VideoEndpointMiniMax, `{"task":{"id":"owned","status":"processing","tasks":[{"id":"extension"}]}}`, true},
		{"wan_trace_id", VideoEndpointWan, `{"id":"trace","request_id":"trace","output":{"task_id":"owned","task_status":"RUNNING"}}`, true},
		{"media_file_id", VideoEndpointCompat, `{"id":"owned","status":"completed","data":[{"id":"file-id","url":"https://cdn.example/video.mp4"}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.allowed, videoResponseMatchesTask(tc.endpoint, []byte(tc.body), "owned"))
		})
	}
	for _, endpoint := range videoIdentityEndpoints {
		t.Run(string(endpoint)+"/no_id", func(t *testing.T) {
			got := ExtractVideoUpstreamResponse(endpoint, []byte(`{"request_id":"trace","status":"processing"}`), "owned")
			require.Equal(t, "owned", got.TaskID)
			require.Equal(t, "processing", got.Status)
			// 创建没有已知 ID，不得因新增查询门禁改变受理归属不明的处理策略。
			require.True(t, videoResponseMatchesTask(endpoint, videoIdentityBody(endpoint, "new-task", "queued"), ""))
		})
	}
}

// 三种计费模式均保留原预留和原包；正确响应稍后恢复时，仍只结算或释放一次。
func TestVideoTaskIdentityLifecycleRejectsThenRecovers(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoToken, BillingModeVideoPerRequest} {
		for _, endpoint := range videoIdentityEndpoints {
			for _, status := range []string{"completed", "failed"} {
				t.Run(string(mode)+"/"+string(endpoint)+"/"+status, func(t *testing.T) {
					s, key, upstream, billing, logs := newVideoLifecycleFixture(mode)
					upstream.selection.Target.Endpoint = endpoint
					created, err := s.Submit(ctx, key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
					require.NoError(t, err)
					task, err := s.repo.Get(ctx, created.LocalTaskID)
					require.NoError(t, err)
					original := append([]byte(nil), task.RawResponse...)
					wrong := videoIdentityBody(endpoint, "foreign-task", status)
					upstream.poll = ExtractVideoUpstreamResponse(endpoint, wrong, task.UpstreamTaskID)
					upstream.poll.StatusCode, upstream.poll.Body = http.StatusOK, wrong
					for range 2 {
						s.advance(ctx, task)
					}
					require.Equal(t, "queued", task.Status)
					require.Equal(t, "reserved", task.BillingStatus)
					require.Equal(t, original, task.RawResponse)
					require.Empty(t, task.VideoURL)
					require.Zero(t, billing.captures+billing.releases)
					require.Empty(t, logs.logs)
					require.NotContains(t, string(videoTaskResponse(task, true).Body), "foreign")
					valid := videoIdentityBody(endpoint, task.UpstreamTaskID, status)
					upstream.poll = ExtractVideoUpstreamResponse(endpoint, valid, task.UpstreamTaskID)
					upstream.poll.StatusCode, upstream.poll.Body = http.StatusOK, valid
					for range 2 {
						s.advance(ctx, task)
					}
					require.Equal(t, status, task.Status)
					if status == "completed" {
						require.Equal(t, "settled", task.BillingStatus)
						require.Equal(t, 1, billing.captures)
						require.Zero(t, billing.releases)
					} else {
						require.Equal(t, "released", task.BillingStatus)
						require.Equal(t, 1, billing.releases)
						require.Zero(t, billing.captures)
					}
					require.Equal(t, 1, upstream.submitted)
				})
			}
		}
	}
}

// 取消无论返回成功或 HTTP 错误，都不能透传其它任务原包或释放本任务的预留。
func TestVideoTaskIdentityCancelRejectsForeignBody(t *testing.T) {
	for _, endpoint := range videoIdentityEndpoints {
		for _, httpStatus := range []int{http.StatusOK, http.StatusServiceUnavailable} {
			t.Run(fmt.Sprintf("%s/%d", endpoint, httpStatus), func(t *testing.T) {
				s, key, upstream, billing, _ := newVideoLifecycleFixture(BillingModeVideoPerRequest)
				upstream.selection.Target.Endpoint = endpoint
				created, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
				require.NoError(t, err)
				raw := videoIdentityBody(endpoint, "foreign-task", "cancelled")
				upstream.cancel = ExtractVideoUpstreamResponse(endpoint, raw, "upstream-task")
				upstream.cancel.StatusCode, upstream.cancel.Body = httpStatus, raw
				response, err := s.Cancel(context.Background(), key, created.LocalTaskID, string(endpoint))
				require.ErrorIs(t, err, errVideoTaskResponseMismatch)
				require.Nil(t, response)
				task, err := s.repo.Get(context.Background(), created.LocalTaskID)
				require.NoError(t, err)
				require.Equal(t, "queued", task.Status)
				require.NotContains(t, string(task.RawResponse), "foreign-task")
				require.Zero(t, billing.captures+billing.releases)
			})
		}
	}
}

// 首次创建出现多个矛盾 ID 时可能已计费，必须保留预算且停止自动轮询，不能因空解析 ID 误退款。
func TestVideoTaskIdentityConflictingCreationRequiresReconciliation(t *testing.T) {
	for _, tc := range []struct {
		endpoint VideoEndpoint
		body     string
	}{
		{VideoEndpointSeedance, `{"id":"task-a","task_id":"task-b","status":"succeeded","content":{"video_url":"https://cdn.example/private.mp4"}}`},
		{VideoEndpointMiniMax, `{"task":{"id":"task-a","task_id":"task-b","status":"failed"}}`},
		{VideoEndpointKling, `{"data":[{"task_id":"task-a","task_status":"processing"},{"task_id":"task-b","task_status":"failed"}]}`},
	} {
		for _, native := range []bool{false, true} {
			for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
				t.Run(fmt.Sprintf("%s/native=%t/http=%d", tc.endpoint, native, status), func(t *testing.T) {
					s, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideoPerRequest)
					upstream.selection.Target.Endpoint = tc.endpoint
					upstream.submit = ExtractVideoUpstreamResponse(tc.endpoint, []byte(tc.body), "")
					upstream.submit.StatusCode, upstream.submit.Body = status, []byte(tc.body)
					req := VideoTaskSubmitRequest{Body: []byte(`{}`), Native: native, InboundProtocol: string(tc.endpoint), IdempotencyKey: "ambiguous-creation"}
					created, err := s.Submit(context.Background(), key, req)
					require.NoError(t, err)
					require.Equal(t, http.StatusBadGateway, created.StatusCode)
					require.NotContains(t, string(created.Body), "task-a")
					require.NotContains(t, string(created.Body), "task-b")
					task, err := s.repo.Get(context.Background(), created.LocalTaskID)
					require.NoError(t, err)
					require.Empty(t, task.UpstreamTaskID)
					require.Equal(t, "submission_unknown", task.Status)
					require.Equal(t, "reconciliation", task.BillingStatus)
					require.Equal(t, tc.body, string(task.CreateResponse), "内部诊断记录保留原包")
					for range 2 {
						s.advance(context.Background(), task)
					}
					response, err := s.Submit(context.Background(), key, req)
					require.ErrorIs(t, err, errVideoTaskResponseMismatch)
					require.Nil(t, response)
					require.Zero(t, upstream.polls+upstream.cancels+billing.captures+billing.releases)
					require.Equal(t, 1, upstream.submitted)
					require.Empty(t, logs.logs)
				})
			}
		}
	}
}

// 替代传输的结构化 ID 不能覆盖原包归属；保留其证据但不自动选择或结算存疑任务。
func TestVideoTaskIdentityCreationStructuredIDMismatch(t *testing.T) {
	for _, status := range []string{"completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			s, key, upstream, billing, _ := newVideoLifecycleFixture(BillingModeVideoPerRequest)
			upstream.submit = &VideoUpstreamResponse{StatusCode: http.StatusOK, TaskID: "structured-task", Status: status,
				Body: videoIdentityBody(VideoEndpointCompat, "raw-task", status)}
			req := VideoTaskSubmitRequest{Body: []byte(`{}`), Native: true, IdempotencyKey: "structured-mismatch"}
			created, err := s.Submit(context.Background(), key, req)
			require.NoError(t, err)
			require.Equal(t, http.StatusBadGateway, created.StatusCode)
			require.NotContains(t, string(created.Body), "raw-task")
			task, err := s.repo.Get(context.Background(), created.LocalTaskID)
			require.NoError(t, err)
			require.Equal(t, "structured-task", task.UpstreamTaskID)
			require.Equal(t, "submission_unknown", task.Status)
			require.Equal(t, "reconciliation", task.BillingStatus)
			s.advance(context.Background(), task)
			_, err = s.Submit(context.Background(), key, req)
			require.ErrorIs(t, err, errVideoTaskResponseMismatch)
			require.Zero(t, upstream.polls+billing.captures+billing.releases)
			require.Equal(t, 1, upstream.submitted)
		})
	}
}

// 重复规范字段或父封装不能由首值解析掩盖另一个任务，创建也不能被误认成无 ID 拒绝。
func TestVideoTaskIdentityDuplicateCanonicalFields(t *testing.T) {
	for _, tc := range []struct {
		name     string
		endpoint VideoEndpoint
		body     string
	}{
		{"compat_id", VideoEndpointCompat, `{"id":"owned","id":"foreign","status":"completed"}`},
		{"escaped_id", VideoEndpointOpenAIVideos, `{"id":"owned","\u0069d":"foreign","status":"failed"}`},
		{"seedance_data", VideoEndpointSeedance, `{"data":{"task_id":"owned"},"data":{"task_id":"foreign"},"status":"completed"}`},
		{"kling_task_id", VideoEndpointKling, `{"data":{"task_id":"owned","task_id":"foreign","task_status":"failed"}}`},
		{"kling_list", VideoEndpointKling, `{"data":[{"task_id":"owned","task_id":"foreign","task_status":"failed"}]}`},
		{"wan_output", VideoEndpointWan, `{"output":{"task_id":"owned","task_status":"SUCCEEDED"},"output":{"task_id":"foreign","task_status":"FAILED"}}`},
		{"minimax_task", VideoEndpointMiniMax, `{"task":{"id":"owned","status":"completed"},"task":{"id":"foreign","status":"failed"}}`},
		{"minimax_task_id", VideoEndpointMiniMax, `{"task":{"id":"owned","id":"foreign","status":"failed"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			require.False(t, videoResponseMatchesTask(tc.endpoint, body, "owned"))
			require.True(t, videoResponseHasConflictingTaskIDs(tc.endpoint, body))
			require.Empty(t, ExtractVideoUpstreamResponse(tc.endpoint, body, "owned").Status)
			s, key, upstream, billing, _ := newVideoLifecycleFixture(BillingModeVideoPerRequest)
			upstream.selection.Target.Endpoint = tc.endpoint
			upstream.submit = ExtractVideoUpstreamResponse(tc.endpoint, body, "")
			upstream.submit.StatusCode, upstream.submit.Body = http.StatusServiceUnavailable, body
			created, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`), Native: true})
			require.NoError(t, err)
			require.Equal(t, http.StatusBadGateway, created.StatusCode)
			require.NotContains(t, string(created.Body), "foreign")
			task, err := s.repo.Get(context.Background(), created.LocalTaskID)
			require.NoError(t, err)
			require.Equal(t, "submission_unknown", task.Status)
			require.Equal(t, "reconciliation", task.BillingStatus)
			s.advance(context.Background(), task)
			require.Zero(t, billing.captures+billing.releases+upstream.polls)
		})
	}
	// 非规范扩展、追踪号以及真实无 ID 错误包继续保持原协议兼容。
	for _, body := range []string{``, `<html>error</html>`, `{"error":{"message":"unavailable"}}`, `{"request_id":"a","request_id":"b","status":"failed"}`, `{"task":{"id":"owned","future":1,"future":2,"status":"completed"}}`} {
		require.False(t, videoResponseHasConflictingTaskIDs(VideoEndpointMiniMax, []byte(body)))
	}
}

// 无租约查询、已完成任务与幂等重放都不能暴露升级前保存的错误原包，也不追改旧费用。
func TestVideoTaskIdentityStoredBodyReadGuards(t *testing.T) {
	for _, state := range []string{"queued", "completed", "failed"} {
		for _, field := range []string{"raw", "create"} {
			t.Run(state+"/"+field, func(t *testing.T) {
				s, key, upstream, billing, _ := newVideoLifecycleFixture(BillingModeVideoPerRequest)
				req := VideoTaskSubmitRequest{Body: []byte(`{}`), IdempotencyKey: "stored-identity"}
				created, err := s.Submit(context.Background(), key, req)
				require.NoError(t, err)
				task, err := s.repo.Get(context.Background(), created.LocalTaskID)
				require.NoError(t, err)
				task.Status, task.EffectsDone, task.NextPollAt = state, state != "queued", time.Now().Add(time.Hour)
				if field == "raw" {
					task.RawResponse = videoIdentityBody(VideoEndpointCompat, "foreign-task", state)
				} else {
					task.CreateResponse = videoIdentityBody(VideoEndpointCompat, "foreign-task", state)
				}
				require.NoError(t, s.repo.Save(context.Background(), task, true))
				for _, protocol := range []string{"unified", "openai_videos", "seedance"} {
					response, err := s.Query(context.Background(), key, task.ID, protocol)
					require.ErrorIs(t, err, errVideoTaskResponseMismatch)
					require.Nil(t, response)
				}
				_, err = s.Cancel(context.Background(), key, task.ID, "unified")
				require.ErrorIs(t, err, errVideoTaskResponseMismatch)
				_, err = s.Submit(context.Background(), key, req)
				require.ErrorIs(t, err, errVideoTaskResponseMismatch)
				s.advance(context.Background(), task)
				for _, response := range []*VideoTaskResponse{videoTaskResponse(task, true), videoCreateResponse(task, true), videoProtocolResponse(task, "openai_videos", false)} {
					require.Equal(t, http.StatusBadGateway, response.StatusCode)
					require.NotContains(t, string(response.Body), "foreign-task")
				}
				require.Zero(t, upstream.polls+upstream.cancels)
				require.Equal(t, 1, upstream.submitted)
				require.Zero(t, billing.captures+billing.releases)
			})
		}
	}
}

// 真实传输出口也拒绝错包，避免调用方绕过协调器直接获得第三方原生任务正文。
func TestVideoTaskIdentityTransportRejectsForeignBody(t *testing.T) {
	for _, endpoint := range videoIdentityEndpoints {
		t.Run(string(endpoint), func(t *testing.T) {
			fixture := &videoHTTPFixture{status: http.StatusOK, response: string(videoIdentityBody(endpoint, "foreign-task", "failed"))}
			svc := NewVideoUpstreamService(&OpenAIGatewayService{httpUpstream: fixture})
			account := videoFixtureAccount(endpoint)
			target := VideoUpstreamTarget{Version: 1, Endpoint: endpoint, BaseURL: "https://relay.example", AccountID: account.ID}
			response, err := svc.forward(context.Background(), account, target, http.MethodGet, "/tasks/owned", nil, "owned")
			require.ErrorIs(t, err, errVideoTaskResponseMismatch)
			require.Nil(t, response)
			require.Equal(t, 1, fixture.calls)
		})
	}
}

// 内容入口与已签发票据每次读取都检查旧原包，不用错误任务的地址继续下载。
func TestVideoTaskIdentityStoredBodyBlocksMedia(t *testing.T) {
	t.Run("api_content", func(t *testing.T) {
		svc, key, task, upstream, billing := newVideoContentFixture()
		task.RawResponse = videoIdentityBody(VideoEndpointOpenAIVideos, "foreign-task", "completed")
		task.VideoURL = "https://cdn.example/foreign.mp4"
		calls := 0
		svc.contentHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, fmt.Errorf("不应下载") })}
		response, err := svc.OpenContent(context.Background(), key, task.ID, "openai_videos", "")
		require.ErrorIs(t, err, errVideoTaskResponseMismatch)
		require.Nil(t, response)
		require.Zero(t, calls+upstream.polls+upstream.submitted+upstream.cancels+billing.captures+billing.releases)
	})
	t.Run("preview_and_existing_ticket", func(t *testing.T) {
		s, repo, _, upstream := newMediaVideoContentPreviewFixture(t)
		token := mediaVideoContentPreviewToken(t, s, MediaTaskActor{UserID: 7})
		repo.video.RawResponse = videoIdentityBody(VideoEndpointOpenAIVideos, "foreign-task", "completed")
		record, err := s.videoPreviewRecord(context.Background(), mediaTaskPreviewIdentity(repo.task))
		require.ErrorIs(t, err, errVideoTaskResponseMismatch)
		require.Nil(t, record)
		response, err := s.OpenPreviewContent(context.Background(), token, http.MethodGet, "")
		require.Error(t, err)
		require.Nil(t, response)
		require.False(t, strings.Contains(err.Error(), "foreign-task"))
		require.Zero(t, upstream.calls)
	})
}
