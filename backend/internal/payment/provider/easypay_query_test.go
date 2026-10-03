package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/payment"
)

func TestEasyPayQueryOrderStatusMapping(t *testing.T) {
	t.Parallel()

	const orderID = "order-123"
	tests := []struct {
		name        string
		body        string
		wantStatus  string
		wantTradeNo string
		wantAmount  float64
	}{
		{
			name:        "top level trade success is paid",
			body:        `{"code":1,"trade_status":"TRADE_SUCCESS","status":0,"money":"12.34","trade_no":"gateway-123"}`,
			wantStatus:  payment.ProviderStatusPaid,
			wantTradeNo: "gateway-123",
			wantAmount:  12.34,
		},
		{
			name:        "waiting trade status with paid numeric status stays pending",
			body:        `{"code":1,"trade_status":"WAITING","status":1,"money":"12.34","trade_no":"gateway-123"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: "gateway-123",
			wantAmount:  12.34,
		},
		{
			name:        "waiting buyer payment is pending",
			body:        `{"code":1,"trade_status":"WAIT_BUYER_PAY","money":"12.34"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
			wantAmount:  12.34,
		},
		{
			name:        "nested data trade success is paid",
			body:        `{"code":1,"data":{"trade_status":"TRADE_SUCCESS","status":0,"money":"9.99","trade_no":"data-456"}}`,
			wantStatus:  payment.ProviderStatusPaid,
			wantTradeNo: "data-456",
			wantAmount:  9.99,
		},
		{
			name:        "legacy numeric paid status remains compatible",
			body:        `{"code":1,"status":1,"money":"3.21"}`,
			wantStatus:  payment.ProviderStatusPaid,
			wantTradeNo: orderID,
			wantAmount:  3.21,
		},
		{
			name:        "legacy numeric non paid status is pending",
			body:        `{"code":1,"status":0,"money":"3.21"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
			wantAmount:  3.21,
		},
		{
			name:        "nested numeric paid status remains compatible",
			body:        `{"code":1,"data":{"status":1,"money":"3.21"}}`,
			wantStatus:  payment.ProviderStatusPaid,
			wantTradeNo: orderID,
			wantAmount:  3.21,
		},
		{
			name:        "nested numeric unpaid status remains compatible",
			body:        `{"code":1,"data":{"status":0}}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
		},
		{
			name:        "nested waiting trade status remains compatible",
			body:        `{"code":1,"data":{"trade_status":"WAITING"}}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotForm url.Values
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("method = %q, want %q", r.Method, http.MethodPost)
				}
				if r.URL.Path != "/api.php" {
					t.Errorf("path = %q, want /api.php", r.URL.Path)
				}
				if err := r.ParseForm(); err != nil {
					t.Errorf("ParseForm: %v", err)
				}
				gotForm = make(url.Values, len(r.PostForm))
				for key, values := range r.PostForm {
					gotForm[key] = append([]string(nil), values...)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			provider := newTestEasyPay(t, server.URL)
			resp, err := provider.QueryOrder(context.Background(), orderID)
			if err != nil {
				t.Fatalf("QueryOrder returned error: %v", err)
			}
			if resp.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (response=%+v)", resp.Status, tt.wantStatus, resp)
			}
			if resp.TradeNo != tt.wantTradeNo {
				t.Fatalf("trade_no = %q, want %q", resp.TradeNo, tt.wantTradeNo)
			}
			if resp.Amount != tt.wantAmount {
				t.Fatalf("amount = %v, want %v", resp.Amount, tt.wantAmount)
			}
			for key, want := range map[string]string{
				"act":          "order",
				"pid":          "pid-1",
				"key":          "pkey-1",
				"out_trade_no": orderID,
			} {
				if got := gotForm.Get(key); got != want {
					t.Fatalf("form[%s] = %q, want %q (form=%v)", key, got, want, gotForm)
				}
			}
		})
	}
}

func TestEasyPayQueryOrderRejectsUnsafeResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    string
	}{
		{
			name:       "non success status",
			statusCode: http.StatusBadGateway,
			body:       `{"code":0,"msg":"gateway error"}`,
			wantErr:    "easypay query HTTP 502",
		},
		{
			name:       "html response",
			statusCode: http.StatusOK,
			body:       "<html>secret-response</html>",
			wantErr:    "easypay query non-JSON response (HTTP 200)",
		},
		{
			name:       "plain response",
			statusCode: http.StatusOK,
			body:       "gateway unavailable",
			wantErr:    "easypay query non-JSON response (HTTP 200)",
		},
		{
			name:       "empty response",
			statusCode: http.StatusOK,
			body:       "",
			wantErr:    "easypay query empty response (HTTP 200)",
		},
		{
			name:       "business failure with status",
			statusCode: http.StatusOK,
			body:       `{"code":0,"status":0,"msg":"secret-response"}`,
			wantErr:    "easypay query unsuccessful response",
		},
		{
			name:       "missing result code",
			statusCode: http.StatusOK,
			body:       `{"status":0}`,
			wantErr:    "easypay query unsuccessful response",
		},
		{
			name:       "null result code",
			statusCode: http.StatusOK,
			body:       `{"code":null,"status":0}`,
			wantErr:    "easypay query unsuccessful response",
		},
		{
			name:       "missing payment status",
			statusCode: http.StatusOK,
			body:       `{"code":1}`,
			wantErr:    "easypay query missing payment status",
		},
		{
			name:       "unknown numeric payment status",
			statusCode: http.StatusOK,
			body:       `{"code":1,"status":2}`,
			wantErr:    "easypay query unknown payment status",
		},
		{
			name:       "unknown nested numeric payment status",
			statusCode: http.StatusOK,
			body:       `{"code":1,"data":{"status":-1}}`,
			wantErr:    "easypay query unknown payment status",
		},
		{
			name:       "unknown trade status cannot fall back to numeric status",
			statusCode: http.StatusOK,
			body:       `{"code":1,"trade_status":"secret-response","status":0}`,
			wantErr:    "easypay query unknown payment status",
		},
		{
			name:       "empty trade status cannot fall back to numeric status",
			statusCode: http.StatusOK,
			body:       `{"code":1,"trade_status":"","status":1}`,
			wantErr:    "easypay query unknown payment status",
		},
		{
			name:       "unknown nested trade status cannot fall back to numeric status",
			statusCode: http.StatusOK,
			body:       `{"code":1,"status":0,"data":{"trade_status":"UNKNOWN"}}`,
			wantErr:    "easypay query unknown payment status",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			provider := newTestEasyPay(t, server.URL)
			_, err := provider.QueryOrder(context.Background(), "order-unsafe")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("QueryOrder error = %v, want containing %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "secret-response") {
				t.Fatalf("QueryOrder error leaked response body: %v", err)
			}
		})
	}
}

// 验证只对成功空白响应回退，且 GET 使用完整查询参数，不重试业务错误。
func TestEasyPayQueryOrderGETFallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		postCode   int
		postBody   string
		getCode    int
		getBody    string
		wantGET    int32
		wantStatus string
		wantError  bool
	}{
		{"post success", 200, `{"code":1,"status":0}`, 0, "", 0, payment.ProviderStatusPending, false},
		{"empty post then unpaid", 200, "", 200, `{"code":1,"status":0,"money":"10.00"}`, 1, payment.ProviderStatusPending, false},
		{"blank post then paid", 200, " \r\n\t", 200, `{"code":1,"data":{"status":1,"money":"10.00"}}`, 1, payment.ProviderStatusPaid, false},
		{"no content post then unpaid", 204, "", 200, `{"code":1,"status":0}`, 1, payment.ProviderStatusPending, false},
		{"post business error", 200, `{"code":0,"msg":"订单不存在"}`, 0, "", 0, "", true},
		{"post html", 200, "<html>error</html>", 0, "", 0, "", true},
		{"post unknown state", 200, `{"code":1,"status":2}`, 0, "", 0, "", true},
		{"post server error", 502, "", 0, "", 0, "", true},
		{"get empty no further retry", 200, "", 200, "", 1, "", true},
		{"get business error", 200, "", 200, `{"code":0,"status":0}`, 1, "", true},
		{"get missing status", 200, "", 200, `{"code":1}`, 1, "", true},
		{"get unknown status", 200, "", 200, `{"code":1,"status":9}`, 1, "", true},
		{"get html", 200, "", 200, "<html>error</html>", 1, "", true},
		{"get server error", 200, "", 502, "", 1, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var postCalls, getCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/epay/api.php" {
					t.Errorf("unexpected query path: %s", r.URL.Path)
				}
				var params url.Values
				switch r.Method {
				case http.MethodPost:
					postCalls.Add(1)
					if err := r.ParseForm(); err != nil {
						t.Errorf("parse form: %v", err)
					}
					params = r.PostForm
					if r.URL.RawQuery != "" {
						t.Error("POST unexpectedly exposes credentials in query")
					}
					w.WriteHeader(tt.postCode)
					_, _ = io.WriteString(w, tt.postBody)
				case http.MethodGet:
					getCalls.Add(1)
					params = r.URL.Query()
					body, _ := io.ReadAll(r.Body)
					if len(body) != 0 {
						t.Error("GET unexpectedly contains request body")
					}
					if tt.getCode == 0 {
						t.Error("unexpected GET fallback")
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					w.WriteHeader(tt.getCode)
					_, _ = io.WriteString(w, tt.getBody)
				default:
					t.Errorf("unexpected method: %s", r.Method)
					return
				}
				for key, want := range map[string]string{"act": "order", "pid": "pid-1", "key": "pkey-1", "out_trade_no": "order&+?中文"} {
					if params.Get(key) != want {
						t.Errorf("incorrect %s parameter", key)
					}
				}
			}))
			defer server.Close()
			provider := newTestEasyPay(t, server.URL+"/epay/api.php")
			resp, err := provider.QueryOrder(context.Background(), "order&+?中文")
			if (err != nil) != tt.wantError {
				t.Fatalf("QueryOrder error = %v, wantError = %v", err, tt.wantError)
			}
			if !tt.wantError && resp.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", resp.Status, tt.wantStatus)
			}
			if postCalls.Load() != 1 || getCalls.Load() != tt.wantGET {
				t.Errorf("request counts POST=%d GET=%d, want 1/%d", postCalls.Load(), getCalls.Load(), tt.wantGET)
			}
		})
	}
}

type easyPayQueryRoundTripper func(*http.Request) (*http.Response, error)

func (f easyPayQueryRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// 自定义传输错误可能重复包含 URL，不能仅剥离 url.Error 的最外层。
func TestEasyPayQueryOrderRedactsTransportErrors(t *testing.T) {
	t.Parallel()
	for _, failMethod := range []string{http.MethodPost, http.MethodGet} {
		t.Run(failMethod, func(t *testing.T) {
			t.Parallel()
			provider := newTestEasyPay(t, "https://payment.example")
			calls := 0
			provider.httpClient.Transport = easyPayQueryRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method == failMethod {
					return nil, &url.Error{Op: "test", URL: r.URL.String(), Err: fmt.Errorf("request %s with pkey-1 failed", r.URL)}
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
			})
			_, err := provider.QueryOrder(context.Background(), "sensitive-order")
			if err == nil {
				t.Fatal("expected transport error")
			}
			for _, secret := range []string{"pkey-1", "pid-1", "sensitive-order", "https://", "key="} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("transport error exposes sensitive data: %s", secret)
				}
			}
			wantCalls := 1
			if failMethod == http.MethodGet {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Errorf("calls = %d, want %d", calls, wantCalls)
			}
		})
	}
}

// 查单不能跟随同站、跨站或 HTTPS 降级跳转，且不修改原客户端的策略。
func TestEasyPayQueryOrderRejectsRedirects(t *testing.T) {
	t.Parallel()
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		for _, target := range []string{"https://payment.example/next", "https://other.example/next", "http://payment.example/next"} {
			t.Run(method+"/"+target, func(t *testing.T) {
				t.Parallel()
				provider := newTestEasyPay(t, "https://payment.example")
				calls := 0
				originalRedirectCalls := 0
				provider.httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
					originalRedirectCalls++
					return nil
				}
				provider.httpClient.Transport = easyPayQueryRoundTripper(func(r *http.Request) (*http.Response, error) {
					calls++
					if strings.HasSuffix(r.URL.Path, "/next") {
						t.Error("query followed forbidden redirect")
						return nil, errors.New("redirected")
					}
					code := http.StatusOK
					header := http.Header{}
					if r.Method == method {
						code = http.StatusTemporaryRedirect
						header.Set("Location", target+"?key=pkey-1")
					}
					return &http.Response{StatusCode: code, Header: header, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				})
				_, err := provider.QueryOrder(context.Background(), "order-redirect")
				if err == nil || !strings.Contains(err.Error(), "HTTP 307") {
					t.Fatalf("error = %v, want HTTP 307", err)
				}
				wantCalls := 1
				if method == http.MethodGet {
					wantCalls = 2
				}
				if calls != wantCalls || originalRedirectCalls != 0 {
					t.Errorf("requests=%d callbacks=%d, want %d/0", calls, originalRedirectCalls, wantCalls)
				}
				if provider.httpClient.CheckRedirect == nil || provider.httpClient.Timeout != easypayHTTPTimeout {
					t.Error("query changed the shared client")
				}
			})
		}
	}
}

// 两种方法使用同一截止时间，外部取消也必须保留可识别的 context 错误。
func TestEasyPayQueryOrderSharesDeadline(t *testing.T) {
	t.Parallel()
	provider := newTestEasyPay(t, "https://payment.example")
	provider.httpClient.Timeout = 100 * time.Millisecond
	var deadline time.Time
	calls := 0
	provider.httpClient.Transport = easyPayQueryRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		got, ok := r.Context().Deadline()
		if !ok {
			t.Fatal("query has no deadline")
		}
		if r.Method == http.MethodPost {
			deadline = got
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		if !got.Equal(deadline) {
			t.Error("GET received a renewed timeout budget")
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	_, err := provider.QueryOrder(context.Background(), "order-timeout")
	if !errors.Is(err, context.DeadlineExceeded) || calls != 2 {
		t.Fatalf("error = %v, calls = %d; want shared deadline after two requests", err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = provider.QueryOrder(ctx, "order-cancelled")
	if !errors.Is(err, context.Canceled) || calls != 2 {
		t.Fatalf("error = %v, calls = %d; cancelled context must not issue requests", err, calls)
	}
}
