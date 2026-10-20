package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 模拟 SDK 结构化分类；正文故意包含签名和凭据，确保公开结果不依赖原始错误。
type imageStorageAPIError string

func (e imageStorageAPIError) ErrorCode() string { return string(e) }
func (e imageStorageAPIError) Error() string {
	return "secret-token https://private.test/image?X-Amz-Signature=private-signature"
}

func TestImageStorageFailureDetailsUseStructuredErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"bucket_missing", imageStorageAPIError("NoSuchBucket"), imageStorageBucketMissing},
		{"permission", imageStorageAPIError("AccessDenied"), imageStorageAccessDenied},
		{"invalid_key", imageStorageAPIError("InvalidAccessKeyId"), imageStorageAccessDenied},
		{"bad_signature", imageStorageAPIError("SignatureDoesNotMatch"), imageStorageAccessDenied},
		{"forbidden", imageRetryHTTPError(http.StatusForbidden), imageStorageAccessDenied},
		{"unauthorized", imageRetryStatusError(http.StatusUnauthorized), imageStorageAccessDenied},
		{"unknown_404", imageRetryHTTPError(http.StatusNotFound), imageStorageUploadFailed},
		{"request_timeout", imageStorageAPIError("RequestTimeout"), imageStorageTimeout},
		{"http_request_timeout", imageRetryHTTPError(http.StatusRequestTimeout), imageStorageTimeout},
		{"http_gateway_timeout", imageRetryStatusError(http.StatusGatewayTimeout), imageStorageTimeout},
		{"deadline", context.DeadlineExceeded, imageStorageTimeout},
		{"network_timeout", &net.DNSError{IsTimeout: true}, imageStorageTimeout},
		{"unstructured", errors.New("NoSuchBucket private-signature secret-token"), imageStorageUploadFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stage, code, message := ImageStorageFailureDetails(fmt.Errorf("SDK operation private-signature: %w", tc.err))
			require.Equal(t, "upload", stage)
			require.Equal(t, tc.code, code)
			require.NotContains(t, message, "private-signature")
			require.NotContains(t, message, "secret-token")
			require.Contains(t, message, "generation usage may already have been billed")
		})
	}
}

