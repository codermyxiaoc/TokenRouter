// Package manxue 提供降智检测服务的受限客户端，凭据仅用于单次创建请求。
package manxue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultBaseURL   = "https://manxue.ai"
	MaxResponseBytes = 4 << 20
	MaxHTMLBytes     = 2 << 20
	defaultTimeout   = 45 * time.Second
)

var (
	ErrInvalidRequest   = errors.New("manxue: invalid request")
	ErrInvalidResponse  = errors.New("manxue: invalid response")
	ErrResponseTooLarge = errors.New("manxue: response exceeds size limit")
	ErrRedirect         = errors.New("manxue: redirect refused")
	ErrTransport        = errors.New("manxue: transport failed")
	idPattern           = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
	idempotencyPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

// Client 仅请求固定检测服务；不会重试已发出的创建请求或跟随重定向。
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// CreateRequest 中的 APIKey 不得写入日志；String 方法提供脱敏调试描述。
type CreateRequest struct {
	Benchmark       string `json:"benchmark"`
	BaseURL         string `json:"base_url"`
	APIKey          string `json:"api_key"`
	Model           string `json:"model"`
	Protocol        string `json:"protocol,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	ServiceTier     string `json:"service_tier,omitempty"`
}

func (CreateRequest) String() string     { return "manxue.CreateRequest{credentials:redacted}" }
func (r CreateRequest) GoString() string { return r.String() }

// TestResult 只保留已知字段；远端任务 ID 是查询凭证，不得返回到用户页面。
type TestResult struct {
	ID              string         `json:"id"`
	Benchmark       string         `json:"benchmark"`
	Status          string         `json:"status"`
	Phase           string         `json:"phase"`
	Model           string         `json:"model,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	ServiceTier     string         `json:"service_tier,omitempty"`
	CreatedAt       string         `json:"created_at,omitempty"`
	ExpiresAt       string         `json:"expires_at,omitempty"`
	UsageReported   *bool          `json:"usage_reported,omitempty"`
	Candy           *CandyResult   `json:"candy,omitempty"`
	Result          *DrawingResult `json:"result,omitempty"`
	Assessment      *Assessment    `json:"assessment,omitempty"`
	Error           *RemoteError   `json:"error,omitempty"`
}

func (TestResult) String() string     { return "manxue.TestResult{task_and_content:redacted}" }
func (r TestResult) GoString() string { return r.String() }

// CandyResult 的题目和统计均允许缺省，避免把未报告的用量解释为零。
type CandyResult struct {
	Status          string       `json:"status"`
	Question        string       `json:"question,omitempty"`
	Answer          string       `json:"answer,omitempty"`
	StartedAt       string       `json:"started_at,omitempty"`
	FinishedAt      string       `json:"finished_at,omitempty"`
	DurationMS      *int64       `json:"duration_ms,omitempty"`
	InputTokens     *int64       `json:"input_tokens,omitempty"`
	OutputTokens    *int64       `json:"output_tokens,omitempty"`
	ReasoningTokens *int64       `json:"reasoning_tokens,omitempty"`
	Error           *RemoteError `json:"error,omitempty"`
}

// DrawingResult 的 HTML 是不可信内容，展示端必须使用隔离的沙箱。
type DrawingResult struct {
	HTML            string `json:"html,omitempty"`
	HasHTML         *bool  `json:"has_html,omitempty"`
	Title           string `json:"title,omitempty"`
	Prompt          string `json:"prompt,omitempty"`
	StartedAt       string `json:"started_at,omitempty"`
	FinishedAt      string `json:"finished_at,omitempty"`
	DurationMS      *int64 `json:"duration_ms,omitempty"`
	InputTokens     *int64 `json:"input_tokens,omitempty"`
	OutputTokens    *int64 `json:"output_tokens,omitempty"`
	ReasoningTokens *int64 `json:"reasoning_tokens,omitempty"`
}

// Assessment 保留服务的质量判断；未知质量不会被视为通过。
type Assessment struct {
	Quality string `json:"quality"`
	Label   string `json:"label,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Source  string `json:"source,omitempty"`
}

// RemoteError 不保留远端任意报错正文，防止原始请求或凭据被带入日志。
type RemoteError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *RemoteError) UnmarshalJSON(data []byte) error {
	if !json.Valid(data) {
		return ErrInvalidResponse
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) || bytes.Equal(bytes.TrimSpace(data), []byte(`""`)) {
		return nil
	}
	e.Code = "remote_error"
	e.Message = "检测服务报告执行失败"
	return nil
}

// HTTPError 只携带安全状态码与重试等待时间，不包含 URL、响应正文或任务 ID。
type HTTPError struct {
	StatusCode int
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string { return fmt.Sprintf("manxue: HTTP %d", e.StatusCode) }

// TransportError 对原始网络错误脱敏，同时保留超时与取消的可判定性。
type TransportError struct {
	Timeout  bool
	Canceled bool
}

func (e *TransportError) Error() string {
	if e.Canceled {
		return "manxue: request cancelled"
	}
	if e.Timeout {
		return "manxue: request timed out"
	}
	return ErrTransport.Error()
}

func (e *TransportError) Unwrap() error {
	if e.Canceled {
		return context.Canceled
	}
	if e.Timeout {
		return context.DeadlineExceeded
	}
	return ErrTransport
}

// NewClient 的生产服务地址固定，不接受管理员把检测请求指向其他站点。
func NewClient(httpClient *http.Client) *Client {
	return newClient(httpClient, DefaultBaseURL)
}

// NewClientWithBaseURL 仅用于测试注入；只允许本机 HTTP 或不含凭据的 HTTPS 地址。
func NewClientWithBaseURL(httpClient *http.Client, baseURL string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, ErrInvalidRequest
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback())) {
		return nil, ErrInvalidRequest
	}
	return newClient(httpClient, strings.TrimRight(baseURL, "/")), nil
}

func newClient(source *http.Client, baseURL string) *Client {
	var cloned http.Client
	if source != nil {
		cloned = *source
	}
	if cloned.Timeout <= 0 {
		cloned.Timeout = defaultTimeout
	}
	// 不使用调用方 Cookie jar，且禁止所有重定向，包括同源重定向。
	cloned.Jar = nil
	cloned.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{httpClient: &cloned, baseURL: baseURL}
}

// Create 仅提交一次；幂等键由业务层生成并持久化，客户端不执行自动重试。
func (c *Client) Create(ctx context.Context, input CreateRequest, idempotencyKey string) (*TestResult, error) {
	if input.Protocol == "" {
		input.Protocol = "responses"
	}
	if err := validateCreate(input, idempotencyKey); err != nil {
		return nil, err
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/tests", bytes.NewReader(body))
	if err != nil {
		return nil, ErrInvalidRequest
	}
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	// 禁止 Transport 在连接中断后利用可回放请求体再次发送带密钥的 POST。
	req.GetBody = nil
	result, err := c.do(req, http.StatusAccepted)
	if err != nil {
		return nil, err
	}
	if !ValidTaskID(result.ID) || (result.Benchmark != "" && result.Benchmark != input.Benchmark) {
		return nil, ErrInvalidResponse
	}
	if result.Benchmark == "" {
		result.Benchmark = input.Benchmark
	}
	return result, nil
}

// Get 校验任务 ID，拒绝路径、查询串及响应中的任务身份冲突。
func (c *Client) Get(ctx context.Context, id string) (*TestResult, error) {
	if !ValidTaskID(id) {
		return nil, ErrInvalidRequest
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/tests/"+id, nil)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	result, err := c.do(req, http.StatusOK)
	if err != nil {
		return nil, err
	}
	if result.ID != "" && result.ID != id {
		return nil, ErrInvalidResponse
	}
	result.ID = id
	return result, nil
}

// ValidTaskID 同时限制长度与字符集，远端任务 ID 不允许改变查询路径。
func ValidTaskID(id string) bool { return idPattern.MatchString(id) }

func validateCreate(input CreateRequest, key string) error {
	if input.Benchmark != "candy" && input.Benchmark != "pelican" {
		return ErrInvalidRequest
	}
	if input.Protocol != "responses" && input.Protocol != "chat_completions" {
		return ErrInvalidRequest
	}
	if input.Benchmark == "candy" && input.Protocol != "responses" {
		return ErrInvalidRequest
	}
	if key != "" && !idempotencyPattern.MatchString(key) {
		return ErrInvalidRequest
	}
	if strings.TrimSpace(input.APIKey) == "" || len(input.APIKey) > 8192 || strings.TrimSpace(input.Model) == "" || len(input.Model) > 256 {
		return ErrInvalidRequest
	}
	u, err := url.Parse(input.BaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(input.BaseURL) > 2048 {
		return ErrInvalidRequest
	}
	switch input.ReasoningEffort {
	case "", "low", "medium", "high":
	case "xhigh", "max", "ultra":
		if input.Benchmark != "candy" {
			return ErrInvalidRequest
		}
	default:
		return ErrInvalidRequest
	}
	if input.ServiceTier != "" && input.ServiceTier != "priority" && input.ServiceTier != "ultrafast" {
		return ErrInvalidRequest
	}
	return nil
}

func (c *Client) do(req *http.Request, expectedStatus int) (*TestResult, error) {
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, safeTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, ErrRedirect
	}
	if resp.StatusCode != expectedStatus {
		return nil, &HTTPError{StatusCode: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, safeTransportError(err)
	}
	if len(body) > MaxResponseBytes {
		return nil, ErrResponseTooLarge
	}
	var result TestResult
	if err := json.Unmarshal(body, &result); err != nil || result.Status == "" {
		return nil, ErrInvalidResponse
	}
	if result.Result != nil && len(result.Result.HTML) > MaxHTMLBytes {
		return nil, ErrResponseTooLarge
	}
	return &result, nil
}

// 读取响应体时同样可能超时或被取消，保留类型但丢弃可能携带凭据的原始文本。
func safeTransportError(err error) *TransportError {
	var timeout net.Error
	return &TransportError{Canceled: errors.Is(err, context.Canceled), Timeout: errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout())}
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > 86400 {
			seconds = 86400
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil && when.After(now) {
		delay := when.Sub(now)
		if delay > 24*time.Hour {
			return 24 * time.Hour
		}
		return delay
	}
	return 0
}

// Terminal 不把未知状态误认为成功或完成，调用方仍应实施独立超时上限。
func (r *TestResult) Terminal() bool {
	return r != nil && (r.Status == "succeeded" || r.Status == "failed" || r.Status == "cancelled")
}

// Outcome 将两类检测结果映射为统一展示状态，执行异常与判断未通过分开。
func (r *TestResult) Outcome() string {
	if r == nil {
		return "unknown"
	}
	if r.Status == "running" || r.Status == "queued" || r.Status == "pending" {
		return "pending"
	}
	if r.Status == "failed" || r.Status == "cancelled" {
		return "error"
	}
	if r.Status != "succeeded" {
		return "unknown"
	}
	if r.Error != nil && r.Error.Code != "" {
		return "error"
	}
	switch r.Benchmark {
	case "candy":
		if r.Candy == nil {
			return "unknown"
		}
		if r.Candy.Error != nil && r.Candy.Error.Code != "" {
			return "error"
		}
		switch r.Candy.Status {
		case "passed":
			return "passed"
		case "incorrect":
			return "failed"
		case "error":
			return "error"
		}
	case "pelican":
		if r.Assessment == nil {
			return "unknown"
		}
		switch r.Assessment.Quality {
		case "normal":
			return "passed"
		case "degraded", "suspicious":
			return "failed"
		}
	}
	return "unknown"
}
