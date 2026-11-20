package httputil

import (
	"bytes"
	"compress/gzip"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/jsonutil"
	"github.com/stretchr/testify/require"
)

// 文本端点在解压时执行专属上限，并给下游一致的无 BOM 正文。
func TestPrepareGatewayBodyCompressedBoundary(t *testing.T) {
	data := []byte("\xef\xbb\xbf" + `{"model":"sample","metadata":{"label":"demo"}}`)
	for _, limit := range []int64{int64(len(data)), int64(len(data) - 1)} {
		var compressed bytes.Buffer
		zw := gzip.NewWriter(&compressed)
		_, err := zw.Write(data)
		require.NoError(t, err)
		require.NoError(t, zw.Close())
		req := httptest.NewRequest("POST", "/v1/embeddings", &compressed)
		req.Header.Set("Content-Encoding", "gzip")
		err = PrepareGatewayRequestBody(req, limit)
		if limit < int64(len(data)) {
			var size *http.MaxBytesError
			require.True(t, errors.As(err, &size))
			require.Equal(t, limit, size.Limit)
			continue
		}
		require.NoError(t, err)
		got, err := ReadRequestBodyWithPrealloc(req)
		require.NoError(t, err)
		require.Equal(t, data[3:], got)
		require.Equal(t, int64(len(got)), req.ContentLength)
	}
}

// HTTP 与 WebSocket 对嵌套对象使用一致的判定；自定义工具字段区分大小写。
func TestGatewayBodyAndFrameAgree(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"options":{"style":"short","Style":"short"}}`, false},
		{`{"schema":{"properties":{"id":{"type":"string"},"ID":{"type":"number"}}}}`, true},
		{`{"items":[{"label":"first"},{"label":"second"}]}`, true},
	} {
		body := []byte(tc.body)
		req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
		for _, err := range []error{PrepareGatewayRequestBody(req, 0), jsonutil.ValidateGatewayFrame(body)} {
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, jsonutil.ErrDuplicateField)
			}
		}
	}
}

// 多图输入可以保留同名 part，普通标量遵循唯一字段约束。
func TestPreparedMultipartCardinality(t *testing.T) {
	for _, tc := range []struct {
		name  string
		valid bool
	}{{"image", true}, {"image_url", true}, {"source_images[]", true}, {"size", false}} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := multipart.NewWriter(&buf)
			require.NoError(t, w.WriteField(tc.name, "first"))
			require.NoError(t, w.WriteField(tc.name, "second"))
			require.NoError(t, w.Close())
			req := httptest.NewRequest("POST", "/v1/images/edits", &buf)
			req.Header.Set("Content-Type", w.FormDataContentType())
			err := PrepareGatewayRequestBody(req, 0)
			if !tc.valid {
				require.ErrorIs(t, err, jsonutil.ErrDuplicateField)
				return
			}
			require.NoError(t, err)
			require.NoError(t, req.ParseMultipartForm(1024))
			require.Equal(t, []string{"first", "second"}, req.MultipartForm.Value[tc.name])
		})
	}
}

// 批任务的自定义元数据区分大小写；其他协议的元数据保留固定字段校验。
func TestGatewayBatchMetadataKeysAndContentType(t *testing.T) {
	for _, tc := range []struct {
		path, body string
		valid      bool
	}{
		{"/v1/images/batches", `{"metadata":{"traceId":"a","traceID":"b"}}`, true},
		{"/v1/images/batches", `{"metadata":{"traceId":"a","traceId":"b"}}`, false},
		{"/v1/messages", `{"metadata":{"user_id":"a","User_ID":"b"}}`, false},
		{"/v1/responses", `{"model":"display-alias"}`, true},
	} {
		req := httptest.NewRequest("POST", tc.path, bytes.NewBufferString(tc.body))
		req.Header.Set("Content-Type", "multipart/form-data; boundary=unused")
		err := PrepareGatewayRequestBody(req, 0)
		if tc.valid {
			require.NoError(t, err)
			require.Equal(t, "application/json", req.Header.Get("Content-Type"))
		} else {
			require.ErrorIs(t, err, jsonutil.ErrDuplicateField)
		}
	}
}
