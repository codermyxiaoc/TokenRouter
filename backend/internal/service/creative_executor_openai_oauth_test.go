//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 创作台三种操作都复用原生字段白名单，mask 只用于局部重绘，保持固定 PNG 单张输出。
func TestCreativeCodexDirectImagesOperations(t *testing.T) {
	png64 := encodeOpenAIImageTestPNG(t, 2, 2)
	png, err := base64.StdEncoding.DecodeString(png64)
	require.NoError(t, err)
	for _, operation := range []string{CreativeOperationGenerate, CreativeOperationEdit, CreativeOperationInpaint} {
		t.Run(operation, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"data":[{"b64_json":%q}]}`, png64)))}}
			executor := &CreativeExecutor{gateway: newOpenAIImagesTestService(upstream)}
			payload := CreativeRunPayload{Prompt: "保持提示词", Quality: "high", Background: "opaque"}
			if operation != CreativeOperationGenerate {
				payload.Sources = []CreativeInputImage{{Bytes: png, Mime: "image/png"}}
			}
			if operation == CreativeOperationInpaint {
				payload.Mask = &CreativeInputImage{Bytes: png, Mime: "image/png"}
			}
			result, err := executor.Execute(context.Background(), CreativeRun{ID: 1, APIKeyID: 42, Operation: operation, ImageSize: "1K", RequestedOutputCount: 9}, payload,
				&CreativeExecution{Account: directImagesTestAccount(), UpstreamModel: "gpt-image-2"})
			require.NoError(t, err)
			require.Len(t, result.Outputs, 1)
			require.Equal(t, png, result.Outputs[0].Bytes)
			require.Equal(t, "application/json", upstream.lastReq.Header.Get("Content-Type"))
			require.Equal(t, "Bearer test-token", upstream.lastReq.Header.Get("Authorization"))
			require.Empty(t, upstream.lastReq.Header.Get("OpenAI-Beta"))
			require.Equal(t, "png", gjson.GetBytes(upstream.lastBody, "output_format").String())
			require.False(t, gjson.GetBytes(upstream.lastBody, "n").Exists(), "原生单图省略 n")
			if operation == CreativeOperationGenerate {
				require.Equal(t, "/backend-api/codex/images/generations", upstream.lastReq.URL.Path)
				require.False(t, gjson.GetBytes(upstream.lastBody, "images").Exists())
			} else {
				require.Equal(t, "/backend-api/codex/images/edits", upstream.lastReq.URL.Path)
				require.Len(t, gjson.GetBytes(upstream.lastBody, "images").Array(), 1)
			}
			require.Equal(t, operation == CreativeOperationInpaint, gjson.GetBytes(upstream.lastBody, "mask.image_url").Exists())
		})
	}
}

// 原生 OAuth 网络失败禁止队列再次生成；API Key 的历史重试策略保持不变。
func TestCreativeCodexDirectImagesNetworkFailureNotRetried(t *testing.T) {
	for _, oauth := range []bool{true, false} {
		calls := 0
		upstream := &directImagesHTTPUpstreamStub{do: func(*http.Request, string, int64, int) (*http.Response, error) {
			calls++
			return nil, errors.New("connection reset")
		}}
		executor := &CreativeExecutor{gateway: newOpenAIImagesTestService(upstream)}
		account := directImagesTestAccount()
		if !oauth {
			account = newOpenAIImagesAPIKeyAccount()
		}
		_, err := executor.Execute(context.Background(), CreativeRun{ID: 1, Operation: CreativeOperationGenerate}, CreativeRunPayload{Prompt: "draw"}, &CreativeExecution{Account: account, UpstreamModel: "gpt-image-2"})
		require.Error(t, err)
		require.Equal(t, !oauth, executor.IsRetryable(err))
		require.Equal(t, 1, calls)
	}
}

// 只有端点明确不可用才回退一次；后台队列不能把接收失败当成重新生成的理由。
func TestCreativeCodexDirectImagesFallbackBoundary(t *testing.T) {
	for _, status := range []int{404, 405, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var paths []string
			upstream := &directImagesHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				paths = append(paths, req.URL.Path)
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"unavailable"}}`))}, nil
			}}
			executor := &CreativeExecutor{gateway: newOpenAIImagesTestService(upstream)}
			_, err := executor.Execute(context.Background(), CreativeRun{ID: 1, Operation: CreativeOperationGenerate}, CreativeRunPayload{Prompt: "draw"}, &CreativeExecution{Account: directImagesTestAccount(), UpstreamModel: "gpt-image-2"})
			require.Error(t, err)
			if status == 404 || status == 405 {
				require.Equal(t, []string{"/backend-api/codex/images/generations", "/backend-api/codex/responses"}, paths)
				require.False(t, executor.IsRetryable(err))
			} else {
				require.Equal(t, []string{"/backend-api/codex/images/generations"}, paths)
				require.True(t, executor.IsRetryable(err), "明确限流/服务拒绝保留队列原有有限重试")
			}
		})
	}
}
