//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// 只提取明确的错误字段；HTML、非字符串、调试对象和未知字段不得回显。
func TestVideoAccountTestSafeErrorSummaryFields(t *testing.T) {
	for _, test := range []struct {
		name     string
		endpoint VideoEndpoint
		body     string
		want     string
	}{
		{"error-message", VideoEndpointCompat, `{"error":{"message":"duration must be 5 seconds"},"debug":"do-not-show"}`, "duration must be 5 seconds"},
		{"top-message", VideoEndpointCompat, `{"message":"不支持当前分辨率 480p"}`, "不支持当前分辨率 480p"},
		{"detail", VideoEndpointCompat, `{"detail":"unsupported model"}`, "unsupported model"},
		{"string-error", VideoEndpointCompat, `{"error":"invalid image type"}`, "invalid image type"},
		{"minimax", VideoEndpointMiniMax, `{"base_resp":{"status_code":1001,"status_msg":"reference image not supported"}}`, "reference image not supported"},
		{"minimax-task", VideoEndpointMiniMax, `{"task":{"error":{"message":"unsupported duration"}}}`, "unsupported duration"},
		{"wan", VideoEndpointWan, `{"output":{"message":"resolution must be 720P"}}`, "resolution must be 720P"},
		{"kling", VideoEndpointKling, `{"data":{"task_status_msg":"invalid first frame"}}`, "invalid first frame"},
		{"vidu", VideoEndpointVidu, `{"err_msg":"images must contain one item"}`, "images must contain one item"},
		{"nested-message", VideoEndpointCompat, `{"error":{"message":"{\"error\":{\"message\":\"invalid resolution\"},\"debug\":\"do-not-show\"}"}}`, "invalid resolution"},
		{"object-detail", VideoEndpointCompat, `{"detail":{"headers":{"private":"do-not-show"}}}`, ""},
		{"embedded-request", VideoEndpointCompat, `{"message":"request rejected: {\"headers\":\"do-not-show\"}"}`, ""},
		{"unknown-field", VideoEndpointCompat, `{"debug":"do-not-show"}`, ""},
		{"html", VideoEndpointCompat, `<html>do-not-show</html>`, ""},
		{"json-html", VideoEndpointCompat, `{"message":"<html>do-not-show</html>"}`, ""},
		{"empty", VideoEndpointCompat, ``, ""},
		{"malformed-json", VideoEndpointCompat, `{"message":"do-not-show"`, ""},
		{"non-object", VideoEndpointCompat, `["do-not-show"]`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := videoAccountTestResponseError(test.endpoint, &VideoUpstreamResponse{StatusCode: 400, Body: []byte(test.body)})
			require.Error(t, err)
			want := "Video upstream returned HTTP 400"
			if test.want != "" {
				want += ": " + test.want
			}
			require.Equal(t, want, err.Error())
			require.NotContains(t, err.Error(), "do-not-show")
		})
	}
	// 业务拒绝仍保持失败，HTTP 200 不能被可读摘要误当成功。
	err := videoAccountTestResponseError(VideoEndpointMiniMax, &VideoUpstreamResponse{StatusCode: 200, Body: []byte(`{"base_resp":{"status_code":1001,"status_msg":"invalid resolution"}}`)})
	require.EqualError(t, err, "Video upstream returned a business error (HTTP 200): invalid resolution")
}

// 明文、带标签凭据、素材地址、Base64 与控制字符都不能通过错误字符串泄露到 SSE。
func TestVideoAccountTestSafeErrorSummaryRedaction(t *testing.T) {
	opaque := strings.Repeat("ABcdEFgh12+_", 8)
	message := "unsupported image: http://user:password@media.example/path/image.png?signature=private; " +
		"data:image/png;base64,shortencoded; Bearer short-token; sk-testsecret; api_key=smallkey; " +
		"fixture-secret custom-header-secret " + opaque + "\r\nresolution=768p\u202e"
	body, err := json.Marshal(map[string]any{"error": map[string]string{"message": message}})
	require.NoError(t, err)
	summary := videoAccountTestUpstreamErrorSummary(VideoEndpointCompat, body, []string{"fixture-secret", "custom-header-secret"})
	require.Contains(t, summary, "unsupported image")
	require.Contains(t, summary, "resolution=768p")
	for _, forbidden := range []string{"media.example", "private", "shortencoded", "short-token", "testsecret", "smallkey", "fixture-secret", "custom-header-secret", opaque, "\r", "\n", "\u202e"} {
		require.NotContains(t, summary, forbidden)
	}
	longBody, err := json.Marshal(map[string]string{"message": strings.Repeat("错误原因。", 150)})
	require.NoError(t, err)
	summary = videoAccountTestUpstreamErrorSummary(VideoEndpointCompat, longBody, nil)
	require.True(t, utf8.ValidString(summary))
	require.Equal(t, 401, utf8.RuneCountInString(summary))
	hugeBody, err := json.Marshal(map[string]string{"message": strings.Repeat("a", 8193)})
	require.NoError(t, err)
	require.Empty(t, videoAccountTestUpstreamErrorSummary(VideoEndpointCompat, hugeBody, nil))
}

// 创建和轮询的 HTTP 错误均展示安全原因，但创建最多一次，不因诊断再次生成。
func TestVideoAccountTestHTTPErrorSummaryNeverResubmits(t *testing.T) {
	for _, onPoll := range []bool{false, true} {
		t.Run(map[bool]string{false: "create-400", true: "poll-400"}[onPoll], func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointCompat)
			account.Credentials["header_override_enabled"] = true
			account.Credentials["header_overrides"] = map[string]any{"X-Custom-Auth": "custom-header-secret"}
			posts := 0
			svc, upstream := videoAccountTestService(account, func(r *http.Request, _ []byte) (*http.Response, error) {
				if r.Method == http.MethodPost {
					posts++
					if onPoll {
						return videoAccountTestJSON(202, `{"id":"task-1","status":"queued"}`), nil
					}
				}
				return videoAccountTestJSON(400, `{"error":{"message":"unsupported resolution 480p; fixture-secret custom-header-secret https://media.example/private.png data:image/png;base64,shortencoded"},"debug":{"headers":"do-not-show"}}`), nil
			})
			c, recorder := grokModesContext(context.Background())
			err := svc.TestAccountConnectionWithOptions(c, account.ID, "m", "", "video", "", "", AccountTestOptions{VideoEndpoint: "compat"})
			require.Error(t, err)
			require.Equal(t, 1, posts)
			wantRequests := 1
			if onPoll {
				wantRequests++
			}
			require.Len(t, upstream.requests, wantRequests)
			output := recorder.Body.String()
			require.Contains(t, output, "HTTP 400")
			require.Contains(t, output, "unsupported resolution 480p")
			require.NotContains(t, output, `"success":true`)
			for _, forbidden := range []string{"fixture-secret", "custom-header-secret", "media.example", "shortencoded", "do-not-show"} {
				require.NotContains(t, output, forbidden)
			}
		})
	}
}
