package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"strings"

	"github.com/tidwall/gjson"
)

// 复用仅访问公网的下载器及连接池，不携带上游账号凭据，也不持久化图片或素材地址。
var creativeMidjourneyImageDownloader = NewImageResultUploader(nil, "", creativeMaxOutputBytes, nil)

// MJ 最多四张、每张 32 MiB；总包允许相应 base64 开销和有界 JSON 余量。
// 单图与像素限制仍由解析器逐张检查，不能因开放 4K 而允许无界响应。
var creativeMidjourneyMaxResponseBytes = int64(creativeMidjourneyOutputCount*base64.StdEncoding.EncodedLen(creativeMaxOutputBytes)) + 1<<20

// readCreativeMidjourneyBody 多读一个字节确认越界，不能把截断后的半份图片响应当成有效结果。
func readCreativeMidjourneyBody(body io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 || limit > creativeMidjourneyMaxResponseBytes {
		return nil, creativeResultUnconfirmedError()
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, creativeResultUnconfirmedError()
	}
	return data, nil
}

// buildCreativeMidjourneyRequestBody 使用已验证的 Images 兼容协议；一次上游任务返回四张。
// n 是上游任务数量，不能把站内预占的四张写成 n=4 而生成十六张。
// @project-doc docs/domains/creative_studio.md#midjourney_images
func buildCreativeMidjourneyRequestBody(run CreativeRun, payload CreativeRunPayload, model string) ([]byte, string, error) {
	if run.Operation != CreativeOperationGenerate || run.RequestedOutputCount != 4 || len(payload.Sources) != 0 || payload.Mask != nil {
		return nil, "", creativeNonRetryableError("Midjourney requires a four-image text generation reservation")
	}
	if strings.TrimSpace(payload.Prompt) == "" || strings.TrimSpace(payload.Quality) != "" || strings.TrimSpace(payload.Background) != "" || strings.TrimSpace(payload.ThinkingLevel) != "" {
		return nil, "", creativeNonRetryableError("unsupported Midjourney generation parameters")
	}
	size, ratio, err := creativeMidjourneyImageSize(run.ImageSize, run.AspectRatio)
	if err != nil {
		return nil, "", err
	}
	request := map[string]any{
		"model": model, "prompt": payload.Prompt, "n": 1,
		"size": size, "response_format": "b64_json",
	}
	if ratio != "1:1" {
		// 非正方形同时声明精确比例；不发送与 size 互斥的 resolution，旧正方形请求保持原状。
		request["ratio"] = ratio
	}
	body, err := json.Marshal(request)
	return body, "application/json", err
}

// creativeMidjourneyImageSize 按长边映射 MJ 的三档尺寸，不借用 GPT 的尺寸或比例限制。
// 空值只用于兼容已排队旧任务；其它未知值一律在调用上游之前拒绝。
func creativeMidjourneyImageSize(imageSize, aspectRatio string) (string, string, error) {
	longEdge := 0
	switch strings.ToUpper(strings.TrimSpace(imageSize)) {
	case "", ImageBillingSize1K:
		longEdge = 1024
	case ImageBillingSize2K:
		longEdge = 2048
	case ImageBillingSize4K:
		longEdge = 4096
	default:
		return "", "", creativeNonRetryableError("unsupported Midjourney image size")
	}
	ratio := strings.TrimSpace(aspectRatio)
	width, height := longEdge, longEdge
	switch ratio {
	case "", "1:1":
		ratio = "1:1"
	case "4:3":
		height = longEdge * 3 / 4
	case "3:4":
		width = longEdge * 3 / 4
	case "16:9":
		height = longEdge * 9 / 16
	case "9:16":
		width = longEdge * 9 / 16
	default:
		return "", "", creativeNonRetryableError("unsupported Midjourney aspect ratio")
	}
	return fmt.Sprintf("%dx%d", width, height), ratio, nil
}

// parseCreativeMidjourneyImageOutputs 完整保留最多四张真实图片，交给现有逐张结算与交付流程。
// URL 下载只能重试读取同一产物；无可确认输出时保留执行栅栏，禁止重新付费生成。
func parseCreativeMidjourneyImageOutputs(ctx context.Context, body []byte, downloader *ImageResultUploader) ([]CreativeOutput, error) {
	if !gjson.ValidBytes(body) || downloader == nil {
		return nil, creativeResultUnconfirmedError()
	}
	data := gjson.GetBytes(body, "data")
	items := data.Array()
	if !data.IsArray() || len(items) == 0 || len(items) > 4 {
		return nil, creativeResultUnconfirmedError()
	}
	outputs := make([]CreativeOutput, 0, len(items))
	for _, item := range items {
		var imageItem map[string]json.RawMessage
		if !item.IsObject() || json.Unmarshal([]byte(item.Raw), &imageItem) != nil {
			continue
		}
		data, _, err := downloader.fetchImageBytes(ctx, imageItem)
		if err != nil || len(data) == 0 || len(data) > creativeMaxOutputBytes {
			continue
		}
		// 不信扩展名或 Content-Type，拒绝伪装的 HTML/SVG 与不可解码的图片头。
		mime := sniffCreativeImageMime(data)
		width, height, err := creativeImageDimensions(data, mime)
		if err != nil || width <= 0 || height <= 0 || width > 8192 || height > 8192 || int64(width)*int64(height) > 16<<20 {
			continue
		}
		// 先限制像素再完整解码，只有图片头而缺失正文的响应不能算作成功收费。
		if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
			continue
		}
		outputs = append(outputs, CreativeOutput{Index: len(outputs), Bytes: data, Mime: mime})
	}
	if len(outputs) == 0 {
		return nil, creativeResultUnconfirmedError()
	}
	return outputs, nil
}
