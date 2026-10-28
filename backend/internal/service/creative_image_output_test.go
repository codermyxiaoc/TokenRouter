//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 下载只 GET 同一产物；无效 base64 可回退 URL，正常 base64 不额外访问地址。
func TestCreativeImageOutputURLAndInlineCompatibility(t *testing.T) {
	img := creativeMJTestPNG(t, 31)
	getCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		getCount++
		require.Equal(t, http.MethodGet, r.Method)
		require.Empty(t, r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get("Cookie"))
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(img)
	}))
	defer server.Close()
	downloader := NewImageResultUploader(nil, "", creativeMaxOutputBytes, server.Client())
	encoded := base64.StdEncoding.EncodeToString(img)
	for _, tc := range []struct {
		name string
		item map[string]any
		gets int
	}{
		{"url", map[string]any{"url": server.URL + "/signed-image?signature=fake"}, 1},
		{"invalid_base64_url_fallback", map[string]any{"b64_json": "!invalid!", "url": server.URL}, 1},
		{"non_image_base64_url_fallback", map[string]any{"b64_json": "eA==", "url": server.URL}, 1},
		{"inline_preferred", map[string]any{"b64_json": encoded, "url": server.URL}, 0},
		{"unpadded_base64", map[string]any{"b64_json": strings.TrimRight(encoded, "=")}, 0},
		{"inline_data_uri", map[string]any{"b64_json": "data:image/png;base64," + encoded}, 0},
		{"url_data_uri", map[string]any{"url": "data:image/png;base64," + encoded}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := getCount
			body, err := json.Marshal(map[string]any{"data": []any{tc.item, map[string]any{"url": server.URL}}})
			require.NoError(t, err)
			outputs, err := parseCreativeOpenAIImageOutputs(context.Background(), body, downloader)
			require.NoError(t, err)
			require.Len(t, outputs, 1, "普通模型不能因多项响应突破单张预占合同")
			require.Equal(t, img, outputs[0].Bytes)
			require.Equal(t, "image/png", outputs[0].Mime)
			require.Equal(t, tc.gets, getCount-before)
		})
	}
}

// 生产下载器拒绝私网、身份信息和非 HTTP 地址，不把地址或响应内容泄漏到错误。
func TestCreativeImageOutputRejectsUnsafeOrInvalidResults(t *testing.T) {
	for _, body := range []string{
		`{`, `{"data":[]}`, `{"data":{}}`, `{"error":{"message":"upstream failed"}}`,
		`{"data":[{"b64_json":123}]}`, `{"data":[{"b64_json":"eA=="}]}`,
		`{"data":[{"url":"http://127.0.0.1/private"}]}`,
		`{"data":[{"url":"http://169.254.169.254/latest/meta-data/"}]}`,
		`{"data":[{"url":"file:///private-file"}]}`,
		`{"data":[{"url":"https://user:secret@example.com/image.png"}]}`,
		`{"data":[{"url":"data:image/svg+xml;base64,PHN2Zy8+"}]}`,
		`{"data":[{"url":"data:text/html;base64,PHN2Zy8+"}]}`,
	} {
		_, err := parseCreativeOpenAIImageOutputs(context.Background(), []byte(body), creativeOutputImageDownloader)
		require.ErrorAs(t, err, new(*CreativeProviderResultUnconfirmedError))
		require.False(t, IsRetryableCreativeError(err))
		require.NotContains(t, err.Error(), "secret")
		require.NotContains(t, err.Error(), "private")
	}
	img := creativeMJTestPNG(t, 32)
	for _, data := range [][]byte{img[:40], append([]byte(nil), img...)} {
		if len(data) == len(img) {
			binary.BigEndian.PutUint32(data[16:20], 8193)
			binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
		}
		body, err := json.Marshal(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(data)}}})
		require.NoError(t, err)
		_, err = parseCreativeOpenAIImageOutputs(context.Background(), body, creativeOutputImageDownloader)
		require.ErrorAs(t, err, new(*CreativeProviderResultUnconfirmedError))
	}
	// 通过较小测试预算验证限长路径，无需分配或下载大图片。
	body, _ := json.Marshal(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(img)}}})
	_, err := parseCreativeOpenAIImageOutputs(context.Background(), body, NewImageResultUploader(nil, "", 8, nil))
	require.ErrorAs(t, err, new(*CreativeProviderResultUnconfirmedError))
}

// 执行器仅提交一次生成，下载暂时失败只能重试下载；两种平台均走真实解析入口。
func TestCreativeImageURLExecutorsNeverResubmitGeneration(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformGrok} {
		for _, outcome := range []string{"success", "download_retries", "not_found", "invalid_image"} {
			t.Run(platform+"/"+outcome, func(t *testing.T) {
				img := creativeMJTestPNG(t, 33)
				gets := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					gets++
					require.Equal(t, http.MethodGet, r.Method)
					require.Empty(t, r.Header.Get("Authorization"))
					require.Empty(t, r.Header.Get("Cookie"))
					if outcome == "download_retries" && gets < 3 {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					if outcome == "not_found" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					w.Header().Set("Content-Type", "image/png")
					if outcome == "invalid_image" {
						_, _ = w.Write([]byte("<html>not an image</html>"))
						return
					}
					_, _ = w.Write(img)
				}))
				defer server.Close()
				body, _ := json.Marshal(map[string]any{"data": []any{map[string]string{"url": server.URL + "/result.png"}}})
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}}
				cfg := &config.Config{}
				executor := &CreativeExecutor{cfg: cfg, gateway: &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}, imageDownloader: NewImageResultUploader(nil, "", creativeMaxOutputBytes, server.Client())}
				account := &Account{ID: 42, Type: AccountTypeAPIKey, Platform: platform, Credentials: map[string]any{"api_key": "test-secret-never-download"}}
				model := "gpt-image-2.5"
				if platform == PlatformGrok {
					model = "grok-imagine-image-2.0"
				}
				result, err := executor.Execute(context.Background(), CreativeRun{RunID: "crun_url_test", Operation: CreativeOperationGenerate, RequestedOutputCount: 1, ImageSize: "1K"}, CreativeRunPayload{Prompt: "test"}, &CreativeExecution{Account: account, UpstreamModel: model})
				require.Len(t, upstream.requests, 1, "下载失败不能重新调用付费生成")
				if outcome == "success" || outcome == "download_retries" {
					require.NoError(t, err)
					require.Len(t, result.Outputs, 1)
					require.Equal(t, img, result.Outputs[0].Bytes)
				} else {
					require.ErrorAs(t, err, new(*CreativeProviderResultUnconfirmedError))
					require.False(t, IsRetryableCreativeError(err))
				}
				if outcome == "download_retries" {
					require.Equal(t, 3, gets)
				} else {
					require.Equal(t, 1, gets)
				}
			})
		}
	}
}

// 普通模型同样必须识别响应越界，真实中断时仍保留 OAuth 可恢复的已读 SSE。
func TestCreativeUpstreamBodyDetectsOverflow(t *testing.T) {
	data, err := readCreativeUpstreamBody(strings.NewReader("12345678"), 8)
	require.NoError(t, err)
	require.Equal(t, "12345678", string(data))
	data, err = readCreativeUpstreamBody(strings.NewReader("123456789"), 8)
	require.ErrorAs(t, err, new(*CreativeProviderResultUnconfirmedError))
	require.Empty(t, data)
	interrupted := errors.New("interrupted")
	data, err = readCreativeUpstreamBody(io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(interrupted)), 16)
	require.ErrorIs(t, err, interrupted)
	require.Equal(t, "partial", string(data))
}
