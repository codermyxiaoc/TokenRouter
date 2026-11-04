//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func mjJSONResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func shortenMidjourneyPoll(t *testing.T) {
	t.Helper()
	old := midjourneyAsyncPollInterval
	midjourneyAsyncPollInterval = time.Millisecond
	t.Cleanup(func() { midjourneyAsyncPollInterval = old })
}

// 异步信封探测不能截断较大的同步图片，也不能吞掉原响应的读取错误。
func TestMidjourneyAsyncDetectionPreservesSynchronousBody(t *testing.T) {
	for _, length := range []int{32, midjourneyAsyncMaxResponseBytes + 20} {
		body := `{"data":[{"b64_json":"` + strings.Repeat("a", length) + `"}]}`
		resp := mjJSONResponse(body)
		err := (&OpenAIGatewayService{}).resolveMidjourneyAsyncResponse(context.Background(), nil, nil, resp, nil, "")
		require.NoError(t, err)
		actual, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, len(body), len(actual))
		require.Equal(t, body, string(actual))
	}
	readErr := errors.New("interrupted image response")
	resp := mjJSONResponse("")
	resp.Body = io.NopCloser(io.MultiReader(strings.NewReader(`{"data":[{"url":"https://img.example/a.png"}]}`), iotest.ErrReader(readErr)))
	err := (&OpenAIGatewayService{}).resolveMidjourneyAsyncResponse(context.Background(), nil, nil, resp, nil, "")
	require.ErrorIs(t, err, readErr)
}

// 实测上游异步响应（HTTP 200、只有任务 ID 与 pending）被识别为异步；带 data 的同步结果不是。
func TestMidjourneyAsyncTaskDetection(t *testing.T) {
	id, async := midjourneyAsyncTaskID([]byte(`{"created":1793775082,"id":"1251015623-AigcImageTask-4b10t","object":"image.generation","status":"pending"}`))
	require.True(t, async)
	require.Equal(t, "1251015623-AigcImageTask-4b10t", id)
	for _, body := range []string{
		`{"created":1,"data":[{"url":"https://img.example/a.png"}],"id":"x"}`, // 同步结果
		`{"status":"pending"}`, // 没有任务 ID
		`{"id":"x"}`,           // 没有状态
		`{`,                    // 非 JSON
	} {
		_, async := midjourneyAsyncTaskID([]byte(body))
		require.False(t, async, body)
	}
	taskURL, err := midjourneyTaskURL("https://token.xinhankr.com/v1/images/generations", "a b/c")
	require.NoError(t, err)
	require.Equal(t, "https://token.xinhankr.com/v1/images/generations/a%20b%2Fc", taskURL)
	_, err = midjourneyTaskURL("https://token.xinhankr.com/v1/images/edits", "x")
	require.Error(t, err)

	rewritten, err := rewriteMidjourneyImagesBodyToURL([]byte(`{"model":"MJ-v8.2","prompt":"x","n":1,"response_format":"b64_json"}`))
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(rewritten, "response_format").Exists())
	require.Equal(t, "MJ-v8.2", gjson.GetBytes(rewritten, "model").String())
}

