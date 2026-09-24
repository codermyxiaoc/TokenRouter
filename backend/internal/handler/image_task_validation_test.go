package handler

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	middleware2 "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestValidateAsyncImagePromptJSON 覆盖本轮空请求问题及 JSON 类型边界，扩展字段仍保持原样。
func TestValidateAsyncImagePromptJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		err  string
	}{
		{"missing", `{}`, "prompt is required"},
		{"null", `{"prompt":null}`, "prompt is required"},
		{"empty", `{"prompt":""}`, "prompt must not be empty"},
		{"whitespace", `{"prompt":" \r\n\t\u2003 "}`, "prompt must not be empty"},
		{"number", `{"prompt":42}`, "prompt must be a string"},
		{"boolean", `{"prompt":true}`, "prompt must be a string"},
		{"array", `{"prompt":["cat"]}`, "prompt must be a string"},
		{"object", `{"prompt":{"text":"cat"}}`, "prompt must be a string"},
		{"root_array", `[{"prompt":"cat"}]`, "valid JSON object"},
		{"root_null", `null`, "valid JSON object"},
		{"malformed", `{"prompt":"cat"`, "valid JSON object"},
		{"trailing_json", `{"prompt":"cat"}{}`, "valid JSON object"},
		{"generation", `{"prompt":"cat","size":"3840x2160","n":1}`, ""},
		{"unicode", `{"prompt":"  画一只猫\n使用水彩。  "}`, ""},
		{"json_edit", `{"prompt":"remove background","images":[{"image_url":"data:image/png;base64,abc"}],"mask":{"image_url":"https://example.test/mask.png"}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			err := validateAsyncImagePrompt("application/json; charset=utf-8", body)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.body, string(body), "校验不能改写客户端正文")
		})
	}
	// 与现有图片解析器保持一致，未声明 Content-Type 的有效 JSON 仍可使用。
	require.NoError(t, validateAsyncImagePrompt("", []byte(`{"prompt":"cat"}`)))
}

// TestValidateAsyncImagePromptMultipart 确认编辑图文件不会被误当成提示词，也不会被修改或保存到临时文件。
func TestValidateAsyncImagePromptMultipart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prompts []string
		file    bool
		err     string
	}{
		{"missing", nil, false, "prompt is required"},
		{"file_is_not_prompt", nil, true, "prompt is required"},
		{"empty", []string{""}, false, "prompt must not be empty"},
		{"whitespace", []string{" \r\n\t\u2003 "}, false, "prompt must not be empty"},
		{"valid", []string{" 修改背景 "}, false, ""},
		{"file_after_valid", []string{"修改背景"}, true, ""},
		{"duplicate_last_empty", []string{"cat", ""}, false, "prompt must not be empty"},
		{"duplicate_last_valid", []string{"", "cat"}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			require.NoError(t, writer.WriteField("model", "gpt-image-2"))
			file, err := writer.CreateFormFile("image[]", "input.png")
			require.NoError(t, err)
			_, err = file.Write(bytes.Repeat([]byte{0, 1, 2, 3}, 1024))
			require.NoError(t, err)
			for _, prompt := range tc.prompts {
				require.NoError(t, writer.WriteField("prompt", prompt))
			}
			if tc.file {
				file, err = writer.CreateFormFile("prompt", "prompt.txt")
				require.NoError(t, err)
				_, err = file.Write([]byte("cat"))
				require.NoError(t, err)
			}
			require.NoError(t, writer.Close())
			original := append([]byte(nil), body.Bytes()...)
			err = validateAsyncImagePrompt(writer.FormDataContentType(), body.Bytes())
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, original, body.Bytes())
		})
	}
}

// TestValidateAsyncImagePromptRejectsMalformedMultipart 防止截断上传或无边界请求先得到可轮询的任务。
func TestValidateAsyncImagePromptRejectsMalformedMultipart(t *testing.T) {
	require.ErrorContains(t, validateAsyncImagePrompt("multipart/form-data", []byte("bad")), "boundary")
	require.ErrorContains(t, validateAsyncImagePrompt("multipart/form-data; boundary=", []byte("bad")), "boundary")
	body := "--boundary\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\ncat"
	require.ErrorContains(t, validateAsyncImagePrompt("multipart/form-data; boundary=boundary", []byte(body)), "read prompt")
	body += "\r\n--boundary\r\nContent-Disposition: form-data; name=\"image\"; filename=\"image.png\"\r\n\r\n" + strings.Repeat("x", 4096)
	require.ErrorContains(t, validateAsyncImagePrompt("multipart/form-data; boundary=boundary", []byte(body)), "multipart request")
}

// TestAsyncImageInvalidPromptDoesNotCreateTask 验证无效输入在返回 202 之前失败，不占用任务或调用上游。
func TestAsyncImageInvalidPromptDoesNotCreateTask(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, platform := range []string{service.PlatformOpenAI, service.PlatformGrok} {
		for _, endpoint := range []string{"generations", "edits"} {
			for _, body := range []string{`{}`, `{"prompt":123}`, `{"prompt":"  "}`} {
				t.Run(platform+"/"+endpoint+"/"+body, func(t *testing.T) {
					store := &asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}
					tasks := service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Minute)
					h := NewAsyncImageHandler(tasks, nil)
					var executions atomic.Int32
					h.SetGatewayExecutor(func(_ string, c *gin.Context) {
						executions.Add(1)
						c.JSON(http.StatusBadGateway, gin.H{"error": "不应执行无效请求"})
					})
					router := gin.New()
					router.Use(func(c *gin.Context) {
						c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
							ID: 9, UserID: 7,
							Group: &service.Group{ID: 3, Platform: platform, AllowImageGeneration: true},
						})
						c.Next()
					})
					path := "/v1/images/" + endpoint + "/async"
					router.POST(path, h.Submit)
					request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
					request.Header.Set("Content-Type", "application/json")
					response := httptest.NewRecorder()
					router.ServeHTTP(response, request)
					require.Equal(t, http.StatusBadRequest, response.Code)
					require.Contains(t, response.Body.String(), "prompt")
					require.Empty(t, response.Header().Get("Location"))
					require.Zero(t, executions.Load())
					store.mu.RLock()
					require.Empty(t, store.tasks)
					store.mu.RUnlock()
					require.Empty(t, h.pending, "校验失败必须释放受理名额")
				})
			}
		}
	}
}
