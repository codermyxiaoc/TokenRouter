package service

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"strings"

	"github.com/tidwall/gjson"
)

// 复用仅访问公网的下载器及连接池，不携带上游账号凭据，也不持久化图片或素材地址。
var creativeMidjourneyImageDownloader = NewImageResultUploader(nil, "", creativeMaxOutputBytes, nil)

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
	if size := strings.TrimSpace(run.ImageSize); size != "" && !strings.EqualFold(size, "1K") {
		return nil, "", creativeNonRetryableError("unsupported Midjourney image size")
	}
	if ratio := strings.TrimSpace(run.AspectRatio); ratio != "" && ratio != "1:1" {
		return nil, "", creativeNonRetryableError("unsupported Midjourney aspect ratio")
	}
	body, err := json.Marshal(map[string]any{
		"model": model, "prompt": payload.Prompt, "n": 1,
		"size": "1024x1024", "response_format": "b64_json",
	})
	return body, "application/json", err
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
