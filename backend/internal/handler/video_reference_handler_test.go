package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type videoReferenceSettingStub struct{ service.SettingRepository }

func (videoReferenceSettingStub) GetValue(context.Context, string) (string, error) {
	return "https://trusted.example.com", nil
}

// HTTP 测试只依赖存储接口；真实限次和原子语义由仓储测试负责。
type videoReferenceHTTPStore struct {
	blobs map[string]service.VideoReferenceBlob
}

func (s *videoReferenceHTTPStore) Ready(context.Context) error { return nil }
func (s *videoReferenceHTTPStore) IssueBatch(_ context.Context, batch service.VideoReferenceBatch) error {
	for _, blob := range batch.Images {
		s.blobs[blob.Digest] = blob
	}
	return nil
}
func (s *videoReferenceHTTPStore) Open(_ context.Context, digest, method string) (*service.VideoReferenceContent, error) {
	blob, ok := s.blobs[digest]
	if !ok {
		return nil, service.ErrVideoReferenceNotFound
	}
	content := &service.VideoReferenceContent{MIMEType: blob.MIMEType, Body: blob.Body, Size: int64(len(blob.Body))}
	if method == http.MethodHead {
		content.Body = nil
	}
	return content, nil
}

// 上游 GET/HEAD 只拿到票据绑定图片；不需要面板登录、不会被请求 Host 引导签发其他地址。
func TestVideoReferenceHTTPContentAndSecurityHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &videoReferenceHTTPStore{blobs: make(map[string]service.VideoReferenceBlob)}
	svc := service.NewVideoReferenceService(store, service.NewSettingService(videoReferenceSettingStub{}, &config.Config{}))
	var data bytes.Buffer
	require.NoError(t, png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 1, 1))))
	urls, err := svc.PrepareDataURLs(context.Background(), 7, []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(data.Bytes())})
	require.NoError(t, err)
	u, err := url.Parse(urls[0])
	require.NoError(t, err)
	require.Equal(t, "trusted.example.com", u.Host)
	router := gin.New()
	h := NewVideoReferenceHandler(svc)
	router.GET("/api/v1/video-references/:ticket", h.Content)
	router.HEAD("/api/v1/video-references/:ticket", h.Content)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r := httptest.NewRequest(method, u.Path, nil)
		r.Host = "attacker.example"
		r.Header.Set("X-Forwarded-Host", "attacker.example")
		r.Header.Set("Cookie", "session=do-not-forward")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, "image/png", w.Header().Get("Content-Type"))
		require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
		require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
		require.Equal(t, "no-referrer", w.Header().Get("Referrer-Policy"))
		require.Empty(t, w.Header().Get("Set-Cookie"))
		require.Empty(t, w.Header().Get("Location"))
		if method == http.MethodGet {
			require.Equal(t, data.Bytes(), w.Body.Bytes())
		} else {
			require.Empty(t, w.Body.Bytes())
		}
	}
	for _, tt := range []struct {
		method, path, rangeHeader string
		status                    int
	}{
		{http.MethodGet, u.Path, "bytes=0-1", http.StatusNotFound},
		{http.MethodGet, "/api/v1/video-references/" + strings.Repeat("0", 64), "", http.StatusNotFound},
		{http.MethodPost, u.Path, "", http.StatusNotFound},
		{http.MethodGet, "/api/v1/video-references", "", http.StatusNotFound},
	} {
		r := httptest.NewRequest(tt.method, tt.path, nil)
		r.Header.Set("Range", tt.rangeHeader)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		require.Equal(t, tt.status, w.Code)
		require.NotContains(t, w.Body.String(), "session")
	}
}

// 参考视频票据的 HTTP 替身：存储只保存授权，素材源返回固定字节。
type videoReferenceHTTPAssetStore struct {
	videoReferenceHTTPStore
	grants map[string]service.VideoReferenceAssetGrant
	reads  int
}

func (s *videoReferenceHTTPAssetStore) IssueAssetBatch(_ context.Context, batch service.VideoReferenceAssetBatch) error {
	for _, grant := range batch.Assets {
		s.grants[grant.Digest] = grant
	}
	return nil
}
func (s *videoReferenceHTTPAssetStore) OpenAsset(_ context.Context, digest, _ string) (*service.VideoReferenceAssetGrant, error) {
	grant, ok := s.grants[digest]
	if !ok {
		return nil, service.ErrVideoReferenceNotFound
	}
	s.reads++
	return &grant, nil
}

type videoReferenceHTTPAssetSource struct{ body []byte }
type videoReferenceHTTPReader struct{ *bytes.Reader }

func (videoReferenceHTTPReader) Close() error { return nil }
func (s videoReferenceHTTPAssetSource) OpenVideoReferenceAsset(_ context.Context, userID int64, assetID string) (io.ReadSeekCloser, *service.CreativeCanvasAsset, error) {
	return videoReferenceHTTPReader{bytes.NewReader(s.body)}, &service.CreativeCanvasAsset{ID: assetID, UserID: userID, MediaType: "video", MIMEType: "video/mp4", ByteSize: int64(len(s.body))}, nil
}

// 参考视频支持单段 Range 与 HEAD，每次请求消耗一次读取；多段 Range 拒绝；安全响应头与图片一致。
func TestVideoReferenceHTTPAssetStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &videoReferenceHTTPAssetStore{videoReferenceHTTPStore: videoReferenceHTTPStore{blobs: map[string]service.VideoReferenceBlob{}}, grants: map[string]service.VideoReferenceAssetGrant{}}
	svc := service.NewVideoReferenceService(store, service.NewSettingService(videoReferenceSettingStub{}, &config.Config{}))
	body := []byte("0123456789abcdef")
	svc.SetAssetSource(videoReferenceHTTPAssetSource{body: body})
	urls, err := svc.PrepareAssetURLs(context.Background(), 7, []service.VideoReferenceAsset{{AssetID: "66666666-6666-4666-8666-666666666666", MIMEType: "video/mp4", Size: int64(len(body))}})
	require.NoError(t, err)
	u, err := url.Parse(urls[0])
	require.NoError(t, err)
	router := gin.New()
	h := NewVideoReferenceHandler(svc)
	router.GET("/api/v1/video-references/:ticket", h.Content)
	router.HEAD("/api/v1/video-references/:ticket", h.Content)
	request := func(method, rangeHeader string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, u.Path, nil)
		if rangeHeader != "" {
			r.Header.Set("Range", rangeHeader)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	full := request(http.MethodGet, "")
	require.Equal(t, http.StatusOK, full.Code)
	require.Equal(t, body, full.Body.Bytes())
	require.Equal(t, "video/mp4", full.Header().Get("Content-Type"))
	require.Equal(t, "private, no-store", full.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", full.Header().Get("X-Content-Type-Options"))
	partial := request(http.MethodGet, "bytes=2-5")
	require.Equal(t, http.StatusPartialContent, partial.Code)
	require.Equal(t, "2345", partial.Body.String())
	require.Equal(t, "bytes 2-5/16", partial.Header().Get("Content-Range"))
	head := request(http.MethodHead, "")
	require.Equal(t, http.StatusOK, head.Code)
	require.Empty(t, head.Body.Bytes())
	require.Equal(t, http.StatusNotFound, request(http.MethodGet, "bytes=0-1,4-5").Code)
	require.Equal(t, 3, store.reads, "多段 Range 在消耗读取前拒绝")
}
