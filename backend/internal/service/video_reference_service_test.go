package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 服务层仅验证业务与信任边界，Redis 原子性和过期行为由仓储测试覆盖。
type videoReferenceMemoryStore struct {
	blobs                       map[string]VideoReferenceBlob
	batch                       VideoReferenceBatch
	issueCalls, openCalls       int
	readyErr, issueErr, openErr error
	override                    *VideoReferenceContent
}

func (s *videoReferenceMemoryStore) Ready(context.Context) error { return s.readyErr }
func (s *videoReferenceMemoryStore) IssueBatch(_ context.Context, batch VideoReferenceBatch) error {
	s.issueCalls++
	if s.issueErr != nil {
		return s.issueErr
	}
	s.batch = batch
	for _, blob := range batch.Images {
		s.blobs[blob.Digest] = blob
	}
	return nil
}
func (s *videoReferenceMemoryStore) Open(_ context.Context, digest, method string) (*VideoReferenceContent, error) {
	s.openCalls++
	if s.openErr != nil {
		return nil, s.openErr
	}
	if s.override != nil {
		return s.override, nil
	}
	blob, ok := s.blobs[digest]
	if !ok {
		return nil, ErrVideoReferenceNotFound
	}
	content := &VideoReferenceContent{MIMEType: blob.MIMEType, Body: blob.Body, Size: int64(len(blob.Body))}
	if method == http.MethodHead {
		content.Body = nil
	}
	return content, nil
}

// 使用真实的小型 PNG 验证内容识别，不依赖远程图片或生产存储。
func videoReferenceTestImage(t *testing.T) ([]byte, string) {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, png.Encode(&out, image.NewNRGBA(image.Rect(0, 0, 2, 2))))
	return out.Bytes(), "data:image/png;base64," + base64.StdEncoding.EncodeToString(out.Bytes())
}
func videoReferenceTestService(t *testing.T) (*VideoReferenceService, *videoReferenceMemoryStore) {
	t.Helper()
	store := &videoReferenceMemoryStore{blobs: make(map[string]VideoReferenceBlob)}
	return &VideoReferenceService{store: store, baseURL: func(context.Context) string { return "https://canvas.example.com" }}, store
}
func videoReferenceToken(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return strings.TrimPrefix(u.Path, videoReferencePath)
}

func TestVideoReferenceIssueContractAndExactContent(t *testing.T) {
	s, store := videoReferenceTestService(t)
	body, dataURL := videoReferenceTestImage(t)
	require.NoError(t, s.ValidateReady(context.Background()))
	urls, err := s.PrepareDataURLs(context.Background(), 7, []string{dataURL, dataURL})
	require.NoError(t, err)
	require.Len(t, urls, 2)
	require.NotEqual(t, urls[0], urls[1])
	require.Equal(t, int64(7), store.batch.UserID)
	require.Equal(t, VideoReferenceTTL, store.batch.ExpiresAt.Sub(store.batch.IssuedAt))
	require.Equal(t, videoReferenceMaxReads, store.batch.MaxReads)
	require.Equal(t, videoReferenceHourlyCount, store.batch.HourlyMaxCount)
	require.Equal(t, int64(videoReferenceHourlyBytes), store.batch.HourlyMaxBytes)
	token := videoReferenceToken(t, urls[0])
	require.Len(t, token, 64)
	require.Equal(t, videoReferenceDigest(token), store.batch.Images[0].Digest)
	require.NotEqual(t, token, store.batch.Images[0].Digest)
	content, err := s.Open(context.Background(), token, http.MethodGet)
	require.NoError(t, err)
	require.Equal(t, body, content.Body)
	head, err := s.Open(context.Background(), token, http.MethodHead)
	require.NoError(t, err)
	require.Empty(t, head.Body)
	require.Equal(t, int64(len(body)), head.Size)
}

func TestVideoReferenceEmptyInputDoesNotRequirePublicURL(t *testing.T) {
	urls, err := (*VideoReferenceService)(nil).PrepareDataURLs(context.Background(), 0, nil)
	require.NoError(t, err)
	require.Empty(t, urls)
}

