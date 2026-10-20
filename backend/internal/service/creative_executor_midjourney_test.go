//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 使用可解码的小图片复现真实响应结构，测试不读取外部素材或凭据。
func creativeMJTestPNG(t *testing.T, red uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: red, A: 255})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func creativeMJTestRun() CreativeRun {
	return CreativeRun{Operation: CreativeOperationGenerate, RequestedOutputCount: 4, ImageSize: "1K", AspectRatio: "1:1"}
}

func TestCreativeMidjourneyRequestContract(t *testing.T) {
	for _, model := range []string{"MJ-v7", "MJ-v8.1", "MJ-v8.2", "MJ", "mj_custom", "MjFast"} {
		body, contentType, err := buildCreativeOpenAIRequestBody(creativeMJTestRun(), CreativeRunPayload{Prompt: "a teapot"}, model)
		require.NoError(t, err)
		require.Equal(t, "application/json", contentType)
		var got map[string]any
		require.NoError(t, json.Unmarshal(body, &got))
		require.Equal(t, map[string]any{"model": model, "prompt": "a teapot", "n": float64(1), "size": "1024x1024", "response_format": "b64_json"}, got)
	}
	for _, mutate := range []func(*CreativeRun, *CreativeRunPayload){
		func(r *CreativeRun, p *CreativeRunPayload) { r.RequestedOutputCount = 1 },
		func(r *CreativeRun, p *CreativeRunPayload) { r.RequestedOutputCount = 16 },
		func(r *CreativeRun, p *CreativeRunPayload) { r.Operation = CreativeOperationEdit },
		func(r *CreativeRun, p *CreativeRunPayload) { r.ImageSize = "8K" },
		func(r *CreativeRun, p *CreativeRunPayload) { r.ImageSize = "4096x4096" },
		func(r *CreativeRun, p *CreativeRunPayload) { r.AspectRatio = "3:2" },
		func(r *CreativeRun, p *CreativeRunPayload) { r.AspectRatio = "auto" },
		func(r *CreativeRun, p *CreativeRunPayload) { p.Quality = "high" },
		func(r *CreativeRun, p *CreativeRunPayload) { p.Background = "transparent" },
		func(r *CreativeRun, p *CreativeRunPayload) { p.ThinkingLevel = "high" },
		func(r *CreativeRun, p *CreativeRunPayload) { p.Sources = []CreativeInputImage{{Bytes: []byte("x")}} },
		func(r *CreativeRun, p *CreativeRunPayload) { p.Mask = &CreativeInputImage{} },
	} {
		run, payload := creativeMJTestRun(), CreativeRunPayload{Prompt: "a teapot"}
		mutate(&run, &payload)
		_, _, err := buildCreativeMidjourneyRequestBody(run, payload, "MJ-v7")
		require.Error(t, err)
		require.False(t, IsRetryableCreativeError(err))
	}
}

// TestCreativeMidjourneySizeAndRatioContract 覆盖全部档位与常见比例，保留旧正方形请求形状。
func TestCreativeMidjourneySizeAndRatioContract(t *testing.T) {
	sizes := []struct {
		tier   string
		pixels []string
	}{
		{"1K", []string{"1024x1024", "1024x768", "768x1024", "1024x576", "576x1024"}},
		{"2K", []string{"2048x2048", "2048x1536", "1536x2048", "2048x1152", "1152x2048"}},
		{"4K", []string{"4096x4096", "4096x3072", "3072x4096", "4096x2304", "2304x4096"}},
	}
	for _, size := range sizes {
		for index, ratio := range []string{"1:1", "4:3", "3:4", "16:9", "9:16"} {
			t.Run(size.tier+"/"+ratio, func(t *testing.T) {
				run := creativeMJTestRun()
				run.ImageSize, run.AspectRatio = size.tier, ratio
				body, contentType, err := buildCreativeOpenAIRequestBody(run, CreativeRunPayload{Prompt: "a teapot"}, "MJ-v8.2")
				require.NoError(t, err)
				require.Equal(t, "application/json", contentType)
				var got map[string]any
				require.NoError(t, json.Unmarshal(body, &got))
				require.Equal(t, size.pixels[index], got["size"])
				require.Equal(t, float64(1), got["n"])
				require.Equal(t, "MJ-v8.2", got["model"])
				require.NotContains(t, got, "resolution")
				if ratio == "1:1" {
					require.NotContains(t, got, "ratio")
				} else {
					require.Equal(t, ratio, got["ratio"])
				}
			})
		}
	}
	for _, size := range []string{"", " 1k "} {
		run := creativeMJTestRun()
		run.ImageSize, run.AspectRatio = size, " "
		body, _, err := buildCreativeMidjourneyRequestBody(run, CreativeRunPayload{Prompt: "a teapot"}, "MJ-v7")
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.Unmarshal(body, &got))
		require.Equal(t, "1024x1024", got["size"])
		require.NotContains(t, got, "ratio")
	}
}

