package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// MiniMax 中继的原生任务包必须按协议解开，输出秒数不能误用输入或合计秒数。
func TestVideoMiniMaxNestedTaskResponse(t *testing.T) {
	body := []byte(`{"status":"processing","duration":99,"task":{"id":"owned","modality":"video","model":"MiniMax-H3-Max","status":"succeeded","task_type":"generation","duration":8,"content":{"url":"https://cdn.example/video.mp4"},"usage":{"input_seconds":3,"output_seconds":5,"total_seconds":8,"completion_tokens":321,"prompt_tokens":100,"total_tokens":421}}}`)
	for _, taskID := range []string{"", "owned"} {
		t.Run(map[bool]string{true: "create", false: "query"}[taskID == ""], func(t *testing.T) {
			original := append([]byte(nil), body...)
			got := ExtractVideoUpstreamResponse(VideoEndpointMiniMax, body, taskID)
			require.Equal(t, "owned", got.TaskID)
			require.Equal(t, "succeeded", got.UpstreamStatus)
			require.Equal(t, "completed", got.Status)
			require.Equal(t, "MiniMax-H3-Max", got.Metadata.Model)
			require.Equal(t, 5.0, got.Metadata.DurationSeconds)
			require.NotNil(t, got.Metadata.Tokens)
			require.Equal(t, int64(321), *got.Metadata.Tokens)
			require.Equal(t, "https://cdn.example/video.mp4", got.VideoURL)
			require.Equal(t, original, body, "原生响应必须保持原样")
		})
	}
}

// 排队、失败、取消与过期都使用任务内的状态，不把外层状态误认为终态。
func TestVideoMiniMaxNestedTaskStates(t *testing.T) {
	for _, tc := range []struct{ status, want string }{
		{"queued", "queued"}, {"running", "processing"}, {"succeeded", "completed"},
		{"failed", "failed"}, {"cancelled", "cancelled"}, {"expired", "expired"}, {"unknown", ""},
	} {
		t.Run(tc.status, func(t *testing.T) {
			got := ExtractVideoUpstreamResponse(VideoEndpointMiniMax, []byte(fmt.Sprintf(`{"task":{"id":"owned","status":%q}}`, tc.status)), "owned")
			require.Equal(t, tc.status, got.UpstreamStatus)
			require.Equal(t, tc.want, got.Status)
		})
	}
}

// 仅 MiniMax 识别 task/content.url/output_seconds，其他协议的同名扩展不能触发扣款或退款。
func TestVideoMiniMaxNestedTaskProtocolAndOwnerIsolation(t *testing.T) {
	body := []byte(`{"id":"outer","status":"processing","duration":3,"content":{"url":"https://cdn.example/outer.mp4"},"usage":{"output_seconds":9},"task":{"id":"other","status":"failed","model":"nested","content":{"url":"https://cdn.example/other.mp4"},"usage":{"output_seconds":5,"completion_tokens":99}}}`)
	for _, endpoint := range []VideoEndpoint{VideoEndpointCompat, VideoEndpointOpenAIVideos, VideoEndpointSeedance, VideoEndpointKling, VideoEndpointWan} {
		t.Run(string(endpoint), func(t *testing.T) {
			// 其它协议仍读取自身平铺任务；外层 ID 必须与查询归属一致。
			got := ExtractVideoUpstreamResponse(endpoint, body, "outer")
			require.Equal(t, "outer", got.TaskID)
			require.Equal(t, "processing", got.Status)
			require.Equal(t, 3.0, got.Metadata.DurationSeconds)
			require.Empty(t, got.VideoURL)
			require.Empty(t, got.Metadata.Model)
			require.Nil(t, got.Metadata.Tokens)
		})
	}
	got := ExtractVideoUpstreamResponse(VideoEndpointMiniMax, body, "owned")
	require.Equal(t, "owned", got.TaskID)
	require.Empty(t, got.Status, "查询返回不同任务时不能应用其失败状态")
	require.Empty(t, got.UpstreamStatus)
	require.Empty(t, got.VideoURL)
	require.Zero(t, got.Metadata.DurationSeconds)
	require.Nil(t, got.Metadata.Tokens)
}

