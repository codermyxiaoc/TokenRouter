// Package chanshui 提供掺水检测公共接口的受限客户端，不保存检测凭据或远端任意正文。
package chanshui

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

	"github.com/TokenFlux/TokenRouter/internal/util/urlvalidator"
)

const (
	DefaultBaseURL   = "https://chanshui.dev"
	MaxResponseBytes = 4 << 20
	defaultTimeout   = 45 * time.Second
)

var (
	ErrInvalidRequest   = errors.New("chanshui: invalid request")
	ErrInvalidResponse  = errors.New("chanshui: invalid response")
	ErrResponseTooLarge = errors.New("chanshui: response exceeds size limit")
	ErrRedirect         = errors.New("chanshui: redirect refused")
	ErrTransport        = errors.New("chanshui: transport failed")
	idPattern           = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)
)

// Client 固定连接官方公共接口，不跟随重定向，不重试创建请求。
type Client struct{ httpClient *http.Client }

// CreateRequest 的密钥仅用于发送 JSON；日志格式化始终隐藏全部字段。
type CreateRequest struct {
	BaseURL  string   `json:"base_url"`
	APIKey   string   `json:"api_key"`
	Model    string   `json:"model"`
	Protocol string   `json:"protocol,omitempty"`
	Timeout  int      `json:"timeout,omitempty"`
	Sections []string `json:"sections,omitempty"`
}

func (CreateRequest) String() string     { return "chanshui.CreateRequest{credentials:redacted}" }
func (r CreateRequest) GoString() string { return r.String() }

// HTTPError 不包含服务器错误正文、请求地址或密钥。
type HTTPError struct {
	StatusCode int
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string { return fmt.Sprintf("chanshui: HTTP %d", e.StatusCode) }

// TransportError 保留取消与超时类别，丢弃可能携带凭据的底层错误文本。
type TransportError struct {
	Timeout  bool
	Canceled bool
}

func (e *TransportError) Error() string {
	if e.Canceled {
		return "chanshui: request cancelled"
	}
	if e.Timeout {
		return "chanshui: request timed out"
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

// NewClient 只注入传输实现，不开放官方地址配置；测试也不需要真实检测请求。
func NewClient(source *http.Client) *Client {
	var client http.Client
	if source != nil {
		client = *source
	}
	if client.Timeout <= 0 {
		client.Timeout = defaultTimeout
	}
	client.Jar = nil
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{httpClient: &client}
}

// Create 仅发送一次；提交未知时由调用方保留未知状态，不能用自动重发猜测受理结果。
func (c *Client) Create(ctx context.Context, input CreateRequest) (*Result, error) {
	if err := ValidateCreate(input); err != nil {
		return nil, err
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, DefaultBaseURL+"/api/v1/audits", bytes.NewReader(body))
	if err != nil {
		return nil, ErrInvalidRequest
	}
	req.Header.Set("Content-Type", "application/json")
	// 禁止标准 Transport 在连接中断后利用请求体重放发送凭据的 POST。
	req.GetBody = nil
	return c.do(req, http.StatusAccepted, "")
}

// Get 只按合法 ID 组装固定路径，不读取或信任创建响应中的 poll_url。
func (c *Client) Get(ctx context.Context, id string) (*Result, error) {
	if !ValidTaskID(id) {
		return nil, ErrInvalidRequest
	}
	id = strings.ToLower(id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DefaultBaseURL+"/api/v1/audits/"+id, nil)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	return c.do(req, http.StatusOK, id)
}

// ValidTaskID 与官方 32 位十六进制报告编号契约一致，不接受路径或查询参数。
func ValidTaskID(id string) bool { return idPattern.MatchString(id) }

// ValidateCreate 校验公开协议；公网 DNS 与账号归属须由业务层在提交前再次核实。
func ValidateCreate(input CreateRequest) error {
	if strings.TrimSpace(input.APIKey) == "" || len(input.APIKey) > 8192 ||
		strings.TrimSpace(input.Model) == "" || len(input.Model) > 256 || strings.ContainsAny(input.Model, "\r\n\x00") {
		return ErrInvalidRequest
	}
	u, err := url.Parse(input.BaseURL)
	if err != nil || len(input.BaseURL) > 2048 || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return ErrInvalidRequest
	}
	// 官方检测支持 HTTP(S) 上游，保留账号所配置协议；公共目标限制与提交前的 DNS 校验仍然生效。
	if _, err := urlvalidator.ValidateHTTPURL(input.BaseURL, true, urlvalidator.ValidationOptions{}); err != nil {
		return ErrInvalidRequest
	}
	if input.Protocol != "" && !ValidProtocol(input.Protocol) {
		return ErrInvalidRequest
	}
	if input.Timeout != 0 && (input.Timeout < 10 || input.Timeout > 600) {
		return ErrInvalidRequest
	}
	if len(input.Sections) > 8 {
		return ErrInvalidRequest
	}
	seen := map[string]bool{}
	for _, section := range input.Sections {
		if !ValidSection(section) || seen[section] {
			return ErrInvalidRequest
		}
		seen[section] = true
	}
	// 官方只对 claude- 前缀开放思维链；混合板块由服务端跳过，单独检测提前拒绝。
	if len(input.Sections) == 1 && seen["thinking"] && !strings.HasPrefix(strings.ToLower(input.Model), "claude-") {
		return ErrInvalidRequest
	}
	return nil
}

func ValidProtocol(protocol string) bool {
	return protocol == "auto" || protocol == "chat" || protocol == "anthropic" || protocol == "openai"
}

func ValidSection(section string) bool {
	_, ok := sectionNames[section]
	return ok
}

func (c *Client) do(req *http.Request, expectedStatus int, expectedID string) (*Result, error) {
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
	var raw wireResult
	if err := json.Unmarshal(body, &raw); err != nil || !ValidTaskID(raw.ID) || raw.Status == "" {
		return nil, ErrInvalidResponse
	}
	raw.ID = strings.ToLower(raw.ID)
	if expectedID != "" && raw.ID != expectedID {
		return nil, ErrInvalidResponse
	}
	return normalizeResult(raw), nil
}

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
		if delay := when.Sub(now); delay < 24*time.Hour {
			return delay
		}
		return 24 * time.Hour
	}
	return 0
}
