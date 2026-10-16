package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// 保存实际成功响应的结构，用测试 ID 和公共示例地址替换真实任务及产物。
const videoMiniMaxTaskIdCompletedFixture = `{"request_id":"query-trace","task":{"content":{"url":"https://cdn.example/video.mp4"},"created_at":1792139272,"duration":5,"error":null,"fail_reason":null,"id":"minimax-created-task","modality":"video","model":"MiniMax-H3","ratio":"16:9","resolution":"768P","status":"succeeded","task_type":"generation","updated_at":1792139413,"usage":{"input_image_count":0,"input_seconds":0,"output_seconds":5,"total_seconds":5}}}`

// 线上创建返回 TaskId，而查询返回 task.id，两种规范位置必须指向同一个受理任务。
func TestVideoMiniMaxTaskIdNativeCreationAndQuery(t *testing.T) {
	created := []byte(`{"TaskId":"minimax-created-task"}`)
	require.False(t, videoResponseHasConflictingTaskIDs(VideoEndpointMiniMax, created))
	result := ExtractVideoUpstreamResponse(VideoEndpointMiniMax, created, "")
	require.Equal(t, "minimax-created-task", result.TaskID)
	require.Empty(t, result.Status)
	queried := []byte(`{"request_id":"query-trace","task":{"id":"minimax-created-task","model":"MiniMax-H3","status":"running"}}`)
	require.True(t, videoResponseMatchesTask(VideoEndpointMiniMax, queried, result.TaskID))
	result = ExtractVideoUpstreamResponse(VideoEndpointMiniMax, queried, result.TaskID)
	require.Equal(t, "minimax-created-task", result.TaskID)
	require.Equal(t, "processing", result.Status)
	require.Equal(t, "MiniMax-H3", result.Metadata.Model)
	result = ExtractVideoUpstreamResponse(VideoEndpointMiniMax, []byte(videoMiniMaxTaskIdCompletedFixture), result.TaskID)
	require.Equal(t, "completed", result.Status)
	require.Equal(t, 5.0, result.Metadata.DurationSeconds)
	require.Equal(t, "768P", result.Metadata.Resolution)
	require.Nil(t, result.Metadata.Tokens, "秒数不代表上游返回了 Token 用量")
	require.Equal(t, "https://cdn.example/video.mp4", result.VideoURL)
}

// 实际完成包仅有输出秒数，按秒和按次正常结算，Token 缺用量必须保留待核对。
func TestVideoMiniMaxTaskIdCompletedWithoutTokensBillingBoundary(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoToken, BillingModeVideoPerRequest} {
		t.Run(string(mode), func(t *testing.T) {
			s, key, upstream, billing, logs := newVideoLifecycleFixture(mode)
			key.Group.RateMultiplier = 1
			key.Group.ModelPricing[0].VideoPrices[0].Resolution = "768p"
			upstream.selection.Target.Endpoint = VideoEndpointMiniMax
			upstream.selection.Metadata.Resolution = "768p"
			upstream.selection.Metadata.DurationSeconds = 5
			body := []byte(`{"TaskId":"minimax-created-task"}`)
			upstream.submit = ExtractVideoUpstreamResponse(VideoEndpointMiniMax, body, "")
			upstream.submit.StatusCode, upstream.submit.Body = http.StatusOK, body
			created, err := s.Submit(ctx, key, VideoTaskSubmitRequest{Body: []byte(`{}`), InboundProtocol: "minimax", Native: true})
			require.NoError(t, err)
			task, err := s.repo.Get(ctx, created.LocalTaskID)
			require.NoError(t, err)
			upstream.poll = ExtractVideoUpstreamResponse(VideoEndpointMiniMax, []byte(videoMiniMaxTaskIdCompletedFixture), task.UpstreamTaskID)
			upstream.poll.StatusCode, upstream.poll.Body = http.StatusOK, []byte(videoMiniMaxTaskIdCompletedFixture)
			for range 2 {
				s.advance(ctx, task)
			}
			require.Equal(t, "completed", task.Status)
			require.Nil(t, task.Metadata.Tokens)
			require.Equal(t, 1, upstream.submitted)
			require.Zero(t, billing.releases)
			if mode == BillingModeVideoToken {
				require.Equal(t, "reconciliation", task.BillingStatus)
				require.Zero(t, billing.captures)
				require.Empty(t, logs.logs)
			} else {
				require.Equal(t, "settled", task.BillingStatus)
				require.Equal(t, 1, billing.captures)
				require.Len(t, logs.logs, 1)
				amount := 1.0
				if mode == BillingModeVideo {
					amount = 5
				}
				require.InDelta(t, amount, task.BillingResult.ActualAmountUSD, 1e-10)
			}
		})
	}
}