// 轮询结局：拿到 data 返回；上游明确失败为失败；完成却无数据、任务不存在、超时都为无法确认；网络错误与 5xx 继续重试。
func TestPollMidjourneyTaskOutcomes(t *testing.T) {
	shortenMidjourneyPoll(t)
	done := `{"id":"t","status":"completed","data":[{"url":"https://img.example/a.png"}]}`
	sequence := func(steps ...func() (*http.Response, error)) midjourneyTaskRequester {
		i := 0
		return func(context.Context, string) (*http.Response, error) {
			step := steps[min(i, len(steps)-1)]
			i++
			return step()
		}
	}
	ok := func(status int, body string) func() (*http.Response, error) {
		return func() (*http.Response, error) {
			resp := mjJSONResponse(body)
			resp.StatusCode = status
			return resp, nil
		}
	}
	netErr := func() (*http.Response, error) { return nil, errors.New("connection reset") }
	deadline := time.Now().Add(5 * time.Second)

	body, err := pollMidjourneyTask(context.Background(), "u", deadline, sequence(ok(200, `{"id":"t","status":"pending"}`), netErr, ok(502, `bad`), ok(200, done)))
	require.NoError(t, err)
	require.JSONEq(t, done, string(body))

	_, err = pollMidjourneyTask(context.Background(), "u", deadline, sequence(ok(200, `{"id":"t","status":"failed"}`)))
	require.ErrorIs(t, err, errMidjourneyTaskFailed)
	_, err = pollMidjourneyTask(context.Background(), "u", deadline, sequence(ok(200, `{"id":"t","status":"completed"}`)))
	require.ErrorIs(t, err, errMidjourneyTaskUnconfirmed, "b64 任务完成后无数据，不能当作成功")
	_, err = pollMidjourneyTask(context.Background(), "u", deadline, sequence(ok(404, `{}`)))
	require.ErrorIs(t, err, errMidjourneyTaskUnconfirmed)
	_, err = pollMidjourneyTask(context.Background(), "u", time.Now().Add(20*time.Millisecond), sequence(ok(200, `{"id":"t","status":"pending"}`)))
	require.ErrorIs(t, err, errMidjourneyTaskUnconfirmed, "超过截止时间仍未完成")
}

// 创作台：请求不带 response_format；上游转为异步后按任务 ID 轮询，拿到四张图并只提交一次生成。
func TestCreativeMidjourneyAsyncTaskIsPolled(t *testing.T) {
	shortenMidjourneyPoll(t)
	items := make([]map[string]string, 4)
	for i := range items {
		items[i] = map[string]string{"b64_json": base64.StdEncoding.EncodeToString(creativeMJTestPNG(t, uint8(i+10)))}
	}
	completed, err := json.Marshal(map[string]any{"id": "task-1", "status": "completed", "data": items})
	require.NoError(t, err)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		mjJSONResponse(`{"created":1,"id":"task-1","object":"image.generation","status":"pending"}`),
		mjJSONResponse(`{"created":1,"id":"task-1","object":"image.generation","status":"pending"}`),
		mjJSONResponse(string(completed)),
	}}
	gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	executor := &CreativeExecutor{gateway: gateway}
	account := &Account{ID: 1, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"api_key": "test-key", "base_url": "https://mj.example.test"}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	outputs, err := executor.executeOpenAI(ctx, creativeMJTestRun(), CreativeRunPayload{Prompt: "teapot"}, account, "MJ-v8.2")
	require.NoError(t, err)
	require.Len(t, outputs, 4)
	require.Len(t, upstream.requests, 3)
	require.Equal(t, http.MethodPost, upstream.requests[0].Method, "只提交一次生成")
	require.False(t, gjson.GetBytes(upstream.bodies[0], "response_format").Exists(), "MJ 不请求 b64_json")
	for _, req := range upstream.requests[1:] {
		require.Equal(t, http.MethodGet, req.Method)
		require.Equal(t, "https://mj.example.test/v1/images/generations/task-1", req.URL.String())
		require.Equal(t, "Bearer test-key", req.Header.Get("Authorization"))
	}
}