// MiniMax 封装内的扩展形状不能冒充其它协议的任务包，更不能借其终态或用量扣费。
func TestVideoMiniMaxNestedTaskDoesNotReadProtocolExtensions(t *testing.T) {
	for _, extension := range []string{
		`"data":[{"id":"owned","status":"failed","duration":99,"usage":{"completion_tokens":99}}]`,
		`"tasks":[{"id":"owned","status":"failed","duration":99,"usage":{"completion_tokens":99}}]`,
		`"data":{"status":"failed","model":"other","duration":99,"video_url":"https://cdn.example/other.mp4","usage":{"completion_tokens":99}}`,
		`"output":{"task_status":"succeeded","resolution":"1080p","duration":99,"video_url":"https://cdn.example/other.mp4","usage":{"completion_tokens":99}}`,
		`"video":{"resolution":"1080p","duration":99,"url":"https://cdn.example/other.mp4"}`,
	} {
		got := ExtractVideoUpstreamResponse(VideoEndpointMiniMax, []byte(`{"status":"failed","task":{"id":"owned",`+extension+`}}`), "owned")
		require.Equal(t, "owned", got.TaskID)
		require.Empty(t, got.Status)
		require.Empty(t, got.UpstreamStatus)
		require.Empty(t, got.VideoURL)
		require.Empty(t, got.Metadata.Model)
		require.Empty(t, got.Metadata.Resolution)
		require.Zero(t, got.Metadata.DurationSeconds)
		require.Nil(t, got.Metadata.Tokens)
	}
	// 已知成功状态也不能从扩展 output 中捞取 Token，把真实零占位绕过成可信用量。
	got := ExtractVideoUpstreamResponse(VideoEndpointMiniMax, []byte(`{"task":{"id":"owned","status":"succeeded","usage":{"completion_tokens":0,"prompt_tokens":0,"total_tokens":0,"output_seconds":5},"output":{"usage":{"completion_tokens":99}}}}`), "owned")
	require.Equal(t, "completed", got.Status)
	require.Equal(t, 5.0, got.Metadata.DurationSeconds)
	require.Nil(t, got.Metadata.Tokens)
}

// ID 缺失保留已知归属，明确冲突和非法类型保持未知；外层错误不能覆盖已受理任务。
func TestVideoMiniMaxNestedTaskIdentityAndIncompleteEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, body, queryID, wantID, wantStatus string
	}{
		{"empty_query", `{"status":"failed","task":{}}`, "owned", "owned", ""},
		{"incomplete_create_keeps_flat_id", `{"task_id":"accepted","base_resp":{"status_code":1000},"task":{}}`, "", "accepted", ""},
		{"empty_create_rejection", `{"base_resp":{"status_code":1000},"task":{}}`, "", "", "failed"},
		{"empty_query_error_is_not_failure", `{"base_resp":{"status_code":1000},"task":{}}`, "owned", "owned", ""},
		{"known_id_missing_status", `{"status":"failed","task":{"id":"owned"}}`, "owned", "owned", ""},
		{"canonical_nested_id", `{"id":"outer","task":{"id":"owned","status":"queued"}}`, "", "owned", "queued"},
		{"same_alias", `{"task":{"id":"owned","task_id":"owned","status":"queued"}}`, "", "owned", "queued"},
		{"task_id_only_create", `{"task":{"task_id":"owned","status":"queued"}}`, "", "owned", "queued"},
		{"task_id_only_query", `{"task":{"task_id":"owned","status":"failed"}}`, "owned", "owned", "failed"},
		{"task_id_with_null_id", `{"task":{"id":null,"task_id":"owned","status":"queued"}}`, "", "owned", "queued"},
		{"id_with_empty_alias", `{"task":{"id":"owned","task_id":"","status":"queued"}}`, "", "owned", "queued"},
		{"invalid_ids_keep_flat_owner", `{"task_id":"accepted","task":{"id":null,"status":"failed"}}`, "", "accepted", ""},
		{"conflicting_create_alias", `{"task":{"id":"owned","task_id":"other","status":"succeeded"}}`, "", "", ""},
		{"conflicting_query_alias", `{"task":{"id":"owned","task_id":"other","status":"failed"}}`, "owned", "owned", ""},
		{"alias_mismatches_query", `{"task":{"task_id":"other","status":"failed"}}`, "owned", "owned", ""},
		{"old_flat_null_task", `{"task_id":"owned","status":"queued","task":null}`, "", "owned", "queued"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractVideoUpstreamResponse(VideoEndpointMiniMax, []byte(tc.body), tc.queryID)
			require.Equal(t, tc.wantID, got.TaskID)
			require.Equal(t, tc.wantStatus, got.Status)
		})
	}
	for _, value := range []string{`null`, `""`, `123`, `false`, `{}`, `[]`} {
		t.Run("invalid_id_"+value, func(t *testing.T) {
			got := ExtractVideoUpstreamResponse(VideoEndpointMiniMax, []byte(`{"task":{"id":`+value+`,"status":"failed"}}`), "owned")
			require.Equal(t, "owned", got.TaskID)
			require.Empty(t, got.Status)
			require.Empty(t, got.UpstreamStatus)
		})
	}
}

