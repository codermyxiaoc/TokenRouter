package service

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 字面量与 DNS 返回值共用同一判断，覆盖元数据网段和 IPv4 映射 IPv6。
func TestImageDownloadRejectsReservedDestinations(t *testing.T) {
	for _, address := range []string{"100.64.0.1", "100.100.100.200", "100.127.255.254", "0.1.2.3", "::ffff:100.100.100.200"} {
		require.False(t, isPublicImageDownloadIP(net.ParseIP(address)), address)
	}
	for _, address := range []string{"8.8.8.8", "100.128.0.1", "2606:4700:4700::1111"} {
		require.True(t, isPublicImageDownloadIP(net.ParseIP(address)), address)
	}
	client := defaultImageDownloadHTTPClient()
	for _, host := range []string{"metadata.google.internal", "METADATA.GOOG.", "instance-data", "100.100.100.200", "0.1.2.3"} {
		require.True(t, isBlockedImageDownloadHost(host))
		req, err := http.NewRequest(http.MethodGet, "http://"+host+"/image", nil)
		require.NoError(t, err)
		require.Error(t, client.CheckRedirect(req, nil))
	}
}

// 视频下载拥有独立预算，修改其客户端不能扩展图片下载时限或产生共享字段写入。
func TestImageDownloadClientsIsolateConfigurationAndReusePool(t *testing.T) {
	imageClient := defaultImageDownloadHTTPClient()
	videoClient := defaultImageDownloadHTTPClient()
	require.NotSame(t, imageClient, videoClient)
	require.Same(t, imageClient.Transport, videoClient.Transport)
	videoClient.Timeout = 2 * time.Minute
	require.Equal(t, time.Minute, imageClient.Timeout)
	require.Equal(t, time.Minute, defaultImageDownloadHTTPClient().Timeout)
	require.Nil(t, imageClient.Transport.(*http.Transport).Proxy)
}

// 保留 fork 的位图白名单，非图片和伪造图片声明都不能进入存储桶。
func TestImageStorageSyncRejectsNonImageResponse(t *testing.T) {
	for _, contentType := range []string{"application/json", "text/html", "image/png", "image/svg+xml"} {
		t.Run(contentType, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", contentType)
				_, _ = w.Write([]byte(`<html>not an image</html>`))
			}))
			defer server.Close()
			storage := &fakeImageStorage{}
			uploader := NewImageResultUploader(storage, "images/", 0, server.Client())
			body, err := json.Marshal(map[string]any{"data": []map[string]string{{"url": server.URL}}})
			require.NoError(t, err)
			_, err = uploader.Rewrite(t.Context(), "imgtask_invalid_content", body)
			require.Error(t, err)
			require.Empty(t, storage.saved)
		})
	}
}
