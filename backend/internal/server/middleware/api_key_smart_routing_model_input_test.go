package middleware

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSmartRoutingJSONModelInputIsUnambiguous(t *testing.T) {
	for _, test := range []struct {
		name, body, model, reason string
	}{
		{"合法模型保留请求体", `{"model":" image-model ","tools":[{"model":"tool-model"}]}`, "image-model", ""},
		{"重复模型不同值", `{"model":"first","model":"second"}`, "", "SMART_ROUTING_INVALID_REQUEST"},
		{"重复模型相同值", `{"model":"first","model":"first"}`, "", "SMART_ROUTING_INVALID_REQUEST"},
		{"后续空值不能触发默认模型", `{"model":"first","model":""}`, "", "SMART_ROUTING_INVALID_REQUEST"},
		{"重复转义模型键", `{"model":"first","\u006dodel":"second"}`, "", "SMART_ROUTING_INVALID_REQUEST"},
		{"标准绑定大小写别名", `{"model":"first","Model":"second"}`, "", "SMART_ROUTING_INVALID_REQUEST"},
		{"非规范大小写名称", `{"Model":"first"}`, "", "SMART_ROUTING_INVALID_REQUEST"},
		{"空白模型", `{"model":" "}`, "", "SMART_ROUTING_MODEL_REQUIRED"},
		{"缺失模型", `{"messages":[]}`, "", "SMART_ROUTING_MODEL_REQUIRED"},
		{"非字符串模型", `{"model":42}`, "", "SMART_ROUTING_MODEL_REQUIRED"},
		{"空值模型", `{"model":null}`, "", "SMART_ROUTING_MODEL_REQUIRED"},
		{"非对象请求", `[{"model":"first"}]`, "", "SMART_ROUTING_INVALID_REQUEST"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(test.body))
			model, err := smartRoutingRequestModel(c)
			if test.reason == "" {
				require.NoError(t, err)
			} else {
				require.Equal(t, test.reason, infraerrors.Reason(err))
			}
			require.Equal(t, test.model, model)
			restored, readErr := io.ReadAll(c.Request.Body)
			require.NoError(t, readErr)
			require.Equal(t, test.body, string(restored))
		})
	}
}

// 表单字段按真实顺序构造，覆盖认证与媒体处理器对重复 model 的解析差异。
func TestSmartRoutingMultipartModelInputIsUnambiguous(t *testing.T) {
	type field struct {
		name, value string
		file        bool
	}
	for _, test := range []struct {
		name          string
		fields        []field
		model, reason string
	}{
		{"模型字段可位于文件后", []field{{"image", "image-data", true}, {"model", " image-model ", false}}, "image-model", ""},
		{"重复模型", []field{{"model", "first", false}, {"model", "second", false}}, "", "SMART_ROUTING_INVALID_REQUEST"},
		{"重复模型为空", []field{{"model", "first", false}, {"model", " ", false}}, "", "SMART_ROUTING_INVALID_REQUEST"},
		{"文件不能充当模型", []field{{"model", "first", true}}, "", "SMART_ROUTING_INVALID_REQUEST"},
		{"文件模型不能掩盖正常字段", []field{{"model", "first", true}, {"model", "second", false}}, "", "SMART_ROUTING_INVALID_REQUEST"},
		{"媒体处理器裁剪名称", []field{{"model", "first", false}, {" model ", "second", false}}, "", "SMART_ROUTING_INVALID_REQUEST"},
		{"空模型", []field{{"model", " ", false}}, "", "SMART_ROUTING_MODEL_REQUIRED"},
		{"缺失模型", []field{{"image", "image-data", true}}, "", "SMART_ROUTING_MODEL_REQUIRED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			for _, item := range test.fields {
				if item.file {
					part, err := writer.CreateFormFile(item.name, "input.png")
					require.NoError(t, err)
					_, err = io.WriteString(part, item.value)
					require.NoError(t, err)
				} else {
					require.NoError(t, writer.WriteField(item.name, item.value))
				}
			}
			require.NoError(t, writer.Close())
			original := append([]byte(nil), body.Bytes()...)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
			c.Request.Header.Set("Content-Type", writer.FormDataContentType())
			model, err := smartRoutingRequestModel(c)
			if test.reason == "" {
				require.NoError(t, err)
			} else {
				require.Equal(t, test.reason, infraerrors.Reason(err))
			}
			require.Equal(t, test.model, model)
			restored, readErr := io.ReadAll(c.Request.Body)
			require.NoError(t, readErr)
			require.Equal(t, original, restored)
		})
	}
}

type smartRoutingInputErrorReader struct{ err error }

func (r smartRoutingInputErrorReader) Read([]byte) (int, error) { return 0, r.err }
func (r smartRoutingInputErrorReader) Close() error             { return nil }

// 严格模型读取仍保留入口请求体限制和取消语义，不能退化为模型目录未命中。
func TestSmartRoutingModelInputPreservesBodyErrors(t *testing.T) {
	for _, contentType := range []string{"application/json", "multipart/form-data; boundary=test"} {
		for _, inputErr := range []error{context.Canceled, context.DeadlineExceeded} {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", nil)
			c.Request.Header.Set("Content-Type", contentType)
			c.Request.Body = smartRoutingInputErrorReader{err: inputErr}
			_, err := smartRoutingRequestModel(c)
			require.Equal(t, "SMART_ROUTING_REQUEST_CANCELED", infraerrors.Reason(err))
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader(`{"model":"image-model"}`))
		c.Request.Header.Set("Content-Type", contentType)
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 5)
		_, err := smartRoutingRequestModel(c)
		require.Equal(t, http.StatusRequestEntityTooLarge, infraerrors.Code(err))
		require.Equal(t, "REQUEST_BODY_TOO_LARGE", infraerrors.Reason(err))
	}
}
