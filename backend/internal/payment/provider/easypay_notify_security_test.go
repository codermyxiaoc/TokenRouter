// 验证下单签名重放与参数注入必须拒绝，正常通知仍通过。
package provider

import (
	"context"
	"net/url"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/payment"
)
func easyPayPoCProvider() *EasyPay {
	return &EasyPay{config: map[string]string{
		"pid":       "1000",
		"pkey":      "MERCHANT_SECRET_KEY",
		"apiBase":   "https://pay.example.com",
		"notifyUrl": "https://site.example.com/api/v1/payment/webhook/easypay",
		"returnUrl": "https://site.example.com/payment/result",
	}}
}
func TestEasyPayNotifyRejectsForgedSignReuseCallback(t *testing.T) {
	t.Parallel()
	e := easyPayPoCProvider()
	returnURL := "https://site.example.com/payment/result?order_id=99&out_trade_no=ORDER123&status=success&trade_status=TRADE_SUCCESS"
	createParams := map[string]string{
		"pid":          "1000",
		"type":         "alipay",
		"out_trade_no": "ORDER123",
		"notify_url":   e.config["notifyUrl"],
		"return_url":   returnURL,
		"name":         "balance recharge",
		"money":        "650.00",
	}
	sign := easyPaySign(createParams, e.config["pkey"])
	prefix := "https://site.example.com/payment/result?order_id=99&out_trade_no=ORDER123&status=success"
	cb := url.Values{}
	cb.Set("pid", "1000")
	cb.Set("type", "alipay")
	cb.Set("out_trade_no", "ORDER123")
	cb.Set("notify_url", e.config["notifyUrl"])
	cb.Set("name", "balance recharge")
	cb.Set("money", "650.00")
	cb.Set("return_url", prefix)
	rawCallback := cb.Encode() + "&trade_status=TRADE_SUCCESS" + "&sign=" + sign + "&sign_type=MD5"

	if _, err := e.VerifyNotification(context.Background(), rawCallback, nil); err == nil {
		t.Fatal("forged sign-reuse callback must be rejected")
	}
}
func TestEasyPayNotifyRejectsOrderURLReplay(t *testing.T) {
	t.Parallel()
	e := easyPayPoCProvider()

	createParams := map[string]string{
		"pid":          "1000",
		"type":         "alipay",
		"out_trade_no": "ORDER123",
		"notify_url":   e.config["notifyUrl"],
		"return_url":   "https://site.example.com/payment/result",
		"name":         "balance recharge",
		"money":        "650.00",
	}
	createParams["sign"] = easyPaySign(createParams, e.config["pkey"])
	createParams["sign_type"] = signTypeMD5
	q := url.Values{}
	for k, v := range createParams {
		q.Set(k, v)
	}

	if _, err := e.VerifyNotification(context.Background(), q.Encode(), nil); err == nil {
		t.Fatal("replayed order URL must be rejected")
	}
}
func TestEasyPayNotifyAcceptsGenuineCallback(t *testing.T) {
	t.Parallel()
	e := easyPayPoCProvider()

	params := map[string]string{
		"pid":          "1000",
		"trade_no":     "2026100622001400000001",
		"out_trade_no": "ORDER123",
		"type":         "alipay",
		"name":         "balance recharge",
		"money":        "650.00",
		"trade_status": tradeStatusSuccess,
	}
	sign := easyPaySign(params, e.config["pkey"])
	params["sign"] = sign
	params["sign_type"] = signTypeMD5
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}

	n, err := e.VerifyNotification(context.Background(), q.Encode(), nil)
	if err != nil {
		t.Fatalf("genuine callback rejected: %v", err)
	}
	if n.Status != payment.ProviderStatusSuccess {
		t.Fatalf("status = %v, want success", n.Status)
	}
	if n.OrderID != "ORDER123" || n.TradeNo != "2026100622001400000001" || n.Amount != 650.00 {
		t.Fatalf("unexpected notification: %+v", n)
	}
}
func TestEasyPayNotifyRejectsUnknownParam(t *testing.T) {
	t.Parallel()
	e := easyPayPoCProvider()

	params := map[string]string{
		"pid":          "1000",
		"trade_no":     "T1",
		"out_trade_no": "ORDER123",
		"type":         "alipay",
		"name":         "balance recharge",
		"money":        "650.00",
		"trade_status": tradeStatusSuccess,
	}
	sign := easyPaySign(params, e.config["pkey"])
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	q.Set("sign", sign)
	q.Set("sign_type", signTypeMD5)
	q.Set("device", "") // 空值未参与签名，也必须检查白名单

	if _, err := e.VerifyNotification(context.Background(), q.Encode(), nil); err == nil {
		t.Fatal("callback with unknown param must be rejected")
	}
}
