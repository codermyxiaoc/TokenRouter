package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 并发使用不同媒体预算时，只共享不可变连接池配置，不交叉修改客户端字段。
func TestImageStability_ConcurrentClientConfigurationIsolation(t *testing.T) {
	base := defaultImageDownloadHTTPClient()
	var wg sync.WaitGroup
	results := make(chan bool, 64)
	for i := range 64 {
		wg.Go(func() {
			client := defaultImageDownloadHTTPClient()
			client.Timeout = time.Duration(i+1) * time.Second
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			fresh := defaultImageDownloadHTTPClient()
			results <- fresh.Timeout == time.Minute && client != fresh && fresh.Transport == base.Transport
		})
	}
	wg.Wait()
	close(results)
	for result := range results {
		require.True(t, result)
	}
}

// 使用真正默认拨号器，确认内网 URL 不会接触本机 HTTP 服务。
func TestImageStability_DefaultClientNeverContactsLoopback(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer server.Close()
	storage := &fakeImageStorage{}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)
	body, _ := json.Marshal(map[string]any{"data": []map[string]string{{"url": server.URL + "/pic.png"}}})
	_, err := uploader.Rewrite(t.Context(), "blocked", body)
	require.Error(t, err)
	require.Zero(t, hits.Load())
	require.Empty(t, storage.saved)
}

// 替身只模拟第一跳，安全客户端自身必须在下一次传输前拒绝危险重定向。
func TestImageStability_RedirectRejectedBeforeSecondRequest(t *testing.T) {
	for _, target := range []string{"http://127.0.0.1/image", "http://100.100.100.200/meta", "http://0.1.2.3/image", "http://metadata.google.internal/image", "file:///etc/passwd", "https://user:pass@public.example/image"} {
		t.Run(target, func(t *testing.T) {
			var calls atomic.Int32
			client := defaultImageDownloadHTTPClient()
			client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{target}}, Body: io.NopCloser(bytes.NewReader(nil)), Request: req}, nil
			})
			storage := &fakeImageStorage{}
			uploader := NewImageResultUploader(storage, "images/", 0, client)
			_, err := uploader.Rewrite(t.Context(), "redirect", json.RawMessage(`{"data":[{"url":"https://public.example/image"}]}`))
			require.Error(t, err)
			require.Equal(t, int32(1), calls.Load())
			require.Empty(t, storage.saved)
		})
	}
}

func TestImageStability_RedirectLimitAndCredentials(t *testing.T) {
	var calls atomic.Int32
	client := defaultImageDownloadHTTPClient()
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		n := calls.Add(1)
		require.Empty(t, req.Header.Get("Authorization"))
		require.Empty(t, req.Header.Get("X-Api-Key"))
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://public.example/image?step=" + strconv.Itoa(int(n))}}, Body: io.NopCloser(bytes.NewReader(nil)), Request: req}, nil
	})
	_, _, err := NewImageResultUploader(&fakeImageStorage{}, "", 0, client).download(t.Context(), "https://public.example/image")
	require.ErrorContains(t, err, "too many image download redirects")
	require.Equal(t, int32(5), calls.Load())
}

// 固定错误内容不可通过重试变成正确图片，不应反复下载或写桶。
func TestImageStability_InvalidBodyAndSizeDoNotRetry(t *testing.T) {
	for _, tc := range []struct {
		name, typ string
		body      []byte
		limit     int64
	}{
		{"json", "application/json", []byte(`{"error":"failed"}`), 1024},
		{"spoofed png", "image/png", []byte("not png"), 1024},
		{"empty image", "image/png", nil, 1024},
		{"oversized image", "image/png", pngBytes, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{tc.typ}}, Body: io.NopCloser(bytes.NewReader(tc.body)), Request: req}, nil
			})}
			storage := &fakeImageStorage{}
			_, err := NewImageResultUploader(storage, "", tc.limit, client).Rewrite(context.Background(), "invalid", json.RawMessage(`{"data":[{"url":"https://public.example/image"}]}`))
			require.Error(t, err)
			require.Equal(t, int32(1), calls.Load())
			require.Empty(t, storage.saved)
		})
	}
}