// 查询 HTTP 错误中的嵌套状态仍不是可靠任务观测，不能把上游 503 改写成扣款或退款。
func TestVideoMiniMaxQueryHTTPErrorDoesNotApplyNestedTerminal(t *testing.T) {
	for _, status := range []string{"succeeded", "failed"} {
		t.Run(status, func(t *testing.T) {
			s, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideoPerRequest)
			upstream.selection.Target.Endpoint = VideoEndpointMiniMax
			created, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{
				Native: true, InboundProtocol: string(VideoEndpointMiniMax), NativePath: "/v2/video_generation",
				Body: []byte(`{"model":"video-model","resolution":"720p","duration":5}`),
			})
			require.NoError(t, err)
			task, err := s.repo.Get(context.Background(), created.LocalTaskID)
			require.NoError(t, err)
			raw := append([]byte(nil), task.RawResponse...)
			body := []byte(fmt.Sprintf(`{"task":{"id":"upstream-task","status":%q}}`, status))
			upstream.poll = ExtractVideoUpstreamResponse(VideoEndpointMiniMax, body, task.UpstreamTaskID)
			upstream.poll.StatusCode, upstream.poll.Body = http.StatusServiceUnavailable, body
			s.advance(context.Background(), task)
			require.Equal(t, "queued", task.Status)
			require.Equal(t, "reserved", task.BillingStatus)
			require.Zero(t, billing.captures)
			require.Zero(t, billing.releases)
			require.Empty(t, logs.logs)
			require.Equal(t, raw, task.RawResponse)
			require.Equal(t, 1, upstream.submitted)
		})
	}
}

// 真实输出秒数必须是有限正数；缺失和非法值不能拿输入/合计秒数补齐。
func TestVideoMiniMaxOutputSecondsValidity(t *testing.T) {
	for _, value := range []string{`null`, `"5"`, `-1`, `0`, `1e309`, `{}`, `[]`} {
		t.Run(value, func(t *testing.T) {
			body := []byte(`{"task":{"id":"owned","status":"succeeded","usage":{"input_seconds":9,"total_seconds":9,"output_seconds":` + value + `}}}`)
			got := ExtractVideoUpstreamResponse(VideoEndpointMiniMax, body, "owned")
			require.Zero(t, got.Metadata.DurationSeconds)
			require.Nil(t, got.Metadata.Tokens)
		})
	}
	got := ExtractVideoUpstreamResponse(VideoEndpointMiniMax, []byte(`{"task":{"id":"owned","status":"succeeded","usage":{"output_seconds":2.5,"completion_tokens":0}}}`), "owned")
	require.Equal(t, 2.5, got.Metadata.DurationSeconds)
	require.NotNil(t, got.Metadata.Tokens)
	require.Zero(t, *got.Metadata.Tokens)
}