// TestCreativeMidjourneyResponseReadBudget 保证四张合法图片有足够预算，同时保留读取上界。
func TestCreativeMidjourneyResponseReadBudget(t *testing.T) {
	encodedFourImages := int64(4 * base64.StdEncoding.EncodedLen(32<<20))
	require.Greater(t, creativeMidjourneyMaxResponseBytes, encodedFourImages)
	require.LessOrEqual(t, creativeMidjourneyMaxResponseBytes, int64(192<<20))
	for _, size := range []int{16, 17} {
		body, err := readCreativeMidjourneyBody(strings.NewReader(strings.Repeat("x", size)), 16)
		if size == 16 {
			require.NoError(t, err)
			require.Len(t, body, 16)
		} else {
			require.Error(t, err)
			require.Empty(t, body)
			var unconfirmed *CreativeProviderResultUnconfirmedError
			require.ErrorAs(t, err, &unconfirmed)
			require.False(t, IsRetryableCreativeError(err))
		}
	}
	// 无效上界与读取中断都不能降级成可再次付费生成的错误。
	for _, test := range []struct {
		name   string
		limit  int64
		reader io.Reader
	}{
		{"零上限", 0, strings.NewReader("x")},
		{"超过最大上限", creativeMidjourneyMaxResponseBytes + 1, strings.NewReader("x")},
		{"中途读取失败", 16, io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(errors.New("read interrupted")))},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := readCreativeMidjourneyBody(test.reader, test.limit)
			require.Empty(t, body)
			var unconfirmed *CreativeProviderResultUnconfirmedError
			require.ErrorAs(t, err, &unconfirmed)
			require.False(t, IsRetryableCreativeError(err))
		})
	}
}

func TestCreativeMidjourneyFourImageResponse(t *testing.T) {
	items := make([]map[string]string, 4)
	for i := range items {
		items[i] = map[string]string{"b64_json": base64.StdEncoding.EncodeToString(creativeMJTestPNG(t, uint8(i+1)))}
	}
	body, err := json.Marshal(map[string]any{"data": items, "object": "image.generation", "status": "completed", "usage": map[string]int{"total_tokens": 0}})
	require.NoError(t, err)
	outputs, err := parseCreativeMidjourneyImageOutputs(context.Background(), body, creativeMidjourneyImageDownloader)
	require.NoError(t, err)
	require.Len(t, outputs, 4)
	for i, output := range outputs {
		require.Equal(t, i, output.Index)
		require.Equal(t, "image/png", output.Mime)
		require.Equal(t, creativeMJTestPNG(t, uint8(i+1)), output.Bytes)
	}
	// 部分无效结果仅交付可验证图片，由现有结算释放其余预占。
	items[1]["b64_json"] = "invalid"
	items[3]["b64_json"] = base64.StdEncoding.EncodeToString([]byte("<html>not an image</html>"))
	body, err = json.Marshal(map[string]any{"data": items})
	require.NoError(t, err)
	outputs, err = parseCreativeMidjourneyImageOutputs(context.Background(), body, creativeMidjourneyImageDownloader)
	require.NoError(t, err)
	require.Len(t, outputs, 2)
	require.Equal(t, 1, outputs[1].Index)
}

func TestCreativeMidjourneyURLResponseAndIsolation(t *testing.T) {
	img := creativeMJTestPNG(t, 42)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		require.Empty(t, r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get("Cookie"))
		require.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(img)
	}))
	defer server.Close()
	body, err := json.Marshal(map[string]any{"data": []map[string]string{{"url": server.URL + "/image.png"}}})
	require.NoError(t, err)
	// 仅测试注入的客户端能访问测试服务器；生产下载器必须阻止此回环地址。
	downloader := NewImageResultUploader(nil, "", creativeMaxOutputBytes, server.Client())
	outputs, err := parseCreativeMidjourneyImageOutputs(context.Background(), body, downloader)
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	require.Equal(t, "image/png", outputs[0].Mime)
	require.Equal(t, 1, requests)
	_, err = parseCreativeMidjourneyImageOutputs(context.Background(), body, creativeMidjourneyImageDownloader)
	require.ErrorAs(t, err, new(*CreativeProviderResultUnconfirmedError))
	require.Equal(t, 1, requests)
}

