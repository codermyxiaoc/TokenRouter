//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/gif"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 完整 GIF 含两帧，截断夹具保留第一帧但丢失后续动画。
func regressionTruncatedGIF(t *testing.T) []byte {
	t.Helper()
	data := creativeTestGIF(t)
	frames, err := gif.DecodeAll(bytes.NewReader(data))
	require.NoError(t, err)
	require.Len(t, frames.Image, 2)
	for cut := 1; cut < len(data); cut++ {
		partial := data[:cut]
		_, firstErr := gif.Decode(bytes.NewReader(partial))
		_, allErr := gif.DecodeAll(bytes.NewReader(partial))
		if firstErr == nil && allErr != nil {
			t.Logf("GIF截断夹具保留%d/%d字节；后续帧解码错误=%v", cut, len(data), allErr)
			return partial
		}
	}
	t.Fatal("无法构造后续帧被截断的GIF")
	return nil
}

// 截断动图不能被三个交付边界误判为完整成品。
func TestRegressionTruncatedGIFRejectedAtDeliveryBoundaries(t *testing.T) {
	partial := regressionTruncatedGIF(t)
	t.Run("creative_validator", func(t *testing.T) {
		_, ok := validatedCreativeImageOutput(partial, 0)
		require.False(t, ok, "创作台完整性校验不能只解码GIF第一帧")
	})
	t.Run("canvas_storage_validator", func(t *testing.T) {
		require.False(t, isDecodableStoredImage(partial), "画布归档校验不能只解码GIF第一帧")
	})
	t.Run("creative_response_parser", func(t *testing.T) {
		body := []byte(fmt.Sprintf(`{"data":[{"b64_json":%q}]}`, base64.StdEncoding.EncodeToString(partial)))
		out, err := parseCreativeOpenAIImageOutputs(context.Background(), body, NewImageResultUploader(nil, "", 1<<20, nil))
		require.Error(t, err, "损坏动图应转入未交付失败释放流程")
		require.Empty(t, out)
	})
}

// 两个2.6模型、两种账号、三种创作操作均须原样保留GIF成品且只提交一次。
func TestRegressionGIFCreativeGenerationEditAndInpaint(t *testing.T) {
	data := creativeTestGIF(t)
	png64 := encodeOpenAIImageTestPNG(t, 4, 4)
	pngBytes, err := base64.StdEncoding.DecodeString(png64)
	require.NoError(t, err)
	for _, model := range []string{"gpt-image-2.6-flare", "gpt-image-2.6-sunburst"} {
		for _, oauth := range []bool{false, true} {
			for _, operation := range []string{CreativeOperationGenerate, CreativeOperationEdit, CreativeOperationInpaint} {
				t.Run(fmt.Sprintf("%s/oauth=%t/%s", model, oauth, operation), func(t *testing.T) {
					body := fmt.Sprintf(`{"output_format":"gif","data":[{"b64_json":%q}]}`, base64.StdEncoding.EncodeToString(data))
					upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}}
					executor := &CreativeExecutor{gateway: newOpenAIImagesTestService(upstream)}
					account := newOpenAIImagesAPIKeyAccount()
					if oauth {
						account = directImagesTestAccount()
					}
					payload := CreativeRunPayload{Prompt: "draw a moving square", OutputFormat: "gif"}
					if operation != CreativeOperationGenerate {
						payload.Sources = []CreativeInputImage{{Bytes: pngBytes, Mime: "image/png"}}
					}
					if operation == CreativeOperationInpaint {
						payload.Mask = &CreativeInputImage{Bytes: pngBytes, Mime: "image/png"}
					}
					result, err := executor.Execute(context.Background(), CreativeRun{ID: 1, APIKeyID: 42, Operation: operation, ImageSize: "1K", RequestedOutputCount: 1}, payload, &CreativeExecution{Account: account, UpstreamModel: model})
					require.NoError(t, err)
					require.Len(t, result.Outputs, 1)
					require.Equal(t, data, result.Outputs[0].Bytes)
					require.Equal(t, "image/gif", result.Outputs[0].Mime)
					require.Len(t, upstream.requests, 1)
					if oauth || operation == CreativeOperationGenerate {
						require.Equal(t, "gif", gjson.GetBytes(upstream.lastBody, "output_format").String())
						require.False(t, gjson.GetBytes(upstream.lastBody, "response_format").Exists())
					} else {
						require.Contains(t, string(upstream.lastBody), "name=\"output_format\"\r\n\r\ngif")
						require.NotContains(t, string(upstream.lastBody), "name=\"response_format\"")
					}
				})
			}
		}
	}
}

// 管理员测试在账号映射后仍必须省略GPT不支持的response_format，并展示URL成品。
func TestRegressionAdminImage26MappedModelAndURL(t *testing.T) {
	for _, model := range []string{"gpt-image-2.6-flare", "gpt-image-2.6-sunburst"} {
		t.Run(model, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":[{"url":"https://cdn.example.test/result.png"}]}`))}}
			svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{}}
			account := &Account{ID: 54, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "local-test", "model_mapping": map[string]any{"draw-alias": model}}}
			err := svc.testOpenAIAccountConnection(c, account, "draw-alias", "draw a square", "", AccountTestTypeImage)
			require.NoError(t, err)
			require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
			require.False(t, gjson.GetBytes(upstream.lastBody, "response_format").Exists())
			require.Contains(t, rec.Body.String(), "https://cdn.example.test/result.png")
			require.Contains(t, rec.Body.String(), `"success":true`)
		})
	}
}

// 当前修改函数的空结果边界：不能仅因data数组非空就报告账号生图成功。
func TestRegressionAdminImageEmptyResultMustFail(t *testing.T) {
	for _, body := range []string{`{"data":[{}]}`, `{"data":[{"url":"  "}]}`, `{"data":[{"revised_prompt":"still no output"}]}`} {
		t.Run(body, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}}
			svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{}}
			account := &Account{ID: 54, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "local-test"}}
			err := svc.testOpenAIImageAPIKey(c, context.Background(), account, "gpt-image-2.6-flare", "draw a square")
			require.Error(t, err, "没有任何base64或URL结果时管理员测试必须失败")
			require.NotContains(t, rec.Body.String(), `"success":true`)
		})
	}
}

// GIF应保留所有帧和正确扩展名，不转为静态PNG。
func TestRegressionGIFObjectStoragePreservesAnimation(t *testing.T) {
	data := creativeTestGIF(t)
	storage := &fakeImageStorage{}
	uploader := NewImageResultUploader(storage, "images/", 1<<20, nil)
	result := json.RawMessage(fmt.Sprintf(`{"output_format":"gif","data":[{"b64_json":%q}]}`, base64.StdEncoding.EncodeToString(data)))
	_, err := uploader.Rewrite(context.Background(), "imgtask_gif_post339", result)
	require.NoError(t, err)
	require.Len(t, storage.saved, 1)
	require.Equal(t, "image/gif", storage.saved[0].contentType)
	require.True(t, strings.HasSuffix(storage.saved[0].key, ".gif"))
	frames, err := gif.DecodeAll(bytes.NewReader(storage.saved[0].data))
	require.NoError(t, err)
	require.Len(t, frames.Image, 2)
}
