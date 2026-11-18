package repository

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 图片 URL 回填等不可信下载带 PublicHostsOnly 标记：即使请求前的解析校验被 DNS Rebinding 绕过，
// 直连拨号阶段也必须拦截回环地址；同一传输上的普通上游请求不受影响。
func TestUpstreamDialGuardBlocksPublicOnlyLoopback(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	transport, err := buildUpstreamTransportWithGuard(defaultPoolSettings(nil), nil, upstreamProtocolModeDefault, false)
	require.NoError(t, err)
	client := &http.Client{Transport: transport}

	// 不可信下载：跳过请求前校验（模拟预检已被绕过），直接验证拨号阶段的拦截。
	req, err := http.NewRequestWithContext(service.WithHTTPUpstreamPublicHostsOnly(context.Background()), http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
	require.Contains(t, err.Error(), "dial guard")
	require.Zero(t, hits)

	// 普通上游请求（运营者配置的内网上游）保持可达。
	req, err = http.NewRequest(http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	resp, err = client.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, 1, hits)
}

// 全局开启 URL 白名单且禁止私网时，所有直连拨号都校验实际 IP。
func TestUpstreamDialGuardGlobalPublicOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = true
	svc := &httpUpstreamService{cfg: cfg}
	require.True(t, svc.shouldValidateResolvedIP())

	// 普通传输（明文与 TLS 都走此直连拨号器）。
	rt, err := buildUpstreamTransportWithGuard(defaultPoolSettings(cfg), nil, upstreamProtocolModeDefault, svc.shouldValidateResolvedIP())
	require.NoError(t, err)
	resp, err := (&http.Client{Transport: rt}).Get(server.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
	require.Contains(t, err.Error(), "dial guard")

	// TLS 指纹传输只接管 HTTPS（明文请求由 DoWithTLS 交回普通传输），用 HTTPS 目标验证其 TCP 拨号同样被拦截。
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer tlsServer.Close()
	rt2, err := buildUpstreamTransportWithTLSFingerprintGuard(defaultPoolSettings(cfg), nil, nil, upstreamProtocolModeDefault, svc.shouldValidateResolvedIP())
	require.NoError(t, err)
	resp, err = (&http.Client{Transport: rt2}).Get(tlsServer.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
	require.Contains(t, err.Error(), "dial guard")
}
