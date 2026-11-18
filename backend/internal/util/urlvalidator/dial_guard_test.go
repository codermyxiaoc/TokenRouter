package urlvalidator

import (
	"errors"
	"net"
	"syscall"
	"testing"
)

// 拨号钩子按实际连接 IP 判定：回环、私网、链路本地、未指定地址拒绝，公网放行。
func TestPublicOnlyDialControl(t *testing.T) {
	blocked := []string{"127.0.0.1:80", "[::1]:443", "10.0.0.5:80", "192.168.1.2:8080", "172.16.0.1:80", "169.254.169.254:80", "0.0.0.0:80", "[fe80::1]:80"}
	for _, address := range blocked {
		if err := PublicOnlyDialControl("tcp", address, nil); err == nil {
			t.Fatalf("expected %s to be blocked", address)
		}
	}
	for _, address := range []string{"8.8.8.8:443", "[2001:4860:4860::8888]:443"} {
		if err := PublicOnlyDialControl("tcp", address, nil); err != nil {
			t.Fatalf("expected %s to be allowed: %v", address, err)
		}
	}
	if err := PublicOnlyDialControl("tcp", "not-an-address", nil); err == nil {
		t.Fatalf("expected malformed address to fail closed")
	}
}

// 组合钩子依次执行并在首个错误处中止，nil 钩子被跳过。
func TestChainDialControl(t *testing.T) {
	if ChainDialControl(nil, nil) != nil {
		t.Fatalf("all-nil chain should be nil")
	}
	calls := 0
	ok := func(string, string, syscall.RawConn) error { calls++; return nil }
	fail := func(string, string, syscall.RawConn) error { calls++; return errors.New("stop") }
	if err := ChainDialControl(ok, nil, fail, ok)("tcp", "8.8.8.8:1", nil); err == nil || calls != 2 {
		t.Fatalf("chain should stop at first error, calls=%d err=%v", calls, err)
	}
	if !IsDisallowedIP(nil) || IsDisallowedIP(net.ParseIP("1.1.1.1")) {
		t.Fatalf("unexpected IsDisallowedIP result")
	}
}
