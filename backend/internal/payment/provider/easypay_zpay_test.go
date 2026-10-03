package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/payment"
)

// 官方域名使用 GET，主机名以外的同名文本及未经核实的域名均不能触发兼容分支。
func TestEasyPayZPayQueryMethod(t *testing.T) {
	t.Parallel()
	tests := []struct {
		apiBase string
		method  string
		path    string
	}{
		{"https://zpayz.cn", http.MethodGet, "/api.php"},
		{"https://www.zpayz.cn/api.php", http.MethodGet, "/api.php"},
		{"https://ZPAYZ.CN:443", http.MethodGet, "/api.php"},
		{"https://WWW.ZPAYZ.CN/epay/mapi.php", http.MethodGet, "/epay/api.php"},
		{"https://payment.example", http.MethodPost, "/api.php"},
		{"https://zpayz.cn.example", http.MethodPost, "/api.php"},
		{"https://fakezpayz.cn", http.MethodPost, "/api.php"},
		{"https://api.zpayz.cn", http.MethodPost, "/api.php"},
		{"https://zpay.cn", http.MethodPost, "/api.php"},
		{"https://zpayz.cn@payment.example", http.MethodPost, "/api.php"},
		{"https://payment.example/zpayz.cn", http.MethodPost, "/zpayz.cn/api.php"},
		{"https://payment.example?host=zpayz.cn", http.MethodPost, "/api.php"},
	}
	for _, tt := range tests {
		t.Run(tt.apiBase, func(t *testing.T) {
			t.Parallel()
			provider := newTestEasyPay(t, tt.apiBase)
			provider.config["pid"] = "pid&+?中文"
			provider.config["pkey"] = "key&+?中文"
			const orderID = "order&+?中文"
			calls := 0
			provider.httpClient.Transport = easyPayQueryRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != tt.method || r.URL.Path != tt.path {
					t.Errorf("request method/path = %s %s, want %s %s", r.Method, r.URL.Path, tt.method, tt.path)
				}
				params := r.URL.Query()
				if r.Method == http.MethodPost {
					if r.URL.RawQuery != "" {
						t.Error("POST exposes parameters in query")
					}
					if err := r.ParseForm(); err != nil {
						t.Errorf("parse form: %v", err)
					}
					params = r.PostForm
				} else if r.Body != nil && r.Body != http.NoBody {
					t.Error("GET contains a request body")
				}
				if len(params) != 4 {
					t.Errorf("parameter count = %d, want 4", len(params))
				}
				for key, want := range map[string]string{"act": "order", "pid": provider.config["pid"], "key": provider.config["pkey"], "out_trade_no": orderID} {
					if values := params[key]; len(values) != 1 || values[0] != want {
						t.Errorf("incorrect %s parameter", key)
					}
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":1,"status":0}`)), Request: r}, nil
			})
			resp, err := provider.QueryOrder(context.Background(), orderID)
			if err != nil || resp == nil || resp.Status != payment.ProviderStatusPending || calls != 1 {
				t.Fatalf("response = %+v, error = %v, calls = %d; want pending after one request", resp, err, calls)
			}
		})
	}
}

// Z-Pay 首次 GET 必须明确返回成功和已知支付状态，所有异常都保留未知且不回退 POST。
func TestEasyPayZPayQueryResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantStatus string
	}{
		{"unpaid", 200, `{"code":1,"status":0,"money":"10.00","trade_no":"zpay-order"}`, payment.ProviderStatusPending},
		{"paid", 200, `{"code":1,"status":1,"money":"10.00","trade_no":"zpay-order"}`, payment.ProviderStatusPaid},
		{"empty", 200, "", ""},
		{"blank", 200, " \r\n\t", ""},
		{"no content", 204, "", ""},
		{"method not allowed", 405, "sensitive-response", ""},
		{"server error", 502, "sensitive-response", ""},
		{"html", 200, "<html>sensitive-response</html>", ""},
		{"malformed JSON", 200, `{"code":1,"status":`, ""},
		{"business error", 200, `{"code":0,"status":0,"msg":"sensitive-response"}`, ""},
		{"missing code", 200, `{"status":0}`, ""},
		{"string code", 200, `{"code":"1","status":0}`, ""},
		{"fractional code", 200, `{"code":1.5,"status":0}`, ""},
		{"missing status", 200, `{"code":1}`, ""},
		{"null status", 200, `{"code":1,"status":null}`, ""},
		{"unknown status", 200, `{"code":1,"status":2}`, ""},
		{"unknown trade status", 200, `{"code":1,"trade_status":"sensitive-response","status":0}`, ""},
		{"oversized", 200, strings.Repeat(" ", maxEasypayResponseSize+1), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := newTestEasyPay(t, "https://zpayz.cn")
			calls := 0
			provider.httpClient.Transport = easyPayQueryRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet {
					t.Errorf("unexpected query method: %s", r.Method)
				}
				return &http.Response{StatusCode: tt.statusCode, Body: io.NopCloser(strings.NewReader(tt.body)), Request: r}, nil
			})
			resp, err := provider.QueryOrder(context.Background(), "local-order")
			if calls != 1 {
				t.Errorf("query requests = %d, want 1", calls)
			}
			if tt.wantStatus == "" {
				if err == nil || resp != nil {
					t.Fatalf("response = %+v, error = %v; invalid response must remain unknown", resp, err)
				}
				for _, secret := range []string{"sensitive-response", "pkey-1", "pid-1", "local-order", "key="} {
					if strings.Contains(err.Error(), secret) {
						t.Errorf("query error exposes sensitive data: %s", secret)
					}
				}
				return
			}
			if err != nil || resp == nil || resp.Status != tt.wantStatus || resp.TradeNo != "zpay-order" || resp.Amount != 10 {
				t.Fatalf("response = %+v, error = %v; want %s, zpay-order, 10", resp, err, tt.wantStatus)
			}
		})
	}
}

