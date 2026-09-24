package service

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type imageRetryHTTPError int

func (e imageRetryHTTPError) Error() string       { return fmt.Sprintf("HTTP %d", e) }
func (e imageRetryHTTPError) HTTPStatusCode() int { return int(e) }

type imageRetryStatusError int

func (e imageRetryStatusError) Error() string   { return fmt.Sprintf("status %d", e) }
func (e imageRetryStatusError) StatusCode() int { return int(e) }

// 记录失败上传在内的全部请求，以核实丢失响应时也复用同一对象键与图片字节。
type imageRetryStorage struct {
	calls []savedImage
	fail  func(int) error
}

func (s *imageRetryStorage) Save(_ context.Context, key, contentType string, data []byte) (string, error) {
	s.calls = append(s.calls, savedImage{key: key, contentType: contentType, data: append([]byte(nil), data...)})
	if s.fail != nil {
		if err := s.fail(len(s.calls)); err != nil {
			return "", err
		}
	}
	return "https://cdn.test/" + key, nil
}

func imageRetryB64Result() json.RawMessage {
	return json.RawMessage(`{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(pngBytes) + `"}],"usage":{"input_tokens":12}}`)
}

// 仅明确的临时网络、限流和服务端故障允许重试，普通参数与权限错误保持立即失败。
func TestImageTransferRetryClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unknown", errors.New("storage misconfigured"), false},
		{"forbidden", imageRetryHTTPError(403), false},
		{"bad_request", imageRetryStatusError(400), false},
		{"not_found", imageRetryHTTPError(404), false},
		{"request_timeout_4xx", imageRetryHTTPError(408), false},
		{"rate_limit", imageRetryStatusError(429), true},
		{"wrapped_service_error", fmt.Errorf("S3 PutObject: %w", imageRetryHTTPError(503)), true},
		{"proxy_timeout", imageRetryHTTPError(524), true},
		{"connection_error", &net.OpError{Op: "write", Net: "tcp", Err: errors.New("connection reset")}, true},
		{"timeout", &net.DNSError{IsTimeout: true}, true},
		{"truncated_body", fmt.Errorf("read image: %w", io.ErrUnexpectedEOF), true},
		{"closed_connection", io.EOF, true},
		{"cancelled", context.Canceled, false},
		{"url_network", &url.Error{Op: "Get", URL: "https://image.test/a.png", Err: &net.DNSError{IsTimeout: true}}, true},
		{"url_policy", &url.Error{Op: "Get", URL: "http://127.0.0.1/a.png", Err: errors.New("image download target is not public")}, false},
		{"url_certificate", &url.Error{Op: "Get", URL: "https://image.test/a.png", Err: x509.UnknownAuthorityError{}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, isRetryableImageTransferError(tc.err)) })
	}
}

func TestImageResultUploaderRetriesTemporaryUploadWithStableObject(t *testing.T) {
	storage := &imageRetryStorage{fail: func(attempt int) error {
		if attempt == 1 {
			return fmt.Errorf("S3 PutObject: %w", imageRetryHTTPError(http.StatusServiceUnavailable))
		}
		return nil
	}}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)
	out, err := uploader.Rewrite(context.Background(), "imgtask_retry", imageRetryB64Result())
	require.NoError(t, err)
	require.Len(t, storage.calls, 2)
	for _, call := range storage.calls {
		require.Equal(t, "images/imgtask_retry-0.png", call.key)
		require.Equal(t, "image/png", call.contentType)
		require.Equal(t, pngBytes, call.data)
	}
	require.NotContains(t, string(out), "b64_json")
	require.Contains(t, string(out), `"url":"https://cdn.test/images/imgtask_retry-0.png"`)
	require.Contains(t, string(out), `"input_tokens":12`, "转存重试必须保留生成用量")
}

func TestImageResultUploaderUploadRetriesAreBounded(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failure  error
		attempts int
	}{
		{"repeated_429", imageRetryStatusError(429), imageTransferMaxAttempts},
		{"permanent_403", imageRetryHTTPError(403), 1},
		{"unknown_failure", errors.New("invalid storage settings"), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := &imageRetryStorage{fail: func(int) error { return tc.failure }}
			uploader := NewImageResultUploader(storage, "images/", 0, nil)
			out, err := uploader.Rewrite(context.Background(), "imgtask_fail", imageRetryB64Result())
			require.ErrorIs(t, err, tc.failure)
			require.Nil(t, out)
			require.Len(t, storage.calls, tc.attempts)
			for _, call := range storage.calls {
				require.Equal(t, "images/imgtask_fail-0.png", call.key)
			}
		})
	}
}