func TestCreativeMidjourneyInvalidOutputsRemainUnconfirmed(t *testing.T) {
	for _, body := range []string{
		`{`, `{"data":[]}`, `{"data":{}}`, `{"data":[{},{},{},{},{}]}`,
		`{"data":[{"b64_json":123}]}`, `{"data":[{"b64_json":"eA=="}]}`,
		`{"data":[{"url":"file:///secret"}]}`, `{"data":[{"url":"http://127.0.0.1/private"}]}`,
		`{"data":[{"url":"http://user:password@example.com/image.png"}]}`,
		`{"data":[{"url":"data:image/svg+xml;base64,PHN2Zy8+"}]}`,
	} {
		_, err := parseCreativeMidjourneyImageOutputs(context.Background(), []byte(body), creativeMidjourneyImageDownloader)
		require.ErrorAs(t, err, new(*CreativeProviderResultUnconfirmedError))
		require.False(t, IsRetryableCreativeError(err))
		require.NotContains(t, err.Error(), "secret")
		require.NotContains(t, err.Error(), "password")
	}
	// 下载器在解码前限制单张长度，避免超大 Base64 分配。
	body := []byte(`{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(creativeMJTestPNG(t, 1)) + `"}]}`)
	_, err := parseCreativeMidjourneyImageOutputs(context.Background(), body, NewImageResultUploader(nil, "", 8, nil))
	require.ErrorAs(t, err, new(*CreativeProviderResultUnconfirmedError))
	// PNG 头部可解析但正文截断时不能作为成功产物计费。
	truncated := creativeMJTestPNG(t, 1)[:40]
	body, err = json.Marshal(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(truncated)}}})
	require.NoError(t, err)
	_, err = parseCreativeMidjourneyImageOutputs(context.Background(), body, creativeMidjourneyImageDownloader)
	require.ErrorAs(t, err, new(*CreativeProviderResultUnconfirmedError))
}

func TestCreativeMidjourneyHTTPFailureDoesNotResubmit(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		body        string
		err         error
		unconfirmed bool
	}{
		{"network", 0, "", errors.New("connection lost"), true},
		{"server failure", 502, `{"error":"bad gateway"}`, nil, true},
		{"request timeout", 408, `{"error":"request timeout"}`, nil, true},
		{"empty success", 200, `{"data":[]}`, nil, true},
		{"bad json", 200, `<html>error</html>`, nil, true},
		{"invalid params", 400, `{"error":"invalid model"}`, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{err: tc.err, resp: &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}}
			gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			executor := &CreativeExecutor{gateway: gateway}
			account := &Account{ID: 1, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"api_key": "test-key"}}
			_, err := executor.executeOpenAI(context.Background(), creativeMJTestRun(), CreativeRunPayload{Prompt: "teapot"}, account, "MJ-v7")
			require.Error(t, err)
			var unconfirmed *CreativeProviderResultUnconfirmedError
			require.Equal(t, tc.unconfirmed, errors.As(err, &unconfirmed))
			require.False(t, IsRetryableCreativeError(err))
			require.Len(t, upstream.requests, 1)
		})
	}
}

// 验证实际送入 HTTP 的上下文始终有时限；零值配置不能让同步图片请求无限挂起。
func TestCreativeImageExecutionAlwaysHasBoundedRequestContext(t *testing.T) {
	image := base64.StdEncoding.EncodeToString(creativeMJTestPNG(t, 42))
	for _, model := range []string{"MJ-v8.1", "gpt-image-2"} {
		for _, seconds := range []int{-1, 0, 12} {
			t.Run(fmt.Sprintf("%s/%d", model, seconds), func(t *testing.T) {
				cfg := &config.Config{}
				cfg.Creative.ExecuteTimeoutSeconds = seconds
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{},
					Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"` + image + `"}]}`)),
				}}
				executor := &CreativeExecutor{cfg: cfg, gateway: &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}}
				run := creativeMJTestRun()
				if model == "gpt-image-2" {
					run.RequestedOutputCount = 1
				}
				start := time.Now()
				result, err := executor.Execute(context.Background(), run, CreativeRunPayload{Prompt: "a teapot"}, &CreativeExecution{
					Account:       &Account{ID: 1, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"api_key": "local-test"}},
					UpstreamModel: model,
				})
				require.NoError(t, err)
				require.Len(t, result.Outputs, 1)
				require.Len(t, upstream.requests, 1)
				deadline, ok := upstream.requests[0].Context().Deadline()
				require.True(t, ok)
				budget := defaultCreativeExecuteTimeout
				if seconds > 0 {
					budget = time.Duration(seconds) * time.Second
				}
				require.WithinDuration(t, start.Add(budget), deadline, time.Second)
			})
		}
	}
}

// 仅改写有效 PNG 头部即可验证像素上限，不为恶意尺寸分配整张测试图片。
func TestCreativeMidjourneyRejectsOversizedPixelDimensions(t *testing.T) {
	for _, dimensions := range [][2]uint32{{8193, 1}, {4097, 4097}} {
		data := creativeMJTestPNG(t, 1)
		binary.BigEndian.PutUint32(data[16:20], dimensions[0])
		binary.BigEndian.PutUint32(data[20:24], dimensions[1])
		binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
		width, height, err := creativeImageDimensions(data, "image/png")
		require.NoError(t, err)
		require.Equal(t, int(dimensions[0]), width)
		require.Equal(t, int(dimensions[1]), height)
		body, err := json.Marshal(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(data)}}})
		require.NoError(t, err)
		_, err = parseCreativeMidjourneyImageOutputs(context.Background(), body, creativeMidjourneyImageDownloader)
		require.ErrorAs(t, err, new(*CreativeProviderResultUnconfirmedError))
	}
}
