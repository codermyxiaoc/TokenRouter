//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 已完成图片后的终态失败或连接中断不能丢弃图片，否则 worker 会重复生成并漏结算首张。
func TestCreativeCodexResponsesCompletedImageSurvivesLaterFailure(t *testing.T) {
	image := encodeOpenAIImageTestPNG(t, 2, 2)
	expected, err := base64.StdEncoding.DecodeString(image)
	require.NoError(t, err)
	complete := fmt.Sprintf("data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"img_done\",\"type\":\"image_generation_call\",\"result\":%q}}\n\n", image)
	for _, failure := range []string{"failed", "incomplete", "read_interrupted"} {
		t.Run(failure, func(t *testing.T) {
			var paths []string
			upstream := &directImagesHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				paths = append(paths, req.URL.Path)
				if len(paths) == 1 {
					return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"endpoint not found"}}`))}, nil
				}
				var tail io.Reader
				switch failure {
				case "failed":
					tail = strings.NewReader("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"type\":\"server_error\",\"message\":\"private upstream diagnostic\"}}}\n\n")
				case "incomplete":
					tail = strings.NewReader("data: {\"type\":\"response.incomplete\",\"response\":{\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n")
				default:
					tail = &openAIImagesReadErrorBody{err: io.ErrUnexpectedEOF}
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(io.MultiReader(strings.NewReader(complete), tail))}, nil
			}}
			executor := &CreativeExecutor{gateway: newOpenAIImagesTestService(upstream)}
			account := directImagesTestAccount()
			result, err := executor.Execute(context.Background(), CreativeRun{ID: 1, RunID: "crun_recovered", APIKeyID: 42, Operation: CreativeOperationGenerate}, CreativeRunPayload{Prompt: "private prompt"}, &CreativeExecution{Account: account, UpstreamModel: "gpt-image-2"})
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, account.ID, result.AccountID)
			require.Len(t, result.Outputs, 1, "完整产出进入现有单张保存与结算流程")
			require.Equal(t, expected, result.Outputs[0].Bytes)
			require.Equal(t, []string{"/backend-api/codex/images/generations", "/backend-api/codex/responses"}, paths)
		})
	}
}

// 没有完整产出时仍保留原错误分类；预览片段不是可结算的最终图片。
func TestCreativeCodexResponsesNoCompleteImageKeepsFailurePolicy(t *testing.T) {
	for _, readInterrupted := range []bool{false, true} {
		t.Run(fmt.Sprint(readInterrupted), func(t *testing.T) {
			calls := 0
			upstream := &directImagesHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				calls++
				if calls == 1 {
					return &http.Response{StatusCode: 405, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
				}
				var tail io.Reader = strings.NewReader("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"type\":\"server_error\"}}}\n\n")
				if readInterrupted {
					tail = &openAIImagesReadErrorBody{err: io.ErrUnexpectedEOF}
				}
				preview := "data: {\"type\":\"response.image_generation_call.partial_image\",\"partial_image_b64\":\"AA==\"}\n\n"
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(io.MultiReader(strings.NewReader(preview), tail))}, nil
			}}
			executor := &CreativeExecutor{gateway: newOpenAIImagesTestService(upstream)}
			result, err := executor.Execute(context.Background(), CreativeRun{ID: 1, Operation: CreativeOperationGenerate}, CreativeRunPayload{Prompt: "draw"}, &CreativeExecution{Account: directImagesTestAccount(), UpstreamModel: "gpt-image-2"})
			require.Error(t, err)
			require.Nil(t, result)
			require.Equal(t, !readInterrupted, executor.IsRetryable(err), "明确上游拒绝有限重试，传输结果未知不重放")
			require.Equal(t, 2, calls)
		})
	}
}
