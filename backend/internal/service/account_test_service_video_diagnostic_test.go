//go:build unit

package service

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 诊断必须能区分字段结构和非法路径，同时不泄露原包，也不能重发创建或猜测 ID 查询。
func TestVideoAccountTestTaskIDDiagnosticDoesNotRetryOrExposeResponseValues(t *testing.T) {
	for _, tc := range []struct {
		name     string
		endpoint VideoEndpoint
		body     string
		fields   []string
		unsafeID bool
	}{
		{"minimax_missing", VideoEndpointMiniMax, `{"status":"pending"}`, []string{"TaskId=missing", "task=missing", "task.id=missing", "id=missing"}, false},
		{"minimax_number", VideoEndpointMiniMax, `{"task":{"id":987654321009876543,"task_id":null}}`, []string{"task=object", "task.id=number", "task.task_id=null"}, false},
		{"minimax_unsafe_path", VideoEndpointMiniMax, `{"task":{"id":"../private-task-value","status":"pending"}}`, []string{"task.id=string"}, true},
		{"minimax_unknown_envelope", VideoEndpointMiniMax, `{"data":{"id":"unrecognized-task-value"}}`, []string{"data=object", "data.id=string"}, false},
		{"minimax_boolean_and_array", VideoEndpointMiniMax, `{"task":{"id":false,"task_id":true},"task_id":null,"data":[],"id":{}}`, []string{"task.id=boolean", "task.task_id=boolean", "task_id=null", "data=array", "id=object"}, false},
		{"minimax_string_envelope", VideoEndpointMiniMax, `{"task":"private-envelope-value"}`, []string{"task=string", "task.id=missing"}, false},
		{"minimax_null_envelope", VideoEndpointMiniMax, `{"task":null}`, []string{"task=null"}, false},
		{"wan_number", VideoEndpointWan, `{"output":{"task_id":987654321009876543}}`, []string{"output=object", "output.task_id=number"}, false},
		{"kling_number", VideoEndpointKling, `{"data":{"task_id":987654321009876543}}`, []string{"data=object", "data.task_id=number"}, false},
		{"compat_number", VideoEndpointCompat, `{"id":987654321009876543}`, []string{"id=number", "output=missing"}, false},
		{"openai_number", VideoEndpointOpenAIVideos, `{"id":987654321009876543}`, []string{"id=number", "data=missing"}, false},
		{"seedance_number", VideoEndpointSeedance, `{"id":987654321009876543}`, []string{"id=number", "task_id=missing"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := videoFixtureAccount(tc.endpoint)
			// 敏感内容同时放在任意键和值中，确保诊断没有枚举或输出供应商正文。
			body := strings.TrimSuffix(tc.body, "}") + `,"private_debug_key":"https://private.example/?key=fixture-secret","private_unknown_key":{"nested":"private-nested-value"}}`
			svc, upstream := videoAccountTestService(account, func(r *http.Request, _ []byte) (*http.Response, error) {
				require.Equal(t, http.MethodPost, r.Method)
				return videoAccountTestJSON(http.StatusAccepted, body), nil
			})
			c, recorder := grokModesContext(context.Background())
			require.Error(t, svc.TestAccountConnectionWithOptions(c, account.ID, "m", "", "video", "", "", AccountTestOptions{VideoEndpoint: string(tc.endpoint)}))
			require.Len(t, upstream.requests, 1)
			output := recorder.Body.String()
			require.Contains(t, output, "HTTP 202")
			require.Contains(t, output, "上游可能已开始生成，不会自动重复提交")
			if tc.unsafeID {
				require.Contains(t, output, "任务 ID 不能安全用于查询路径")
				require.NotContains(t, output, "未识别到任务 ID")
			} else {
				require.Contains(t, output, "未识别到任务 ID")
				require.NotContains(t, output, "不能安全用于查询路径")
			}
			for _, field := range tc.fields {
				require.Contains(t, output, field)
			}
			for _, secret := range []string{"private_debug_key", "private_unknown_key", "private.example", "fixture-secret", "private-nested-value", "private-task-value", "private-envelope-value", "unrecognized-task-value", "987654321009876543"} {
				require.NotContains(t, output, secret)
			}
			require.NotContains(t, output, `"success":true`)
		})
	}
}

// 已确认的 MiniMax 创建别名必须接续原账号查询，只提交一次且不改变测试的免站内计费边界。
func TestVideoAccountTestMiniMaxTaskIdCreationAndNestedCompletion(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointMiniMax)
	svc, upstream := videoAccountTestService(account, func(r *http.Request, _ []byte) (*http.Response, error) {
		switch r.Method {
		case http.MethodPost:
			require.Equal(t, "/prefix/v2/video_generation", r.URL.Path)
			return videoAccountTestJSON(http.StatusOK, `{"TaskId":"minimax-created-task"}`), nil
		case http.MethodGet:
			require.Equal(t, "/prefix/v2/query/video_generation/minimax-created-task", r.URL.Path)
			return videoAccountTestJSON(http.StatusOK, videoMiniMaxTaskIdCompletedFixture), nil
		default:
			t.Fatalf("unexpected method: %s", r.Method)
			return nil, nil
		}
	})
	c, recorder := grokModesContext(context.Background())
	require.NoError(t, svc.TestAccountConnectionWithOptions(c, account.ID, "MiniMax-H3-open", "", "video", "", "", AccountTestOptions{VideoEndpoint: "minimax", Duration: videoAccountTestInt(5), Ratio: "16:9"}))
	require.Len(t, upstream.requests, 2)
	require.Equal(t, http.MethodPost, upstream.requests[0].Method)
	require.Equal(t, http.MethodGet, upstream.requests[1].Method)
	require.Contains(t, recorder.Body.String(), "视频任务已创建：minimax-created-task")
	require.Contains(t, recorder.Body.String(), `"success":true`)
	require.Contains(t, recorder.Body.String(), `"type":"video"`)
	require.NotContains(t, recorder.Body.String(), "已知响应字段")
	require.NotContains(t, recorder.Body.String(), "fixture-secret")
}

// 新诊断不能覆盖明确 HTTP 错误、业务错误、冲突归属或非法 JSON 的既有处理。
func TestVideoAccountTestTaskIDDiagnosticPreservesEarlierErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body, message string
		status              int
	}{
		{"http_error", `{"status":"pending"}`, "Video upstream returned HTTP 503", http.StatusServiceUnavailable},
		{"business_error", `{"base_resp":{"status_code":1001}}`, "Video upstream returned a business error", http.StatusOK},
		{"conflict", `{"task":{"id":"one","task_id":"two"}}`, "Video creation returned conflicting task IDs", http.StatusOK},
		{"duplicate", `{"task":{"id":"one","id":"two"}}`, "Video creation returned conflicting task IDs", http.StatusOK},
		{"invalid_json", `not-json-private-value`, "Video upstream returned an invalid JSON response", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointMiniMax)
			svc, upstream := videoAccountTestService(account, func(r *http.Request, _ []byte) (*http.Response, error) {
				require.Equal(t, http.MethodPost, r.Method)
				return videoAccountTestJSON(tc.status, tc.body), nil
			})
			c, recorder := grokModesContext(context.Background())
			require.Error(t, svc.TestAccountConnectionWithOptions(c, account.ID, "m", "", "video", "", "", AccountTestOptions{VideoEndpoint: "minimax"}))
			require.Len(t, upstream.requests, 1)
			require.Contains(t, recorder.Body.String(), tc.message)
			require.NotContains(t, recorder.Body.String(), "已知响应字段")
			require.NotContains(t, recorder.Body.String(), "not-json-private-value")
		})
	}
}
