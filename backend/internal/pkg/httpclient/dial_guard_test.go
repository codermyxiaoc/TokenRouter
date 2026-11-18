package httpclient

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 复现 DNS Rebinding：请求前的解析校验返回“公网”（被替换为恒通过），真正拨号时目标是
// 127.0.0.1。修复后拨号钩子必须拦截实际连接的回环地址，请求失败且本地服务未被访问。
func TestValidatedClientBlocksLoopbackAtDialTime(t *testing.T) {
	originalValidate := validateResolvedIP
	defer func() { validateResolvedIP = originalValidate }()
	// 模拟攻击者控制的域名：预检阶段解析出公网地址，因此预检放行。
	validateResolvedIP = func(string) error { return nil }

	hit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := buildClient(Options{ValidateResolvedIP: true})
	require.NoError(t, err)
	resp, err := client.Get(server.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "dial guard"), err.Error())
	require.False(t, hit, "拨号钩子应在连接前阻断，回环服务不得收到请求")
}

// 允许私网或未开启校验的客户端保持原行为，可正常访问本地服务。
func TestUnvalidatedClientsStillReachLoopback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	for _, opts := range []Options{{}, {ValidateResolvedIP: true, AllowPrivateHosts: true}} {
		client, err := buildClient(opts)
		require.NoError(t, err)
		resp, err := client.Get(server.URL)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusNoContent, resp.StatusCode)
	}
}