// 新别名不能绕过旧别名的冲突、重复键和查询归属检查，也不接受近似大小写或数值 ID。
func TestVideoMiniMaxTaskIdIdentityBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		conflict   bool
		queryMatch bool
	}{
		{"same_flat_alias", `{"TaskId":"owned","task_id":"owned"}`, false, true},
		{"same_data_alias", `{"TaskId":"owned","data":{"task_id":"owned"}}`, false, true},
		{"same_id_alias", `{"TaskId":"owned","id":"owned"}`, false, true},
		{"same_nested_id", `{"TaskId":"owned","task":{"id":"owned"}}`, false, true},
		{"same_nested_alias", `{"TaskId":"owned","task":{"task_id":"owned"}}`, false, true},
		{"flat_conflict", `{"TaskId":"owned","task_id":"foreign"}`, true, false},
		{"data_conflict", `{"TaskId":"owned","data":{"task_id":"foreign"}}`, true, false},
		{"id_conflict", `{"TaskId":"owned","id":"foreign"}`, true, false},
		{"nested_conflict", `{"TaskId":"owned","task":{"id":"foreign"}}`, true, false},
		{"nested_alias_conflict", `{"TaskId":"owned","task":{"task_id":"foreign"}}`, true, false},
		{"query_foreign", `{"TaskId":"foreign","status":"failed"}`, false, false},
		{"query_foreign_nested_owned", `{"TaskId":"foreign","task":{"id":"owned","status":"failed"}}`, true, false},
		{"duplicate_different", `{"TaskId":"owned","TaskId":"foreign"}`, true, false},
		{"duplicate_equal", `{"TaskId":"owned","TaskId":"owned"}`, true, false},
		{"null_with_valid_alias", `{"TaskId":null,"task_id":"owned"}`, false, true},
		{"empty_with_valid_alias", `{"TaskId":"","task_id":"owned"}`, false, true},
		{"numeric_only", `{"TaskId":123,"status":"failed"}`, false, false},
		{"null_only", `{"TaskId":null,"status":"failed"}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			require.Equal(t, tc.conflict, videoResponseHasConflictingTaskIDs(VideoEndpointMiniMax, body))
			require.Equal(t, tc.queryMatch, videoResponseMatchesTask(VideoEndpointMiniMax, body, "owned"))
			if !tc.queryMatch {
				result := ExtractVideoUpstreamResponse(VideoEndpointMiniMax, body, "owned")
				require.Equal(t, "owned", result.TaskID)
				require.Empty(t, result.Status)
				require.Empty(t, result.VideoURL)
			}
		})
	}
	for _, body := range []string{`{"taskId":"not-recognized"}`, `{"TASKID":"not-recognized"}`, `{"TaskID":"not-recognized"}`, `{"TaskId":123}`} {
		require.Empty(t, ExtractVideoUpstreamResponse(VideoEndpointMiniMax, []byte(body), "").TaskID)
	}
}

// 顶层别名为空或类型不合法时，已有合法嵌套 ID 仍沿用旧的容错合同，不能拒绝正常查询。
func TestVideoMiniMaxTaskIdInvalidAliasKeepsValidNestedIdentity(t *testing.T) {
	for _, alias := range []string{`null`, `""`, `123`, `true`, `false`, `{}`, `[]`} {
		for _, field := range []string{"id", "task_id"} {
			t.Run(field+"/"+alias, func(t *testing.T) {
				body := []byte(fmt.Sprintf(`{"TaskId":%s,"task":{%q:"owned","status":"running"}}`, alias, field))
				require.False(t, videoResponseHasConflictingTaskIDs(VideoEndpointMiniMax, body))
				require.True(t, videoResponseMatchesTask(VideoEndpointMiniMax, body, "owned"))
				for _, knownID := range []string{"", "owned"} {
					result := ExtractVideoUpstreamResponse(VideoEndpointMiniMax, body, knownID)
					require.Equal(t, "owned", result.TaskID)
					require.Equal(t, "processing", result.Status)
				}
			})
		}
	}
}

// 仅返回无法解析的受理别名时不能把失败文案或 HTTP 错误当成未受理证明；恢复也不能退款或重新生成。
func TestVideoMiniMaxUnusableTaskIdRetainsBudgetAcrossRecovery(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoToken, BillingModeVideoPerRequest} {
		for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			for _, value := range []string{`123`, `true`, `false`, `{}`, `[]`} {
				t.Run(fmt.Sprintf("%s/%d/%s", mode, status, value), func(t *testing.T) {
					s, key, upstream, billing, logs := newVideoLifecycleFixture(mode)
					upstream.selection.Target.Endpoint = VideoEndpointMiniMax
					body := []byte(fmt.Sprintf(`{"TaskId":%s,"status":"failed","error":{"code":"server_busy"}}`, value))
					upstream.submit = ExtractVideoUpstreamResponse(VideoEndpointMiniMax, body, "")
					upstream.submit.StatusCode, upstream.submit.Body = status, body
					req := VideoTaskSubmitRequest{Body: []byte(`{}`), InboundProtocol: "minimax", Native: true, IdempotencyKey: "unusable-minimax-id"}
					created, err := s.Submit(ctx, key, req)
					require.NoError(t, err)
					for range 3 {
						task, err := s.repo.Get(ctx, created.LocalTaskID)
						require.NoError(t, err)
						require.Empty(t, task.UpstreamTaskID)
						require.Equal(t, "submission_unknown", task.Status)
						require.Equal(t, "reconciliation", task.BillingStatus)
						require.Equal(t, body, task.CreateResponse)
						s.advance(ctx, task)
						require.Equal(t, "submission_unknown", task.Status)
						require.Equal(t, "reconciliation", task.BillingStatus)
					}
					_, err = s.Submit(ctx, key, req)
					require.NoError(t, err)
					require.Equal(t, 1, upstream.submitted)
					require.Zero(t, upstream.polls)
					require.Equal(t, 1, billing.reserves)
					require.Zero(t, billing.captures+billing.releases)
					require.Empty(t, logs.logs)
				})
			}
		}
	}
}

// 其它协议的同名扩展不能被提升成任务 ID，也不能改变其原有归属校验。
func TestVideoMiniMaxTaskIdDoesNotChangeOtherProtocols(t *testing.T) {
	for _, endpoint := range []VideoEndpoint{VideoEndpointCompat, VideoEndpointOpenAIVideos, VideoEndpointSeedance, VideoEndpointKling, VideoEndpointWan} {
		t.Run(string(endpoint), func(t *testing.T) {
			body := []byte(`{"TaskId":"foreign","TaskId":"another-extension"}`)
			require.Empty(t, ExtractVideoUpstreamResponse(endpoint, body, "").TaskID)
			require.False(t, videoResponseHasConflictingTaskIDs(endpoint, body))
			require.True(t, videoResponseMatchesTask(endpoint, body, "owned"))
		})
	}
}

// 带有受理别名的错误仍可能已开始生成，不能被认证或容量错误的换号规则重提。
func TestVideoMiniMaxTaskIdPreventsRejectedCreationRetry(t *testing.T) {
	for _, status := range []int{400, 401, 402, 403, 429, 503} {
		for _, body := range []string{
			`{"TaskId":"accepted","error":{"code":"server_busy"}}`,
			`{"TaskId":123,"error":{"code":"server_busy"}}`,
			`{"TaskId":"one","TaskId":"two","error":{"code":"server_busy"}}`,
		} {
			require.False(t, VideoCreationDefinitelyRejected(status, []byte(body)), "status=%d body=%s", status, body)
		}
	}
}

// 创建 HTTP 报错但已返回 TaskId 时保留原预算，重复提交和查询都不能重新生成或提前退款。
func TestVideoMiniMaxTaskIdHTTPErrorKeepsOwnershipAndHold(t *testing.T) {
	ctx := context.Background()
	for _, status := range []int{400, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s, key, upstream, billing, _ := newVideoLifecycleFixture(BillingModeVideoPerRequest)
			upstream.selection.Target.Endpoint = VideoEndpointMiniMax
			body := []byte(`{"TaskId":"minimax-created-task","error":{"code":"server_busy"}}`)
			upstream.submit = ExtractVideoUpstreamResponse(VideoEndpointMiniMax, body, "")
			upstream.submit.StatusCode, upstream.submit.Body = status, body
			req := VideoTaskSubmitRequest{Body: []byte(`{}`), InboundProtocol: "minimax", Native: true, IdempotencyKey: "minimax-taskid-once"}
			created, err := s.Submit(ctx, key, req)
			require.NoError(t, err)
			task, err := s.repo.Get(ctx, created.LocalTaskID)
			require.NoError(t, err)
			require.Equal(t, "minimax-created-task", task.UpstreamTaskID)
			require.Equal(t, "submission_unknown", task.Status)
			require.Zero(t, billing.captures+billing.releases)
			_, err = s.Submit(ctx, key, req)
			require.NoError(t, err)
			queried := []byte(`{"task":{"id":"minimax-created-task","status":"running"}}`)
			upstream.poll = ExtractVideoUpstreamResponse(VideoEndpointMiniMax, queried, task.UpstreamTaskID)
			upstream.poll.StatusCode, upstream.poll.Body = http.StatusOK, queried
			s.advance(ctx, task)
			require.Equal(t, "processing", task.Status)
			require.Equal(t, 1, upstream.polls)
			require.Equal(t, 1, upstream.submitted)
			require.Equal(t, 1, billing.reserves)
			require.Zero(t, billing.captures+billing.releases)
		})
	}
}

// 旧版已保存 TaskId 原包但漏记归属的任务，恢复时只接续查询；三种计费的扣款和退款均幂等。
func TestVideoMiniMaxTaskIdHistoricalRecoveryAndSettlement(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []struct {
		mode          BillingMode
		price, actual float64
	}{
		{BillingModeVideo, .425, 2.125},
		{BillingModeVideoToken, 1, .000321},
		{BillingModeVideoPerRequest, 2.125, 2.125},
	} {
		for _, status := range []string{"succeeded", "failed", "cancelled"} {
			t.Run(string(mode.mode)+"/"+status, func(t *testing.T) {
				s, key, upstream, billing, logs := newVideoLifecycleFixture(mode.mode)
				key.Group.RateMultiplier = 1
				*key.Group.ModelPricing[0].VideoPrices[0].Price = mode.price
				upstream.selection.Target.Endpoint = VideoEndpointMiniMax
				upstream.selection.Metadata.DurationSeconds = 5
				// 模拟旧解析器收到完整成功包但没有提取出受理 ID 的持久状态。
				createdBody := []byte(`{"TaskId":"minimax-created-task"}`)
				upstream.submit = &VideoUpstreamResponse{StatusCode: http.StatusOK, Body: createdBody}
				created, err := s.Submit(ctx, key, VideoTaskSubmitRequest{Body: []byte(`{}`), InboundProtocol: "minimax", Native: true})
				require.NoError(t, err)
				task, err := s.repo.Get(ctx, created.LocalTaskID)
				require.NoError(t, err)
				require.Equal(t, "submission_unknown", task.Status)
				require.Empty(t, task.UpstreamTaskID)
				require.Equal(t, createdBody, task.CreateResponse)
				queryBody := []byte(fmt.Sprintf(`{"task":{"id":"minimax-created-task","status":%q,"content":{"url":"https://cdn.example/video.mp4"},"usage":{"output_seconds":5,"completion_tokens":321}}}`, status))
				upstream.poll = ExtractVideoUpstreamResponse(VideoEndpointMiniMax, queryBody, "minimax-created-task")
				upstream.poll.StatusCode, upstream.poll.Body = http.StatusOK, queryBody
				for range 3 {
					// 每轮从持久副本重新加载，验证重启恢复不会依赖内存中的已结算标记。
					task, err = s.repo.Get(ctx, created.LocalTaskID)
					require.NoError(t, err)
					s.advance(ctx, task)
				}
				require.Equal(t, "minimax-created-task", task.UpstreamTaskID)
				require.Equal(t, 1, upstream.submitted)
				require.Equal(t, 1, upstream.polls)
				require.Equal(t, 1, billing.reserves)
				if status == "succeeded" {
					require.Equal(t, "completed", task.Status)
					require.Equal(t, "settled", task.BillingStatus)
					require.Equal(t, 1, billing.captures)
					require.Zero(t, billing.releases)
					require.InDelta(t, mode.actual, task.BillingResult.ActualAmountUSD, 1e-10)
					require.Len(t, logs.logs, 1)
				} else {
					require.Equal(t, status, task.Status)
					require.Equal(t, "released", task.BillingStatus)
					require.Equal(t, 1, billing.releases)
					require.Zero(t, billing.captures)
					require.Empty(t, logs.logs)
				}
			})
		}
	}
}