// 创作台：上游明确失败不扣费（不是无法确认）；完成却无数据保留为无法确认，均不重新提交生成。
func TestCreativeMidjourneyAsyncTaskFailures(t *testing.T) {
	shortenMidjourneyPoll(t)
	for _, tc := range []struct {
		name        string
		final       string
		unconfirmed bool
	}{
		{"failed", `{"id":"task-1","status":"failed"}`, false},
		{"completed without data", `{"id":"task-1","status":"completed"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				mjJSONResponse(`{"id":"task-1","status":"pending"}`), mjJSONResponse(tc.final),
			}}
			executor := &CreativeExecutor{gateway: &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}}
			account := &Account{ID: 1, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"api_key": "test-key", "base_url": "https://mj.example.test"}}
			_, err := executor.executeOpenAI(context.Background(), creativeMJTestRun(), CreativeRunPayload{Prompt: "teapot"}, account, "MJ-v8.2")
			require.Error(t, err)
			var unconfirmed *CreativeProviderResultUnconfirmedError
			require.Equal(t, tc.unconfirmed, errors.As(err, &unconfirmed))
			require.False(t, IsRetryableCreativeError(err))
			require.Equal(t, http.MethodPost, upstream.requests[0].Method)
			for _, req := range upstream.requests[1:] {
				require.Equal(t, http.MethodGet, req.Method, "不重新提交生成")
			}
		})
	}
}

// 创作台 MJ 的总等待预算至少 15 分钟（上游同步约 5 分钟、异步完成再需数分钟），其它模型不变。
func TestCreativeImageBudgetForMidjourney(t *testing.T) {
	require.Equal(t, MidjourneyImageExecutionBudget, creativeImageBudgetForModel(5*time.Minute, "MJ-v8.2"))
	require.Equal(t, 20*time.Minute, creativeImageBudgetForModel(20*time.Minute, "MJ-v8.2"), "配置更长时保留配置")
	require.Equal(t, 5*time.Minute, creativeImageBudgetForModel(5*time.Minute, "gpt-image-2"))
}

// API：MJ 转发去掉 response_format；异步任务轮询到完成后按同步结果返回并计数，其它模型保持原请求。
func TestForwardImagesMidjourneyAsyncTaskIsPolled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	shortenMidjourneyPoll(t)
	img := base64.StdEncoding.EncodeToString(creativeMJTestPNG(t, 7))
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		mjJSONResponse(`{"created":1,"id":"task-9","object":"image.generation","status":"pending"}`),
		mjJSONResponse(`{"id":"task-9","status":"processing"}`),
		mjJSONResponse(`{"created":1,"id":"task-9","status":"completed","data":[{"b64_json":"` + img + `"},{"b64_json":"` + img + `"},{"b64_json":"` + img + `"},{"b64_json":"` + img + `"}]}`),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "mj-key", "base_url": "https://mj.example.test"}}
	body := []byte(`{"model":"MJ-v8.2","prompt":"apple","n":1,"response_format":"b64_json"}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	parsed := &OpenAIImagesRequest{Model: "MJ-v8.2", Endpoint: openAIImagesGenerationsEndpoint, ContentType: "application/json", N: 1, ResponseFormat: "b64_json", Body: body}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := svc.ForwardImages(ctx, c, account, body, parsed, "")
	require.NoError(t, err)
	require.Equal(t, 4, result.ImageCount)
	require.Len(t, upstream.requests, 3)
	require.Equal(t, http.MethodPost, upstream.requests[0].Method)
	require.False(t, gjson.GetBytes(upstream.bodies[0], "response_format").Exists())
	require.Equal(t, "https://mj.example.test/v1/images/generations/task-9", upstream.requests[2].URL.String())
	require.Equal(t, "Bearer mj-key", upstream.requests[2].Header.Get("Authorization"))
	require.Len(t, gjson.GetBytes(rec.Body.Bytes(), "data").Array(), 4)

	// 上游明确失败：按明确拒绝返回，便于释放预占。
	upstream = &httpUpstreamRecorder{responses: []*http.Response{
		mjJSONResponse(`{"id":"task-9","status":"pending"}`), mjJSONResponse(`{"id":"task-9","status":"failed"}`),
	}}
	svc = &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, _ = gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	_, err = svc.ForwardImages(ctx, c, account, body, parsed, "")
	decision, ok := ImageAttemptDecisionForError(err)
	require.True(t, ok)
	require.Equal(t, ImageAttemptRejected, decision.Outcome)

	// 非 MJ 模型保持原请求，不改 response_format。
	upstream = &httpUpstreamRecorder{resp: mjJSONResponse(`{"data":[{"b64_json":"aW1hZ2U="}]}`)}
	svc = &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	plainBody := []byte(`{"model":"gpt-image-1","prompt":"apple","n":1,"response_format":"b64_json"}`)
	c, _ = gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(plainBody))
	_, err = svc.ForwardImages(ctx, c, account, plainBody, &OpenAIImagesRequest{Model: "gpt-image-1", Endpoint: openAIImagesGenerationsEndpoint, ContentType: "application/json", N: 1, ResponseFormat: "b64_json", Body: plainBody}, "")
	require.NoError(t, err)
	require.Equal(t, "b64_json", gjson.GetBytes(upstream.lastBody, "response_format").String())
}
