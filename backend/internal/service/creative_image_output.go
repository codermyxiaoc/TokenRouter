package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"strings"

	"github.com/tidwall/gjson"
)

// 图片生成已经完成；此下载器只取回同一产物，不带上游鉴权、不使用环境代理。
var creativeOutputImageDownloader = NewImageResultUploader(nil, "", creativeMaxOutputBytes, nil)

func (e *CreativeExecutor) outputImageDownloader() *ImageResultUploader {
	if e != nil && e.imageDownloader != nil {
		return e.imageDownloader
	}
	return creativeOutputImageDownloader
}

// parseCreativeOpenAIImageOutputs 同时兼容 OpenAI/Grok 的内联图片与 URL，维持单张交付合同。
// 只有真实可解码图片才交给成功结算；产物缺失或下载失败保留原任务核对，不能再次生成。
func parseCreativeOpenAIImageOutputs(ctx context.Context, body []byte, downloader *ImageResultUploader) ([]CreativeOutput, error) {
	if !gjson.ValidBytes(body) || downloader == nil {
		return nil, creativeResultUnconfirmedError()
	}
	data := gjson.GetBytes(body, "data")
	if !data.IsArray() {
		return nil, creativeResultUnconfirmedError()
	}
	for _, item := range data.Array() {
		if ctx.Err() != nil {
			return nil, creativeResultUnconfirmedError()
		}
		if !item.IsObject() {
			continue
		}
		// 优先使用有效内联结果，避免为同时含 URL 的响应多发下载请求。
		if raw := item.Get("b64_json"); raw.Type == gjson.String {
			if output, ok := creativeInlineImageOutput(strings.TrimSpace(raw.String()), downloader); ok {
				return []CreativeOutput{output}, nil
			}
		}
		if raw := item.Get("url"); raw.Type == gjson.String && strings.TrimSpace(raw.String()) != "" {
			// 仅传递产物地址，不能把生成请求头、Cookie 或账号密钥带到下载主机。
			encoded, _ := json.Marshal(strings.TrimSpace(raw.String()))
			data, _, err := downloader.fetchImageBytes(ctx, map[string]json.RawMessage{"url": encoded})
			if err == nil {
				if output, ok := validatedCreativeImageOutput(data, downloader.maxDownloadBytes); ok {
					return []CreativeOutput{output}, nil
				}
			}
		}
	}
	return nil, creativeResultUnconfirmedError()
}

// 保留无 padding 的旧版 base64 兼容，同时为 data URI 和普通编码设置解码前大小上限。
func creativeInlineImageOutput(raw string, downloader *ImageResultUploader) (CreativeOutput, bool) {
	if raw == "" {
		return CreativeOutput{}, false
	}
	if len(raw) >= 5 && strings.EqualFold(raw[:5], "data:") {
		data, _, err := downloader.decodeImageDataURL(raw)
		if err != nil {
			return CreativeOutput{}, false
		}
		return validatedCreativeImageOutput(data, downloader.maxDownloadBytes)
	}
	limit := creativeImageOutputByteLimit(downloader.maxDownloadBytes)
	if int64(base64.StdEncoding.DecodedLen(len(raw))) > limit+2 {
		return CreativeOutput{}, false
	}
	decoded, err := decodeBase64Image(raw)
	if err != nil {
		return CreativeOutput{}, false
	}
	return validatedCreativeImageOutput(decoded.Bytes, limit)
}

func creativeImageOutputByteLimit(limit int64) int64 {
	if limit <= 0 || limit > creativeMaxOutputBytes {
		return creativeMaxOutputBytes
	}
	return limit
}

// 不信任扩展名和 MIME：先检查像素预算，再完整解码，拒绝 HTML、SVG 和截断的图片。
func validatedCreativeImageOutput(data []byte, limit int64) (CreativeOutput, bool) {
	if len(data) == 0 || int64(len(data)) > creativeImageOutputByteLimit(limit) {
		return CreativeOutput{}, false
	}
	mime := sniffCreativeImageMime(data)
	width, height, err := creativeImageDimensions(data, mime)
	if err != nil || width <= 0 || height <= 0 || width > 8192 || height > 8192 || int64(width)*int64(height) > 16<<20 {
		return CreativeOutput{}, false
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return CreativeOutput{}, false
	}
	return CreativeOutput{Index: 0, Bytes: data, Mime: mime}, true
}
