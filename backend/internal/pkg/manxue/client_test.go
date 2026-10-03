package manxue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 使用独立假服务验证契约，测试不访问真实平台，也不使用生产密钥。
func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClientWithBaseURL(server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func createInput(benchmark string) CreateRequest {
	return CreateRequest{Benchmark: benchmark, BaseURL: "https://gateway.example/v1", APIKey: "sk-unit-test-only", Model: "unit-model", Protocol: "responses"}
}

func TestCreateAndGetCandy(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("不应向检测服务发送站内认证或Cookie")
		}
		if r.Method == http.MethodPost {
			if r.URL.Path != "/api/v1/tests" || r.Header.Get("Idempotency-Key") != "monitor_123" {
				t.Error("创建路径或幂等键错误")
			}
			var body CreateRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.APIKey != "sk-unit-test-only" || body.BaseURL != "https://gateway.example/v1" || body.Protocol != "responses" {
				t.Error("创建参数未按契约发送")
			}
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"id":"task_123","benchmark":"candy","status":"running","phase":"generating","expires_at":"2026-10-03T10:00:00Z"}`)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/tests/task_123" {
			t.Error("查询路径错误")
		}
		fmt.Fprint(w, `{"id":"task_123","benchmark":"candy","status":"succeeded","phase":"complete","usage_reported":true,"candy":{"status":"passed","question":"题目","answer":"答案 21","started_at":"2026-10-03T08:00:00Z","finished_at":"2026-10-03T08:00:42Z","duration_ms":42000,"input_tokens":4317,"output_tokens":1159}}`)
	})
	input := createInput("candy")
	input.Protocol = ""
	result, err := client.Create(context.Background(), input, "monitor_123")
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "task_123" || result.Terminal() || result.Outcome() != "pending" {
		t.Fatal("创建状态解析错误")
	}
	result, err = client.Get(context.Background(), result.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Terminal() || result.Outcome() != "passed" || result.Candy.Question != "题目" || result.Candy.Answer != "答案 21" || *result.Candy.DurationMS != 42000 || *result.Candy.InputTokens != 4317 || result.Candy.ReasoningTokens != nil {
		t.Fatal("糖果结果或可选统计解析错误")
	}
	if calls.Load() != 2 {
		t.Fatal("请求次数异常")
	}
}

func TestDrawingResultAndLegacyAssessment(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"benchmark":"pelican","status":"succeeded","result":{"html":"<svg><text>作品</text></svg>","has_html":true,"duration_ms":1000,"output_tokens":0},"assessment":{"quality":"suspicious","label":"疑似降智","reason":"缺少指定元素","source":"classifier"}}`)
	})
	result, err := client.Get(context.Background(), "task_drawing")
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "task_drawing" || result.Result.HTML != "<svg><text>作品</text></svg>" || result.Result.OutputTokens == nil || *result.Result.OutputTokens != 0 || result.Result.InputTokens != nil || result.Outcome() != "failed" {
		t.Fatal("作品或历史质量结果解析错误")
	}
}

func TestOutcomeNeverTreatsUnknownAsPassed(t *testing.T) {
	cases := []struct {
		name     string
		result   TestResult
		outcome  string
		terminal bool
	}{
		{"unknown task", TestResult{Status: "future_status", Benchmark: "candy", Candy: &CandyResult{Status: "passed"}}, "unknown", false},
		{"unknown candy", TestResult{Status: "succeeded", Benchmark: "candy", Candy: &CandyResult{Status: "future_status"}}, "unknown", true},
		{"unknown quality", TestResult{Status: "succeeded", Benchmark: "pelican", Assessment: &Assessment{Quality: "future_status"}}, "unknown", true},
		{"missing candy", TestResult{Status: "succeeded", Benchmark: "candy"}, "unknown", true},
		{"missing assessment", TestResult{Status: "succeeded", Benchmark: "pelican"}, "unknown", true},
		{"incorrect", TestResult{Status: "succeeded", Benchmark: "candy", Candy: &CandyResult{Status: "incorrect"}}, "failed", true},
		{"candy error", TestResult{Status: "succeeded", Benchmark: "candy", Candy: &CandyResult{Status: "error"}}, "error", true},
		{"failed", TestResult{Status: "failed", Benchmark: "candy", Candy: &CandyResult{Status: "passed"}}, "error", true},
		{"cancelled", TestResult{Status: "cancelled"}, "error", true},
		{"normal", TestResult{Status: "succeeded", Benchmark: "pelican", Assessment: &Assessment{Quality: "normal"}}, "passed", true},
		{"remote error overrides pass", TestResult{Status: "succeeded", Benchmark: "candy", Candy: &CandyResult{Status: "passed"}, Error: &RemoteError{Code: "remote_error"}}, "error", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.result.Outcome() != tc.outcome || tc.result.Terminal() != tc.terminal {
				t.Fatal("状态映射错误")
			}
		})
	}
}

