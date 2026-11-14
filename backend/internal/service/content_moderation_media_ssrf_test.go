//go:build unit

package service

import (
	"context"
	"net"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// 审核快照的地址判定与图片下载一致：CGNAT、this-network、私网、回环、链路本地均拒绝。
func TestContentModerationPublicIPRejectsReservedRanges(t *testing.T) {
	for _, addr := range []string{
		"100.64.0.1", "100.127.255.254", "0.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1",
		"127.0.0.1", "169.254.169.254", "::1", "fe80::1", "fc00::1", "0.0.0.0", "224.0.0.1",
	} {
		require.False(t, isContentModerationPublicIP(net.ParseIP(addr)), addr)
	}
	for _, addr := range []string{"8.8.8.8", "100.63.255.255", "100.128.0.1", "2001:4860:4860::8888"} {
		require.True(t, isContentModerationPublicIP(net.ParseIP(addr)), addr)
	}
}

// 元数据主机名与保留 IP 字面量在解析前即被拒绝，重定向校验同样适用。
func TestContentModerationRemoteURLRejectsMetadataHosts(t *testing.T) {
	for _, raw := range []string{
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://169.254.169.254/latest/meta-data/",
		"http://100.64.0.1/a.png",
		"http://0.0.0.1/a.png",
		"http://localhost/a.png",
	} {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		require.Error(t, validateContentModerationRemoteURL(context.Background(), u), raw)
	}
}
