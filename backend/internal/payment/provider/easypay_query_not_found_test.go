package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// 只有成功 HTTP 中明确的失败码及固定不存在文案可以产生脱敏标记错误。
func TestEasyPayQueryOrderNotFoundClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		statusCode   int
		body         string
		wantNotFound bool
	}{
		{"not found", 200, `{"code":0,"msg":"not found"}`, true},
		{"order not found", 200, `{"code":0,"msg":"order not found"}`, true},
		{"order does not exist", 200, `{"code":0,"msg":"order does not exist"}`, true},
		{"order not exist", 200, `{"code":-1,"msg":"order not exist"}`, true},
		{"Chinese order", 200, `{"code":0,"msg":"订单不存在"}`, true},
		{"Chinese order identifier", 200, `{"code":0,"msg":"订单编号不存在"}`, true},
		{"Chinese order number", 200, `{"code":0,"msg":"订单号不存在"}`, true},
		{"case whitespace punctuation", 200, `{"code":0,"msg":"  ORDER NOT FOUND.  "}`, true},
		{"Chinese punctuation", 200, `{"code":0,"msg":"订单不存在！"}`, true},
		{"extra raw data is redacted", 200, `{"code":0,"msg":"not found","key":"secret-response","out_trade_no":"sensitive-order"}`, true},
		{"HTTP not found", 404, `{"code":0,"msg":"order not found"}`, false},
		{"HTTP auth failure", 403, `{"code":0,"msg":"order not found"}`, false},
		{"HTTP server failure", 502, `{"code":0,"msg":"order not found"}`, false},
		{"missing code", 200, `{"msg":"order not found"}`, false},
		{"null code", 200, `{"code":null,"msg":"order not found"}`, false},
		{"string code", 200, `{"code":"0","msg":"order not found"}`, false},
		{"fractional code", 200, `{"code":0.5,"msg":"order not found"}`, false},
		{"boolean code", 200, `{"code":false,"msg":"order not found"}`, false},
		{"success code without state", 200, `{"code":1,"msg":"order not found"}`, false},
		{"missing message", 200, `{"code":0}`, false},
		{"null message", 200, `{"code":0,"msg":null}`, false},
		{"auth failure", 200, `{"code":0,"msg":"invalid key secret-response"}`, false},
		{"missing merchant", 200, `{"code":0,"msg":"merchant not found"}`, false},
		{"missing route", 200, `{"code":0,"msg":"route not found"}`, false},
		{"auth suffix", 200, `{"code":0,"msg":"order not found: permission denied"}`, false},
		{"auth prefix", 200, `{"code":0,"msg":"permission denied: order not found"}`, false},
		{"unknown suffix", 200, `{"code":0,"msg":"order not found secret-response"}`, false},
		{"Chinese auth suffix", 200, `{"code":0,"msg":"订单不存在或商户权限不足"}`, false},
		{"HTML", 200, `<html>order not found secret-response</html>`, false},
		{"malformed JSON", 200, `{"code":0,"msg":"not found"`, false},
		{"JSON array", 200, `[{"code":0,"msg":"not found"}]`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := parseEasyPayQueryResponse(tt.statusCode, []byte(tt.body), "sensitive-order", nil)
			if err == nil || resp != nil {
				t.Fatalf("response = %+v, error = %v; failure must not become pending", resp, err)
			}
			if got := errors.Is(err, ErrEasyPayOrderNotFound); got != tt.wantNotFound {
				t.Errorf("order not found = %v, want %v; error = %v", got, tt.wantNotFound, err)
			}
			for _, secret := range []string{"secret-response", "sensitive-order", "permission denied", "商户权限不足"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("query error exposes upstream data: %s", secret)
				}
			}
		})
	}
}

// 不存在标记只供调用方辨认失败语义；GET 和 POST 均不返回未付款、不追加兼容请求。
func TestEasyPayQueryOrderNotFoundDoesNotBecomePendingOrRetry(t *testing.T) {
	t.Parallel()
	for _, apiBase := range []string{"https://zpayz.cn", "https://payment.example"} {
		t.Run(apiBase, func(t *testing.T) {
			provider := newTestEasyPay(t, apiBase)
			calls := 0
			provider.httpClient.Transport = easyPayQueryRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"status":0,"msg":"not found"}`)), Request: r}, nil
			})
			resp, err := provider.QueryOrder(context.Background(), "missing-order")
			if resp != nil || !errors.Is(err, ErrEasyPayOrderNotFound) || calls != 1 {
				t.Fatalf("response = %+v, error = %v, calls = %d; want one failed request with the order-not-found marker", resp, err, calls)
			}
		})
	}
}