// 真实中继会在生成五秒视频时把三项 Token 填零，这个封装组合不能证明视频 Token 免费。
func TestVideoMiniMaxNestedZeroTokenPlaceholders(t *testing.T) {
	usage := `"usage":{"completion_tokens":0,"prompt_tokens":0,"total_tokens":0,"input_seconds":0,"output_seconds":5,"total_seconds":5}`
	body := []byte(`{"task":{"id":"owned","modality":"video","model":"MiniMax-H3-Max","status":"succeeded","task_type":"generation","content":{"url":"https://cdn.example/video.mp4"},` + usage + `}}`)
	got := ExtractVideoUpstreamResponse(VideoEndpointMiniMax, body, "owned")
	require.Equal(t, "completed", got.Status)
	require.Equal(t, 5.0, got.Metadata.DurationSeconds)
	require.Nil(t, got.Metadata.Tokens)

	// 平铺原生包仍保留明确零；没有真实输出秒数的新封装也不扩大占位判定。
	for _, endpoint := range []VideoEndpoint{VideoEndpointMiniMax, VideoEndpointSeedance, VideoEndpointKling, VideoEndpointWan} {
		flat := ExtractVideoUpstreamResponse(endpoint, []byte(`{"id":"owned","status":"succeeded",`+usage+`}`), "owned")
		require.NotNil(t, flat.Metadata.Tokens)
		require.Zero(t, *flat.Metadata.Tokens)
	}
	withoutSeconds := ExtractVideoUpstreamResponse(VideoEndpointMiniMax, []byte(`{"task":{"id":"owned","status":"succeeded","usage":{"completion_tokens":0,"prompt_tokens":0,"total_tokens":0}}}`), "owned")
	require.NotNil(t, withoutSeconds.Metadata.Tokens)
	require.Zero(t, *withoutSeconds.Metadata.Tokens)
}

// 已受理任务的真实嵌套轮询包应推进原账本，成功扣一次、失败释放一次，过期仍等待对账。
func TestVideoMiniMaxNestedTaskLifecycleSettlement(t *testing.T) {
	for _, mode := range []struct {
		mode                BillingMode
		price, hold, actual float64
	}{
		{BillingModeVideo, .425, 2.125, 2.125},
		{BillingModeVideoToken, 1, 0, .000321},
		{BillingModeVideoPerRequest, 2.125, 2.125, 2.125},
	} {
		for _, tc := range []struct {
			status, wantStatus, wantBilling string
			captures, releases              int
		}{
			{"succeeded", "completed", "settled", 1, 0},
			{"failed", "failed", "released", 0, 1},
			{"cancelled", "cancelled", "released", 0, 1},
			{"running", "processing", "reserved", 0, 0},
			{"expired", "expired", "reconciliation", 0, 0},
		} {
			t.Run(string(mode.mode)+"/"+tc.status, func(t *testing.T) {
				s, key, upstream, billing, logs := newVideoLifecycleFixture(mode.mode)
				key.Group.RateMultiplier = 1
				*key.Group.ModelPricing[0].VideoPrices[0].Price = mode.price
				upstream.selection.Target.Endpoint = VideoEndpointMiniMax
				upstream.selection.Metadata.DurationSeconds = 5
				created, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{
					Native: true, InboundProtocol: string(VideoEndpointMiniMax), NativePath: "/v2/video_generation",
					Body: []byte(`{"model":"video-model","resolution":"720p","duration":5}`),
				})
				require.NoError(t, err)
				task, err := s.repo.Get(context.Background(), created.LocalTaskID)
				require.NoError(t, err)
				require.Equal(t, "queued", task.Status)
				require.Equal(t, "reserved", task.BillingStatus)
				require.Equal(t, mode.hold, task.Hold.HoldAmount)
				completionTokens := 321
				if mode.mode == BillingModeVideo {
					// 按秒真实案例包含三项零 Token 占位，但五秒输出仍须正常结算 2.125。
					completionTokens = 0
				}
				body := []byte(fmt.Sprintf(`{"task":{"id":"upstream-task","modality":"video","model":"MiniMax-H3-Max","status":%q,"content":{"url":"https://cdn.example/video.mp4"},"usage":{"input_seconds":0,"output_seconds":5,"total_seconds":5,"completion_tokens":%d,"prompt_tokens":0,"total_tokens":%d}}}`, tc.status, completionTokens, completionTokens))
				upstream.poll = ExtractVideoUpstreamResponse(VideoEndpointMiniMax, body, task.UpstreamTaskID)
				upstream.poll.StatusCode, upstream.poll.Body = http.StatusOK, body
				s.advance(context.Background(), task)
				s.advance(context.Background(), task)
				require.Equal(t, tc.wantStatus, task.Status)
				require.Equal(t, tc.wantBilling, task.BillingStatus)
				require.Equal(t, tc.status, task.UpstreamStatus)
				require.Equal(t, tc.captures, billing.captures)
				require.Equal(t, tc.releases, billing.releases)
				require.Equal(t, 1, upstream.submitted, "查询和恢复不能补发生成")
				require.Equal(t, body, task.RawResponse)
				if tc.captures == 1 {
					require.InDelta(t, mode.actual, task.BillingResult.ActualAmountUSD, 1e-10)
					require.Equal(t, 5.0, task.Metadata.DurationSeconds)
					if mode.mode == BillingModeVideo {
						require.Nil(t, task.Metadata.Tokens)
					} else {
						require.NotNil(t, task.Metadata.Tokens)
						require.Equal(t, int64(321), *task.Metadata.Tokens)
					}
					require.Equal(t, "https://cdn.example/video.mp4", task.VideoURL)
					require.Len(t, logs.logs, 1)
				} else {
					require.Empty(t, logs.logs)
				}
			})
		}
	}
}