func TestImageResultUploaderRetryRespectsParentCancellation(t *testing.T) {
	t.Run("cancelled_before_transfer", func(t *testing.T) {
		storage := &imageRetryStorage{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := NewImageResultUploader(storage, "", 0, nil).Rewrite(ctx, "imgtask_cancel", imageRetryB64Result())
		require.ErrorIs(t, err, context.Canceled)
		require.Empty(t, storage.calls)
	})
	t.Run("deadline_during_backoff", func(t *testing.T) {
		storage := &imageRetryStorage{fail: func(int) error { return imageRetryHTTPError(503) }}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancel()
		_, err := NewImageResultUploader(storage, "", 0, nil).Rewrite(ctx, "imgtask_timeout", imageRetryB64Result())
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Len(t, storage.calls, 1, "外层截止后不得再上传")
	})
	t.Run("cancelled_by_storage", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		storage := &imageRetryStorage{fail: func(int) error { cancel(); return imageRetryHTTPError(503) }}
		_, err := NewImageResultUploader(storage, "", 0, nil).Rewrite(ctx, "imgtask_cancel", imageRetryB64Result())
		require.ErrorIs(t, err, context.Canceled)
		require.Len(t, storage.calls, 1)
	})
}

// 后续图片的下载或上传失败，只重试对应阶段；此前成功图片及已下载字节不得重复处理。
func TestImageResultUploaderRetryDoesNotRestartEarlierImages(t *testing.T) {
	downloads := make(map[string]int)
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		downloads[req.URL.Path]++
		status := http.StatusOK
		if req.URL.Path == "/second.png" && downloads[req.URL.Path] == 1 {
			status = http.StatusTooManyRequests
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"image/png"}}, Body: io.NopCloser(bytes.NewReader(pngBytes))}, nil
	})}
	storage := &imageRetryStorage{fail: func(attempt int) error {
		if attempt == 2 {
			return imageRetryHTTPError(503)
		}
		return nil
	}}
	uploader := NewImageResultUploader(storage, "images/", 0, client)
	out, err := uploader.Rewrite(context.Background(), "imgtask_many", json.RawMessage(`{"data":[{"url":"https://image.test/first.png"},{"url":"https://image.test/second.png"}]}`))
	require.NoError(t, err)
	require.Equal(t, map[string]int{"/first.png": 1, "/second.png": 2}, downloads)
	require.Len(t, storage.calls, 3)
	require.Equal(t, "images/imgtask_many-0.png", storage.calls[0].key)
	require.Equal(t, "images/imgtask_many-1.png", storage.calls[1].key)
	require.Equal(t, storage.calls[1], storage.calls[2], "上传失败重试应保留完整字节及对象键")
	require.Contains(t, string(out), "images/imgtask_many-0.png")
	require.Contains(t, string(out), "images/imgtask_many-1.png")
}

func TestImageResultUploaderDownloadRetriesAreBounded(t *testing.T) {
	for _, tc := range []struct {
		status, attempts int
	}{
		{http.StatusServiceUnavailable, imageTransferMaxAttempts},
		{http.StatusForbidden, 1},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			downloads := 0
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				downloads++
				return &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(nil))}, nil
			})}
			storage := &imageRetryStorage{}
			_, err := NewImageResultUploader(storage, "", 0, client).Rewrite(context.Background(), "imgtask_download", json.RawMessage(`{"data":[{"url":"https://image.test/image.png"}]}`))
			require.ErrorContains(t, err, fmt.Sprintf("unexpected status %d", tc.status))
			require.Equal(t, tc.attempts, downloads)
			require.Empty(t, storage.calls)
		})
	}
}

// http.Client 会把底层连接错误包装为 url.Error，仍应只恢复下载而不提前上传空结果。
func TestImageResultUploaderRecoversTemporaryDownloadConnection(t *testing.T) {
	downloads := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		downloads++
		if downloads == 1 {
			return nil, &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(pngBytes))}, nil
	})}
	storage := &imageRetryStorage{}
	out, err := NewImageResultUploader(storage, "images/", 0, client).Rewrite(context.Background(), "imgtask_connection", json.RawMessage(`{"data":[{"url":"https://image.test/image.png"}]}`))
	require.NoError(t, err)
	require.Equal(t, 2, downloads)
	require.Len(t, storage.calls, 1)
	require.Equal(t, pngBytes, storage.calls[0].data)
	require.Contains(t, string(out), "images/imgtask_connection-0.png")
}

// Base64 固定输入截断也会返回 EOF，必须立即报告参数错误而不是进入网络重试等待。
func TestImageResultUploaderDoesNotRetryInvalidEncodedInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	storage := &imageRetryStorage{}
	_, err := NewImageResultUploader(storage, "", 0, nil).Rewrite(ctx, "imgtask_invalid", json.RawMessage(`{"data":[{"url":"data:image/png;base64,A"}]}`))
	require.Error(t, err)
	require.NotErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "decode image data URL base64 payload")
	require.Empty(t, storage.calls)
}