func TestVideoReferenceInvalidBatchNeverPartiallyPublishes(t *testing.T) {
	_, valid := videoReferenceTestImage(t)
	for _, bad := range []string{"https://example.com/image.png", "file:///etc/passwd", "data:image/svg+xml;base64,PHN2Zz4=", "data:image/png;base64,aGVsbG8=", strings.Replace(valid, "image/png", "image/jpeg", 1), "data:image/png;base64,%%"} {
		t.Run(bad[:min(30, len(bad))], func(t *testing.T) {
			s, store := videoReferenceTestService(t)
			_, err := s.PrepareDataURLs(context.Background(), 7, []string{valid, bad})
			require.ErrorIs(t, err, ErrVideoReferenceInvalid)
			require.Zero(t, store.issueCalls)
		})
	}
	s, store := videoReferenceTestService(t)
	_, err := s.PrepareDataURLs(context.Background(), 0, []string{valid})
	require.ErrorIs(t, err, ErrVideoReferenceInvalid)
	_, err = s.PrepareDataURLs(context.Background(), 7, make([]string, 17))
	require.ErrorIs(t, err, ErrVideoReferenceInvalid)
	require.Zero(t, store.issueCalls)
	_, _, err = decodeVideoReferenceDataURL(valid, 1)
	require.ErrorIs(t, err, ErrVideoReferenceInvalid)
}

func TestVideoReferencePublicBaseRejectsUnsafeConfiguration(t *testing.T) {
	_, valid := videoReferenceTestImage(t)
	for _, base := range []string{"", "http://localhost:8080", "http://localhost.", "http://127.0.0.1", "http://127.0.0.1.", "http://[::1]", "http://10.0.0.1", "http://169.254.169.254", "http://printer.local", "http://metadata.internal", "http://hostname", "https://user:secret@example.com", "https://example.com?key=secret", "https://example.com?", "https://example.com/#x", "ftp://example.com", "https://example.com:99999"} {
		t.Run(base, func(t *testing.T) {
			s, store := videoReferenceTestService(t)
			s.baseURL = func(context.Context) string { return base }
			require.ErrorIs(t, s.ValidateReady(context.Background()), ErrVideoReferencePublicURL)
			_, err := s.PrepareDataURLs(context.Background(), 7, []string{valid})
			require.ErrorIs(t, err, ErrVideoReferencePublicURL)
			require.Zero(t, store.issueCalls)
		})
	}
}

// 前端基础 URL 可以包含页面路径；生成的供应商链接始终使用可信站点的 API 根路径。
func TestVideoReferencePublicBaseUsesOrigin(t *testing.T) {
	s, _ := videoReferenceTestService(t)
	s.baseURL = func(context.Context) string { return "https://trusted.example.com:8443/creative/canvas%20page/" }
	_, valid := videoReferenceTestImage(t)
	urls, err := s.PrepareDataURLs(context.Background(), 7, []string{valid})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(urls[0], "https://trusted.example.com:8443/api/v1/video-references/"))
	require.NotContains(t, urls[0], "/creative")
	require.NotContains(t, urls[0], "%20")
}

func TestVideoReferenceMalformedTokenAndStoreFailureFailClosed(t *testing.T) {
	s, store := videoReferenceTestService(t)
	for _, token := range []string{"", strings.Repeat("g", 64), strings.Repeat("A", 64), "../asset"} {
		_, err := s.Open(context.Background(), token, http.MethodGet)
		require.ErrorIs(t, err, ErrVideoReferenceNotFound)
	}
	_, err := s.Open(context.Background(), strings.Repeat("a", 64), http.MethodPost)
	require.ErrorIs(t, err, ErrVideoReferenceNotFound)
	require.Zero(t, store.openCalls)
	store.readyErr = errors.New("private infrastructure failure")
	require.ErrorIs(t, s.ValidateReady(context.Background()), ErrVideoReferenceUnavailable)
	_, valid := videoReferenceTestImage(t)
	_, err = s.PrepareDataURLs(context.Background(), 7, []string{valid})
	require.ErrorIs(t, err, ErrVideoReferenceUnavailable)
	require.Zero(t, store.issueCalls)
	store.readyErr = nil
	store.issueErr = ErrVideoReferenceQuota
	_, err = s.PrepareDataURLs(context.Background(), 7, []string{valid})
	require.ErrorIs(t, err, ErrVideoReferenceQuota)
	store.openErr = errors.New("private infrastructure failure")
	_, err = s.Open(context.Background(), strings.Repeat("a", 64), http.MethodGet)
	require.ErrorIs(t, err, ErrVideoReferenceNotFound)
}

func TestVideoReferenceRejectsCorruptStoredContent(t *testing.T) {
	for _, content := range []*VideoReferenceContent{
		{MIMEType: "image/svg+xml", Size: 1, Body: []byte("x")},
		{MIMEType: "image/png", Size: 1, Body: nil},
		{MIMEType: "image/png", Size: VideoReferenceMaxBytes + 1},
	} {
		s, store := videoReferenceTestService(t)
		store.override = content
		_, err := s.Open(context.Background(), strings.Repeat("a", 64), http.MethodGet)
		require.ErrorIs(t, err, ErrVideoReferenceNotFound)
	}
}
