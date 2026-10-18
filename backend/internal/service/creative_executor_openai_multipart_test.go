//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 使用标准 multipart 解析器检查实际线格式，避免只匹配字段名漏掉文件媒体类型。
func TestCreativeOpenAIMultipartPreservesImageTypesAndParameters(t *testing.T) {
	for _, operation := range []string{CreativeOperationEdit, CreativeOperationInpaint} {
		t.Run(operation, func(t *testing.T) {
			run := CreativeRun{Operation: operation, ImageSize: "1K", AspectRatio: "1:1"}
			payload := CreativeRunPayload{Prompt: "test edit", Quality: "medium", Background: "auto", Sources: []CreativeInputImage{
				{Mime: "image/png", Bytes: []byte("png-original")},
				{Mime: "image/jpeg", Bytes: []byte("jpeg-original")},
				{Mime: "image/webp", Bytes: []byte("webp-original")},
			}}
			if operation == CreativeOperationInpaint {
				payload.Mask = &CreativeInputImage{Mime: "image/png", Bytes: []byte("mask-original")}
			}
			body, contentType, err := buildCreativeOpenAIRequestBody(run, payload, "gpt-image-2")
			require.NoError(t, err)
			mediaType, params, err := mime.ParseMediaType(contentType)
			require.NoError(t, err)
			require.Equal(t, "multipart/form-data", mediaType)
			reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
			fields := map[string]string{}
			imageIndex, maskCount := 0, 0
			for {
				part, readErr := reader.NextPart()
				if readErr == io.EOF {
					break
				}
				require.NoError(t, readErr)
				data, readErr := io.ReadAll(part)
				require.NoError(t, readErr)
				switch part.FormName() {
				case "image":
					require.Less(t, imageIndex, len(payload.Sources))
					source := payload.Sources[imageIndex]
					require.Equal(t, source.Mime, part.Header.Get("Content-Type"))
					require.Equal(t, source.Bytes, data)
					require.NotEmpty(t, part.FileName())
					imageIndex++
				case "mask":
					require.NotNil(t, payload.Mask)
					require.Equal(t, "image/png", part.Header.Get("Content-Type"))
					require.Equal(t, payload.Mask.Bytes, data)
					maskCount++
				default:
					require.Empty(t, part.FileName())
					fields[part.FormName()] = string(data)
				}
				require.NoError(t, part.Close())
			}
			require.Equal(t, len(payload.Sources), imageIndex)
			if payload.Mask != nil {
				require.Equal(t, 1, maskCount)
			} else {
				require.Zero(t, maskCount)
			}
			require.Equal(t, map[string]string{"model": "gpt-image-2", "prompt": "test edit", "size": "1024x1024", "n": "1", "output_format": "png", "quality": "medium", "background": "auto"}, fields)
		})
	}
}

// 不可信或损坏的媒体头不能写入出站 multipart，也不能借换行注入新的头。
func TestCreativeOpenAIMultipartRejectsInvalidImageType(t *testing.T) {
	for _, mediaType := range []string{"", "application/octet-stream", "image/svg+xml", "image/png\r\nX-Injected: true"} {
		t.Run(mediaType, func(t *testing.T) {
			body, _, err := buildCreativeOpenAIRequestBody(CreativeRun{Operation: CreativeOperationEdit}, CreativeRunPayload{Sources: []CreativeInputImage{{Mime: mediaType, Bytes: []byte("image")}}}, "gpt-image-2")
			require.Error(t, err)
			require.Nil(t, body)
			require.False(t, IsRetryableCreativeError(err))
		})
	}
}

// 上游HTTP错误与本地解析错误必须可区分，诊断事件不得带出原包中的提示词、凭据或素材地址。
func TestCreativeOpenAIImageFailureDiagnosticsRemainSafe(t *testing.T) {
	for _, tc := range []struct {
		name, body, stage string
		status            int
	}{
		{"upstream_http", `{"error":{"message":"secret-prompt https://private.example/image sk-private"}}`, "http_status", 502},
		{"url_only", `{"data":[{"url":"https://private.example/image"}],"prompt":"secret-prompt","key":"sk-private"}`, "response_parse", 200},
		{"no_image", `{"data":[],"prompt":"secret-prompt","key":"sk-private"}`, "response_parse", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink, cleanup := captureStructuredLog(t)
			defer cleanup()
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}}
			executor := &CreativeExecutor{gateway: &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}}
			account := &Account{ID: 41, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"api_key": "test-key"}}
			_, err := executor.executeOpenAI(context.Background(), CreativeRun{RunID: "safe-run", Operation: CreativeOperationGenerate, ImageSize: "1K", AspectRatio: "1:1"}, CreativeRunPayload{Prompt: "secret-prompt"}, account, "gpt-image-2")
			require.Error(t, err)
			require.Len(t, upstream.requests, 1)
			require.True(t, sink.ContainsMessage("creative.openai_image_failed"))
			require.True(t, sink.ContainsFieldValue("stage", tc.stage))
			sink.mu.Lock()
			raw, marshalErr := json.Marshal(sink.events)
			sink.mu.Unlock()
			require.NoError(t, marshalErr)
			require.NotContains(t, string(raw), "secret-prompt")
			require.NotContains(t, string(raw), "private.example")
			require.NotContains(t, string(raw), "sk-private")
		})
	}
}