// 秒数和产物不能代替 Token 用量；旧任务待对账后可由同一任务的可靠用量恢复结算。
func TestVideoMiniMaxTokenMissingUsageRequiresReconciliation(t *testing.T) {
	s, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideoToken)
	key.Group.RateMultiplier = 1
	upstream.selection.Target.Endpoint = VideoEndpointMiniMax
	created, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{
		Native: true, InboundProtocol: string(VideoEndpointMiniMax), NativePath: "/v2/video_generation",
		Body: []byte(`{"model":"video-model","resolution":"720p","duration":5}`),
	})
	require.NoError(t, err)
	task, err := s.repo.Get(context.Background(), created.LocalTaskID)
	require.NoError(t, err)
	for _, usage := range []string{
		`{"input_seconds":0,"output_seconds":5,"total_seconds":5}`,
		`{"completion_tokens":0,"prompt_tokens":0,"total_tokens":0,"input_seconds":0,"output_seconds":5,"total_seconds":5}`,
	} {
		body := []byte(`{"task":{"id":"upstream-task","status":"succeeded","content":{"url":"https://cdn.example/video.mp4"},"usage":` + usage + `}}`)
		upstream.poll = ExtractVideoUpstreamResponse(VideoEndpointMiniMax, body, task.UpstreamTaskID)
		upstream.poll.StatusCode, upstream.poll.Body = http.StatusOK, body
		s.advance(context.Background(), task)
		require.Equal(t, "completed", task.Status)
		require.Equal(t, "reconciliation", task.BillingStatus)
		require.Nil(t, task.Metadata.Tokens)
		require.Zero(t, billing.captures)
		require.Zero(t, billing.releases)
		require.Empty(t, logs.logs)
	}
	body := []byte(`{"task":{"id":"upstream-task","status":"succeeded","usage":{"output_seconds":5,"completion_tokens":321}}}`)
	upstream.poll = ExtractVideoUpstreamResponse(VideoEndpointMiniMax, body, task.UpstreamTaskID)
	upstream.poll.StatusCode, upstream.poll.Body = http.StatusOK, body
	s.advance(context.Background(), task)
	s.advance(context.Background(), task)
	require.Equal(t, "settled", task.BillingStatus)
	require.InDelta(t, .000321, task.BillingResult.ActualAmountUSD, 1e-10)
	require.Equal(t, 1, billing.captures)
	require.Zero(t, billing.releases)
	require.Equal(t, 1, upstream.submitted)
	require.Len(t, logs.logs, 1)
}
