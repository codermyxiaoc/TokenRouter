package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/TokenFlux/TokenRouter/internal/util/urlvalidator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// AccountExtraImagesURLToB64JSON 是账户 extra 中的开关键。
const AccountExtraImagesURLToB64JSON = "images_url_to_b64_json"

const openAIImageURLDownloadTimeout = 60 * time.Second

// ImagesURLToB64JSONEnabled 返回账户是否开启 URL 到 base64 的图片回填。
func ImagesURLToB64JSONEnabled(account *Account) bool {
	return account != nil && account.Platform == PlatformOpenAI && account.Type == AccountTypeAPIKey && account.getExtraBool(AccountExtraImagesURLToB64JSON)
}

// backfillOpenAIImagesB64JSON 仅处理非流式 Images 响应中缺失的 b64_json。
// 单项下载失败只记录日志并保留原响应，避免图片 URL 回填影响主请求成功。
// @project-doc docs/interfaces/openai_upstream.md#images_url_backfill
func (s *OpenAIGatewayService) backfillOpenAIImagesB64JSON(
	ctx context.Context,
	account *Account,
	parsed *OpenAIImagesRequest,
	body []byte,
) []byte {
	if !ImagesURLToB64JSONEnabled(account) || !gjson.ValidBytes(body) || (parsed != nil && (parsed.Stream || strings.EqualFold(strings.TrimSpace(parsed.ResponseFormat), "url"))) {
		return body
	}
	items := gjson.GetBytes(body, "data")
	if !items.IsArray() {
		return body
	}
	for index, item := range items.Array() {
		if !item.IsObject() || strings.TrimSpace(item.Get("b64_json").String()) != "" {
			continue
		}
		rawURL := strings.TrimSpace(item.Get("url").String())
		if rawURL == "" {
			continue
		}
		encoded, err := s.fetchOpenAIImageURLBase64(ctx, account, rawURL)
		if err != nil {
			// 下载错误可能含带签名的 URL，日志只记录阶段与条目，不记录原始地址。
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Images b64_json backfill skipped account_id=%d index=%d: image download or conversion failed", account.ID, index)
			continue
		}
		updated, err := sjson.SetBytes(body, fmt.Sprintf("data.%d.b64_json", index), encoded)
		if err != nil {
			// 响应写回失败时同样保留原始图片项。
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Images b64_json backfill skipped account_id=%d index=%d: image download or conversion failed", account.ID, index)
			continue
		}
		body = updated
	}
	return body
}

// fetchOpenAIImageURLBase64 下载图片 URL 并返回标准 base64。
// 目的地和每次重定向都必须是公网主机，内容类型只信字节嗅探结果。
func (s *OpenAIGatewayService) fetchOpenAIImageURLBase64(ctx context.Context, account *Account, rawURL string) (string, error) {
	if strings.HasPrefix(strings.ToLower(rawURL), "data:") {
		if encoded := normalizeOpenAIImageBase64(rawURL); encoded != "" {
			if len(encoded) > base64.StdEncoding.EncodedLen(int(openAIImageMaxDownloadBytes)) {
				return "", errors.New("data url image exceeds size limit")
			}
			data, err := base64.StdEncoding.DecodeString(encoded)
			if err == nil && int64(len(data)) <= openAIImageMaxDownloadBytes && isBackfillImageContent(data) {
				return encoded, nil
			}
		}
		return "", errors.New("data url payload is not valid base64")
	}
	if s == nil || s.httpUpstream == nil || account == nil {
		return "", errors.New("http upstream is not configured")
	}
	// URL 校验器为 base URL 规范化路径；下载必须保留签名 URL 的原始转义和尾斜杠。
	downloadURL := strings.TrimSpace(rawURL)
	if _, err := s.validateOutboundURL(downloadURL); err != nil {
		return "", fmt.Errorf("invalid image url: %w", err)
	}
	if err := rejectPrivateImageHost(downloadURL); err != nil {
		return "", err
	}
	downloadCtx, cancel := context.WithTimeout(WithHTTPUpstreamPublicHostsOnly(ctx), openAIImageURLDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(downloadCtx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", fmt.Errorf("build image download request: %w", err)
	}
	req.Header.Set("Accept", "image/*,*/*;q=0.8")
	proxyURL := ""
	if account != nil && account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return "", fmt.Errorf("download image: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("download image: unexpected status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, openAIImageMaxDownloadBytes+1))
	if err != nil {
		return "", fmt.Errorf("read image body: %w", err)
	}
	if int64(len(data)) > openAIImageMaxDownloadBytes {
		return "", fmt.Errorf("downloaded image exceeds %d bytes", openAIImageMaxDownloadBytes)
	}
	if !isBackfillImageContent(data) {
		return "", errors.New("download image: content is not an allowed image format")
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// rejectPrivateImageHost 拒绝 URL 中直接写出的内网、回环和未指定地址。
func rejectPrivateImageHost(downloadURL string) error {
	parsed, err := url.Parse(downloadURL)
	if err != nil {
		return fmt.Errorf("invalid image url: %w", err)
	}
	if host := parsed.Hostname(); urlvalidator.IsBlockedHost(host) {
		return fmt.Errorf("image url host is not allowed: %s", host)
	}
	return nil
}

var openAIImageBackfillContentTypes = map[string]struct{}{
	"image/png": {}, "image/jpeg": {}, "image/webp": {}, "image/gif": {},
}

// isBackfillImageContent 只允许常见位图格式，避免把 SVG/HTML 等内容写入 b64_json。
func isBackfillImageContent(data []byte) bool {
	_, ok := openAIImageBackfillContentTypes[strings.ToLower(http.DetectContentType(data))]
	return ok
}