func TestNoRedirectOrPostRetry(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var leaked atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
			defer destination.Close()
			var calls atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", destination.URL+"/capture")
				w.WriteHeader(status)
			})
			_, err := client.Create(context.Background(), createInput("candy"), "safe-id")
			if !errors.Is(err, ErrRedirect) || calls.Load() != 1 || leaked.Load() != 0 {
				t.Fatalf("重定向或重复提交未受阻止: %v", err)
			}
		})
	}
}

func TestHTTPErrorIsSafeAndRetryAfterAvailable(t *testing.T) {
	for _, status := range []int{400, 404, 409, 413, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "17")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":"sk-secret-value private-task-id raw-body"}`)
			})
			_, err := client.Create(context.Background(), createInput("pelican"), "once")
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) || httpErr.StatusCode != status || httpErr.RetryAfter != 17*time.Second || calls.Load() != 1 {
				t.Fatalf("错误类型或请求次数异常: %v", err)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private-task") || strings.Contains(err.Error(), "raw-body") {
				t.Fatal("错误泄漏凭据或正文")
			}
		})
	}
}

func TestRemoteErrorsAreSanitized(t *testing.T) {
	for _, remote := range []string{`"sk-secret-value task-private"`, `{"code":"sk-secret-value","message":"task-private body"}`} {
		client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"status":"failed","error":%s,"candy":{"status":"error","error":%s}}`, remote, remote)
		})
		result, err := client.Get(context.Background(), "test-task")
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(result)
		if result.Error.Code != "remote_error" || result.Candy.Error.Code != "remote_error" || strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "task-private") {
			t.Fatal("远端错误未脱敏")
		}
	}
}

func TestResponseLimitsAndInvalidResponses(t *testing.T) {
	cases := []struct {
		name string
		body string
		want error
	}{
		{"body limit", strings.Repeat(" ", MaxResponseBytes+1), ErrResponseTooLarge},
		{"HTML limit", `{"status":"succeeded","result":{"html":"` + strings.Repeat("x", MaxHTMLBytes+1) + `"}}`, ErrResponseTooLarge},
		{"invalid JSON", `{`, ErrInvalidResponse},
		{"missing status", `{}`, ErrInvalidResponse},
		{"null result", `null`, ErrInvalidResponse},
		{"multiple documents", `{"status":"running"}{"status":"succeeded"}`, ErrInvalidResponse},
		{"conflicting ID", `{"id":"other-task","status":"running"}`, ErrInvalidResponse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) })
			_, err := client.Get(context.Background(), "own-task")
			if !errors.Is(err, tc.want) {
				t.Fatalf("预期 %v，实际 %v", tc.want, err)
			}
		})
	}
}

func TestCreateValidatesBeforeSending(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	cases := []struct {
		name   string
		change func(*CreateRequest)
		key    string
	}{
		{"benchmark", func(r *CreateRequest) { r.Benchmark = "arbitrary" }, "ok"},
		{"candy chat", func(r *CreateRequest) { r.Protocol = "chat_completions" }, "ok"},
		{"protocol", func(r *CreateRequest) { r.Protocol = "invalid" }, "ok"},
		{"key missing", func(r *CreateRequest) { r.APIKey = "" }, "ok"},
		{"model missing", func(r *CreateRequest) { r.Model = " " }, "ok"},
		{"insecure target", func(r *CreateRequest) { r.BaseURL = "http://target.example" }, "ok"},
		{"URL credentials", func(r *CreateRequest) { r.BaseURL = "https://user:secret@target.example" }, "ok"},
		{"URL query", func(r *CreateRequest) { r.BaseURL = "https://target.example?key=secret" }, "ok"},
		{"reasoning", func(r *CreateRequest) { r.ReasoningEffort = "invalid" }, "ok"},
		{"service tier", func(r *CreateRequest) { r.ServiceTier = "invalid" }, "ok"},
		{"idempotency path", func(r *CreateRequest) {}, "../secret"},
		{"idempotency too long", func(r *CreateRequest) {}, strings.Repeat("a", 129)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := createInput("candy")
			tc.change(&input)
			_, err := client.Create(context.Background(), input, tc.key)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("无效输入未拒绝: %v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("无效输入发送了外部请求")
	}
	for _, id := range []string{"", "../task", "task?key=secret", "task/extra", "task#fragment", "task%2Fextra", strings.Repeat("a", 257)} {
		_, err := client.Get(context.Background(), id)
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatal("无效任务ID未拒绝")
		}
	}
}

func TestCancellationAndTimeoutDoNotLeakURL(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Get(ctx, "private-task")
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "private-task") || strings.Contains(err.Error(), "http") {
		t.Fatalf("取消错误处理不正确: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = client.Get(ctx, "private-task")
	var transport *TransportError
	if !errors.As(err, &transport) || !transport.Timeout || !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "private-task") {
		t.Fatalf("超时错误处理不正确: %v", err)
	}
}

func TestTimeoutDuringBodyRead(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := client.Get(ctx, "private-task")
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "private-task") {
		t.Fatalf("读取响应体的超时未正确分类: %v", err)
	}
}

type testRoundTripper func(*http.Request) (*http.Response, error)

func (f testRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCreateDisablesBodyReplay(t *testing.T) {
	var calls int
	client := NewClient(&http.Client{Transport: testRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.GetBody != nil {
			t.Error("带凭据创建请求不得提供自动回放函数")
		}
		return nil, errors.New("connection reset sk-secret private-task")
	})})
	_, err := client.Create(context.Background(), createInput("candy"), "dedupe")
	if !errors.Is(err, ErrTransport) || calls != 1 || strings.Contains(err.Error(), "secret") {
		t.Fatalf("网络失败重复提交或未脱敏: %v", err)
	}
}

func TestClientCloneAndSafeFormatting(t *testing.T) {
	caller := &http.Client{}
	client := NewClient(caller)
	if caller.Timeout != 0 || caller.CheckRedirect != nil || client.httpClient.Timeout != defaultTimeout || client.baseURL != DefaultBaseURL {
		t.Fatal("修改了调用方client或生产服务地址")
	}
	input := createInput("candy")
	for _, formatted := range []string{fmt.Sprint(input), fmt.Sprintf("%+v", input), fmt.Sprintf("%#v", input), fmt.Sprintf("%+v", TestResult{ID: "private-task"})} {
		if strings.Contains(formatted, "sk-unit") || strings.Contains(formatted, "private-task") {
			t.Fatal("格式化日志未脱敏")
		}
	}
	for _, base := range []string{"http://outside.example", "https://u:p@manxue.ai", "https://manxue.ai?x=secret", "https://manxue.ai/path", "https://manxue.ai#secret"} {
		if _, err := NewClientWithBaseURL(nil, base); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal("无效服务地址未拒绝")
		}
	}
}

func TestRetryAfterDateAndLimit(t *testing.T) {
	now := time.Date(2026, 10, 3, 1, 2, 3, 0, time.UTC)
	if parseRetryAfter(now.Add(time.Minute).Format(http.TimeFormat), now) != time.Minute {
		t.Fatal("日期形式Retry-After解析错误")
	}
	if parseRetryAfter("9999999999", now) != 24*time.Hour || parseRetryAfter("-1", now) != 0 || parseRetryAfter("invalid", now) != 0 {
		t.Fatal("Retry-After边界处理错误")
	}
}