// 来源下载、结果校验与目标存储分开归因；失败只写紧凑错误，不保留大正文或原始 SDK 信息。
func TestImageTaskResultFailureClassification(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	validBody := `{"data":[{"b64_json":"` + b64 + `"}]}`
	urlBody := `{"data":[{"url":"https://image.test/a.png?X-Amz-Signature=private-signature"}]}`
	for _, tc := range []struct {
		name       string
		body       string
		limit      int64
		storageErr error
		download   []byte
		httpStatus int
		stage      string
		code       string
		summary    string
	}{
		{"source_http_403", urlBody, 0, nil, nil, 403, "download", imageSourceDownloadFailed, "来源图片下载失败"},
		{"source_transport_error", urlBody, 0, nil, nil, -1, "download", imageSourceDownloadFailed, "来源图片下载失败"},
		{"source_too_large", urlBody, 3, nil, pngBytes, 200, "download", imageResultTooLarge, "超过转存大小限制"},
		{"base64_too_large", validBody, 3, nil, nil, 0, "decode", imageResultTooLarge, "超过转存大小限制"},
		{"data_url_too_large", `{"data":[{"url":"data:image/png;base64,` + b64 + `"}]}`, 3, nil, nil, 0, "decode", imageResultTooLarge, "超过转存大小限制"},
		{"invalid_base64", `{"data":[{"b64_json":"private-signature"}]}`, 0, nil, nil, 0, "decode", imageResultInvalid, "格式或编码无效"},
		{"not_image", `{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString([]byte("<html>private-signature</html>")) + `"}]}`, 0, nil, nil, 0, "decode", imageResultInvalid, "格式或编码无效"},
		{"bucket_missing", validBody, 0, imageStorageAPIError("NoSuchBucket"), nil, 0, "upload", imageStorageBucketMissing, "目标存储桶不存在"},
		{"storage_forbidden", validBody, 0, imageStorageAPIError("AccessDenied"), nil, 0, "upload", imageStorageAccessDenied, "存储访问被拒绝"},
		{"storage_timeout", validBody, 0, context.DeadlineExceeded, nil, 0, "upload", imageStorageTimeout, "上传存储超时"},
		{"storage_unknown", validBody, 0, errors.New("private-signature secret-token"), nil, 0, "upload", imageStorageUploadFailed, "上传存储失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := &fakeImageStorage{err: tc.storageErr}
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if tc.httpStatus < 0 {
					return nil, errors.New("secret-token private-signature")
				}
				return &http.Response{StatusCode: tc.httpStatus, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(tc.download))}, nil
			})}
			uploader := NewImageResultUploader(storage, "images/", tc.limit, client)
			_, err := uploader.Rewrite(context.Background(), "imgtask_classification", json.RawMessage(tc.body))
			require.Error(t, err)
			stage, code, message := ImageStorageFailureDetails(err)
			require.Equal(t, tc.stage, stage)
			require.Equal(t, tc.code, code)
			require.NotContains(t, message, "private-signature")
			if tc.stage != "upload" {
				require.Empty(t, storage.saved, "来源下载和校验失败不得调用目标存储")
				require.NotContains(t, message, "object storage")
			}

			store := &imageTaskMemoryStore{}
			svc := NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute)
			owner := ImageTaskOwner{UserID: 7, APIKeyID: 9}
			task, err := svc.Create(context.Background(), owner)
			require.NoError(t, err)
			require.NoError(t, svc.CompleteWithUploader(context.Background(), task.ID, http.StatusOK, json.RawMessage(tc.body), uploader))
			got, err := svc.Get(context.Background(), owner, task.ID)
			require.NoError(t, err)
			require.Equal(t, ImageTaskStatusFailed, got.Status)
			require.Equal(t, http.StatusBadGateway, got.HTTPStatus)
			require.Empty(t, got.Result)
			var envelope map[string]string
			require.NoError(t, json.Unmarshal(got.Error, &envelope))
			require.Equal(t, "api_error", envelope["type"])
			require.Equal(t, tc.code, envelope["code"])
			require.Contains(t, ImageTaskFailureSummary(got.Error), tc.summary)
			require.NotContains(t, string(got.Error), "private-signature")
			require.NotContains(t, string(got.Error), "secret-token")
			require.NotContains(t, string(got.Error), "X-Amz")
		})
	}
}

func TestImageTaskFailureSummaryCompatibilityAndSafety(t *testing.T) {
	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"type":"api_error","message":"image was generated but object storage failed; generation usage may already have been billed"}`, "图片生成完成但结果存储失败，生成用量可能已经计费"},
		{`{"type":"timeout_error"}`, "图片生成任务超时"},
		{`{"type":"execution_interrupted"}`, "执行中断，结果不确定且可能已计费；系统未重新生成"},
		{`{"type":"image_storage_unavailable"}`, "图片存储不可用，未开始生成且未产生生成费用，请联系管理员"},
		{`{"type":"api_error","code":"unknown","message":"https://image.test/?secret-token"}`, "图片生成失败，请根据请求 ID 查看错误日志"},
		{`{"type":"api_error","code":"image_source_download_failed","message":"secret-token"}`, "图片生成完成但来源图片下载失败，生成用量可能已经计费"},
		{`{"type":"api_error","code":"image_result_timeout"}`, "图片生成完成但结果处理超时，生成用量可能已经计费"},
		{`{invalid-json`, "图片生成失败，请根据请求 ID 查看错误日志"},
	} {
		require.Equal(t, tc.want, ImageTaskFailureSummary(json.RawMessage(tc.body)))
	}
}