// GET 的传输错误不能暴露 URL 或密钥，也不能用 POST 再请求一次。
func TestEasyPayZPayQueryRedactsTransportError(t *testing.T) {
	t.Parallel()
	provider := newTestEasyPay(t, "https://zpayz.cn")
	calls := 0
	provider.httpClient.Transport = easyPayQueryRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, &url.Error{Op: "test", URL: r.URL.String(), Err: fmt.Errorf("request %s with pkey-1 failed", r.URL)}
	})
	resp, err := provider.QueryOrder(context.Background(), "sensitive-order")
	if err == nil || resp != nil || calls != 1 {
		t.Fatalf("response = %+v, error = %v, calls = %d; want one failed request", resp, err, calls)
	}
	for _, secret := range []string{"pkey-1", "pid-1", "sensitive-order", "https://", "key="} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("transport error exposes sensitive data: %s", secret)
		}
	}
}

// 官方域名直接 GET 也禁止同站、跨站和降级跳转，避免查单凭据继续传播。
func TestEasyPayZPayQueryRejectsRedirects(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"https://zpayz.cn/next", "https://other.example/next", "http://zpayz.cn/next"} {
		for _, statusCode := range []int{301, 302, 303, 307, 308} {
			t.Run(fmt.Sprintf("%s/%d", target, statusCode), func(t *testing.T) {
				t.Parallel()
				provider := newTestEasyPay(t, "https://zpayz.cn")
				calls := 0
				provider.httpClient.Transport = easyPayQueryRoundTripper(func(r *http.Request) (*http.Response, error) {
					calls++
					if calls > 1 || r.Method != http.MethodGet {
						t.Error("query followed a redirect or changed request method")
						return nil, errors.New("unexpected request")
					}
					return &http.Response{StatusCode: statusCode, Header: http.Header{"Location": {target + "?key=pkey-1"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				})
				resp, err := provider.QueryOrder(context.Background(), "order-redirect")
				if err == nil || resp != nil || calls != 1 || !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", statusCode)) {
					t.Fatalf("response = %+v, error = %v, calls = %d; want one rejected redirect", resp, err, calls)
				}
				if strings.Contains(err.Error(), "pkey-1") {
					t.Error("redirect error exposes merchant key")
				}
			})
		}
	}
}

// 直接 GET 仍受总超时约束，已取消的上下文不发出请求。
func TestEasyPayZPayQueryDeadline(t *testing.T) {
	t.Parallel()
	provider := newTestEasyPay(t, "https://zpayz.cn")
	provider.httpClient.Timeout = 20 * time.Millisecond
	calls := 0
	provider.httpClient.Transport = easyPayQueryRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if _, ok := r.Context().Deadline(); !ok {
			t.Fatal("query has no deadline")
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	_, err := provider.QueryOrder(context.Background(), "order-timeout")
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("error = %v, calls = %d; want deadline after one GET", err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = provider.QueryOrder(ctx, "order-cancelled")
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("error = %v, calls = %d; cancelled context must not issue requests", err, calls)
	}
}

// 查单适配只影响 act=order；官方域名上的创建支付和退款仍使用 POST。
func TestEasyPayZPayCreateAndRefundRemainPOST(t *testing.T) {
	t.Parallel()
	provider := newTestEasyPay(t, "https://zpayz.cn")
	calls := 0
	provider.httpClient.Transport = easyPayQueryRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if r.PostForm.Get("out_trade_no") != "local-order" || r.PostForm.Get("pid") != "pid-1" {
			t.Error("missing order or merchant parameter in POST form")
		}
		body := `{"code":1}`
		switch r.URL.Path {
		case "/mapi.php":
			if r.URL.RawQuery != "" || r.PostForm.Get("sign") == "" {
				t.Error("creation parameters changed")
			}
			body = `{"code":1,"trade_no":"zpay-order","payurl":"https://zpayz.cn/pay/test"}`
		case "/api.php":
			if r.URL.RawQuery != "act=refund" || r.PostForm.Get("key") != "pkey-1" {
				t.Error("refund parameters changed")
			}
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	if _, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{OrderID: "local-order", Amount: "10.00", PaymentType: "alipay", Subject: "测试订单"}); err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if _, err := provider.Refund(context.Background(), payment.RefundRequest{OrderID: "local-order", Amount: "10.00"}); err != nil {
		t.Fatalf("Refund: %v", err)
	}
	if calls != 2 {
		t.Errorf("request count = %d, want 2", calls)
	}
}
