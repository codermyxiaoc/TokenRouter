package service

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/dns/dnsmessage"
)

// 子进程隔离标准库全局 DNS resolver，避免并发套件与故障注入互相污染。
func TestImageStability_DNSPrivateAnswerBlocked(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestImageStability_DNSHelper$", "-test.timeout=10s")
	cmd.Env = append(os.Environ(), "TOKENROUTER_IMAGE_DNS_STABILITY_CHILD=1")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}

func TestImageStability_DNSHelper(t *testing.T) {
	if os.Getenv("TOKENROUTER_IMAGE_DNS_STABILITY_CHILD") != "1" {
		t.Skip("仅在隔离 DNS 子进程运行")
	}
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); _, _ = w.Write(pngBytes) }))
	defer server.Close()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	require.NoError(t, err)
	dns, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer dns.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, address, err := dns.ReadFrom(buf)
			if err != nil {
				return
			}
			var query dnsmessage.Message
			if query.Unpack(buf[:n]) != nil {
				continue
			}
			response := dnsmessage.Message{Header: dnsmessage.Header{ID: query.Header.ID, Response: true, RecursionAvailable: true, RecursionDesired: query.Header.RecursionDesired}, Questions: query.Questions}
			for _, q := range query.Questions {
				if q.Type == dnsmessage.TypeA {
					response.Answers = append(response.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}, Body: &dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}}})
				}
			}
			wire, err := response.Pack()
			if err == nil {
				_, _ = dns.WriteTo(wire, address)
			}
		}
	}()
	original := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", dns.LocalAddr().String())
	}}
	defer func() { net.DefaultResolver = original; _ = dns.Close(); <-done }()
	storage := &fakeImageStorage{}
	uploader := NewImageResultUploader(storage, "", 0, newImageDownloadHTTPClient())
	body, _ := json.Marshal(map[string]any{"data": []map[string]string{{"url": "http://public-looking.invalid:" + port + "/image"}}})
	_, err = uploader.Rewrite(t.Context(), "dns-private", body)
	require.ErrorContains(t, err, "resolved address is not public")
	require.Zero(t, hits.Load(), "DNS 返回私网后不得向本机图片服务发起请求")
	require.Empty(t, storage.saved)
}
