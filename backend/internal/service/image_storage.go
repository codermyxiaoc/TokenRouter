package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/util/urlvalidator"
)

const defaultImageMaxDownloadBytes int64 = 32 << 20 // 32 MiB

const (
	imageTransferMaxAttempts = 3
	imageTransferRetryDelay  = 250 * time.Millisecond
)

// ImageStorage 把图片字节写入对象存储并返回可访问 URL。
// 这是对象存储的可插拔抽象：适配一个新的对象存储厂商，只需实现本接口
// （例如包一个厂商 SDK），无需改动任务/网关逻辑。仓库内自带一个 S3 兼容实现
// （repository.S3ImageStorage），适用于 AWS S3 / Cloudflare R2 / 阿里云 OSS / MinIO 等。
type ImageStorage interface {
	// Save 把 data 以 key 存入对象存储，返回可下载的 URL（公开直链或 presigned 临时链接）。
	// contentType 为图片 MIME 类型，如 "image/png"。
	Save(ctx context.Context, key, contentType string, data []byte) (url string, err error)
}

// ImageObjectURLSigner 为已上传对象重新签发下载链接；只接受本服务生成的对象键，签名期限不超过 expiresAt。
// 公开直链存储直接返回公开地址。用于结果保存后签名先于对象过期的情况（例如维护器较晚补交结果）。
type ImageObjectURLSigner interface {
	SignObjectURL(ctx context.Context, key string, expiresAt time.Time) (string, error)
}

// ImageResultUploader 是 ImageStorage 的上层编排器（与具体厂商无关）：
// 把上游生图响应里的每张图片（b64_json 解码 / url 下载）转存到对象存储，
// 并把响应结果改写为只含短链接的紧凑 JSON，从而避免大 base64 落 Redis。
type ImageResultUploader struct {
	storage          ImageStorage
	httpClient       *http.Client
	prefix           string
	maxDownloadBytes int64
}

// NewImageResultUploader 构造一个 uploader；storage 为 nil 时 Rewrite 直接透传。
func NewImageResultUploader(storage ImageStorage, prefix string, maxDownloadBytes int64, httpClient *http.Client) *ImageResultUploader {
	if httpClient == nil {
		httpClient = defaultImageDownloadHTTPClient()
	}
	if maxDownloadBytes <= 0 {
		maxDownloadBytes = defaultImageMaxDownloadBytes
	}
	return &ImageResultUploader{
		storage:          storage,
		httpClient:       httpClient,
		prefix:           prefix,
		maxDownloadBytes: maxDownloadBytes,
	}
}

// CheckReadiness 在进入生成和计费之前验证固定存储快照，不能只根据凭证非空判断可用。
// 生产 S3 实现包含有界读写探测和并发缓存；无存储的测试实例保持原有行为。
// @project-doc docs/domains/media_tasks.md#async_image_lifecycle
func (u *ImageResultUploader) CheckReadiness(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if u == nil || u.storage == nil {
		return nil
	}
	if checker, ok := u.storage.(interface{ CheckReadiness(context.Context) error }); ok {
		checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		return checker.CheckReadiness(checkCtx)
	}
	return nil
}

// 仅共享安全传输连接池；调用方可独立配置视频超时，不能修改全局图片客户端。
var sharedImageDownloadClient = sync.OnceValue(newImageDownloadHTTPClient)

func defaultImageDownloadHTTPClient() *http.Client {
	client := *sharedImageDownloadClient()
	return &client
}

// 公网下载额外排除 CGNAT、this-network 和云元数据主机，不能仅依赖 IsPrivate。
func isPublicImageDownloadIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || urlvalidator.IsBlockedHost(ip.String()) {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		return v4[0] != 0 && !(v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127)
	}
	return true
}

func isBlockedImageDownloadHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || urlvalidator.IsBlockedHost(host) {
		return true
	}
	switch host {
	case "localhost.localdomain", "metadata", "metadata.google.internal", "metadata.goog", "instance-data", "instance-data.ec2.internal":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return !isPublicImageDownloadIP(ip)
	}
	return false
}

func newImageDownloadHTTPClient() *http.Client {
	// 禁止环境代理和内网连接；直接拨号已校验的解析地址，避免 DNS 重绑定与重定向绕过。
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if isBlockedImageDownloadHost(host) {
			return nil, errors.New("image download target is not public")
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("image download host has no address")
		}
		for _, ip := range ips {
			if !isPublicImageDownloadIP(ip.IP) {
				return nil, errors.New("image download resolved address is not public")
			}
		}
		dialer := net.Dialer{Timeout: 10 * time.Second}
		var lastErr error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	return &http.Client{Timeout: 60 * time.Second, Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many image download redirects")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" || req.URL.User != nil {
			return errors.New("invalid image download redirect")
		}
		if isBlockedImageDownloadHost(req.URL.Hostname()) {
			return errors.New("image download redirect target is not public")
		}
		return nil
	}}
}

// Rewrite 将 result（上游生图响应 JSON）里的每张图片转存到对象存储，
// 返回改写后的紧凑结果（data[i].url 指向对象存储，b64_json 被移除）。
// 任一图片转存失败即返回 error（调用方据此将任务标记为失败，绝不把大 blob 落 Redis）。
func (u *ImageResultUploader) Rewrite(ctx context.Context, taskID string, result json.RawMessage) (json.RawMessage, error) {
	if u == nil || u.storage == nil {
		return result, nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(result, &top); err != nil {
		return nil, classifyImageStorageFailure("decode", imageResultInvalid, fmt.Errorf("parse image response: %w", err))
	}
	rawData, ok := top["data"]
	if !ok {
		return nil, classifyImageStorageFailure("decode", imageResultInvalid, errors.New("image response has no data array"))
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(rawData, &items); err != nil {
		return nil, classifyImageStorageFailure("decode", imageResultInvalid, fmt.Errorf("parse image response data: %w", err))
	}
	if len(items) == 0 {
		return nil, classifyImageStorageFailure("decode", imageResultInvalid, errors.New("image response has no generated images"))
	}
	for i, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, classifyImageStorageFailure("result", imageResultTimeout, err)
		}
		data, contentType, err := u.fetchImageBytes(ctx, item)
		if err != nil {
			return nil, classifyImageStorageFailure("decode", imageResultInvalid, fmt.Errorf("image %d: %w", i, err))
		}
		// 只转存已知位图格式，禁止 SVG/HTML 伪装成图片落入公开存储桶；
		// 还要能解析出有效尺寸，只有文件签名、正文损坏的结果不能算可交付的图片。
		if !isBackfillImageContent(data) || !isDecodableStoredImage(data) {
			return nil, classifyImageStorageFailure("decode", imageResultInvalid, fmt.Errorf("image %d: unsupported image content", i))
		}
		contentType = http.DetectContentType(data)
		key := u.buildKey(taskID, i, contentType)
		// 上传重试只复用本图片字节和稳定对象键，不能重跑生成或已完成图片的下载上传。
		var storedURL string
		err = retryImageTransfer(ctx, func() error {
			var saveErr error
			storedURL, saveErr = u.storage.Save(ctx, key, contentType, data)
			return saveErr
		})
		if err != nil {
			return nil, classifyImageStorageFailure("upload", imageStorageUploadFailed, fmt.Errorf("image %d: upload to object storage: %w", i, err))
		}
		urlRaw, err := json.Marshal(storedURL)
		if err != nil {
			return nil, fmt.Errorf("image %d: encode url: %w", i, err)
		}
		item["url"] = urlRaw
		delete(item, "b64_json")
		items[i] = item
	}
	newData, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("encode image response data: %w", err)
	}
	top["data"] = newData
	out, err := json.Marshal(top)
	if err != nil {
		return nil, fmt.Errorf("encode image response: %w", err)
	}
	return out, nil
}

// retryImageTransfer 仅恢复临时转存故障，共享外层存储截止时间，不新增生成或结算入口。
func retryImageTransfer(ctx context.Context, operation func() error) error {
	for attempt := 0; attempt < imageTransferMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := operation()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt == imageTransferMaxAttempts-1 || !isRetryableImageTransferError(err) {
			return err
		}
		timer := time.NewTimer(imageTransferRetryDelay << attempt)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

// isRetryableImageTransferError 保留状态码优先级，权限、参数、图片校验和未知错误立即失败。
func isRetryableImageTransferError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var httpStatus interface{ HTTPStatusCode() int }
	if errors.As(err, &httpStatus) {
		status := httpStatus.HTTPStatusCode()
		return status == http.StatusTooManyRequests || (status >= 500 && status <= 599)
	}
	var statusCode interface{ StatusCode() int }
	if errors.As(err, &statusCode) {
		status := statusCode.StatusCode()
		return status == http.StatusTooManyRequests || (status >= 500 && status <= 599)
	}
	// url.Error 自身也实现 net.Error；先拆开，避免将内网拒绝、证书或 URL 校验误当网络抖动。
	var requestErr *url.Error
	if errors.As(err, &requestErr) {
		return isRetryableImageTransferError(requestErr.Err)
	}
	var networkErr net.Error
	return errors.As(err, &networkErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// imageDownloadStatusError 仅携带状态码，重试判断不依赖可能包含敏感信息的响应正文。
type imageDownloadStatusError struct{ status int }

func (e *imageDownloadStatusError) Error() string {
	return fmt.Sprintf("download image: unexpected status %d", e.status)
}

func (e *imageDownloadStatusError) HTTPStatusCode() int { return e.status }

func (u *ImageResultUploader) fetchImageBytes(ctx context.Context, item map[string]json.RawMessage) ([]byte, string, error) {
	if raw, ok := item["b64_json"]; ok {
		var b64 string
		if err := json.Unmarshal(raw, &b64); err == nil {
			if b64 = strings.TrimSpace(b64); b64 != "" {
				limit := u.maxDownloadBytes
				if int64(base64.StdEncoding.DecodedLen(len(b64))) > limit+2 {
					return nil, "", classifyImageStorageFailure("decode", imageResultTooLarge, fmt.Errorf("decoded image exceeds %d bytes", limit))
				}
				data, err := base64.StdEncoding.DecodeString(b64)
				if err != nil {
					return nil, "", fmt.Errorf("decode b64_json: %w", err)
				}
				if int64(len(data)) > limit {
					return nil, "", classifyImageStorageFailure("decode", imageResultTooLarge, fmt.Errorf("decoded image exceeds %d bytes", limit))
				}
				return data, detectImageContentType(data), nil
			}
		}
	}
	if raw, ok := item["url"]; ok {
		var rawURL string
		if err := json.Unmarshal(raw, &rawURL); err == nil {
			if rawURL = strings.TrimSpace(rawURL); rawURL != "" {
				if len(rawURL) >= len("data:") && strings.EqualFold(rawURL[:len("data:")], "data:") {
					return u.decodeImageDataURL(rawURL)
				}
				return u.download(ctx, rawURL)
			}
		}
	}
	return nil, "", errors.New("image item has neither b64_json nor url")
}

func (u *ImageResultUploader) decodeImageDataURL(rawURL string) ([]byte, string, error) {
	header, payload, ok := strings.Cut(rawURL[len("data:"):], ",")
	if !ok {
		return nil, "", errors.New("decode image data URL: missing comma separator")
	}

	parts := strings.Split(header, ";")
	if strings.TrimSpace(parts[0]) == "" {
		return nil, "", errors.New("decode image data URL: missing media type")
	}
	base64Index := len(parts) - 1
	if base64Index < 1 || !strings.EqualFold(strings.TrimSpace(parts[base64Index]), "base64") {
		for i := 1; i < base64Index; i++ {
			if strings.EqualFold(strings.TrimSpace(parts[i]), "base64") {
				return nil, "", errors.New("decode image data URL: base64 marker must be the final header token")
			}
		}
		return nil, "", errors.New("decode image data URL: payload is not base64 encoded")
	}
	for i := 1; i < base64Index; i++ {
		if strings.EqualFold(strings.TrimSpace(parts[i]), "base64") {
			return nil, "", errors.New("decode image data URL: duplicate base64 marker")
		}
	}
	mediaTypeHeader := strings.Join(parts[:base64Index], ";")
	declaredType, _, err := mime.ParseMediaType(mediaTypeHeader)
	if err != nil {
		return nil, "", fmt.Errorf("decode image data URL: invalid media type: %w", err)
	}
	declaredType = strings.ToLower(declaredType)
	if !strings.HasPrefix(declaredType, "image/") {
		return nil, "", fmt.Errorf("decode image data URL: media type %q is not an image", declaredType)
	}

	limit := u.maxDownloadBytes
	if limit <= 0 {
		limit = defaultImageMaxDownloadBytes
	}
	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(payload))
	data, err := io.ReadAll(io.LimitReader(decoder, limit+1))
	if err != nil {
		return nil, "", fmt.Errorf("decode image data URL base64 payload: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, "", classifyImageStorageFailure("decode", imageResultTooLarge, fmt.Errorf("decoded image data URL exceeds %d bytes", limit))
	}

	contentType := detectedImageContentType(data)
	if contentType == "" {
		contentType = declaredType
	}
	return data, contentType, nil
}

func (u *ImageResultUploader) download(ctx context.Context, rawURL string) ([]byte, string, error) {
	// 仅网络下载可重试，data URL/Base64 解码的 EOF 属于固定输入错误，不能套用网络恢复。
	var data []byte
	var contentType string
	err := retryImageTransfer(ctx, func() error {
		var downloadErr error
		data, contentType, downloadErr = u.downloadOnce(ctx, rawURL)
		return downloadErr
	})
	return data, contentType, classifyImageStorageFailure("download", imageSourceDownloadFailed, err)
}

func (u *ImageResultUploader) downloadOnce(ctx context.Context, rawURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build download request: %w", err)
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" || req.URL.User != nil {
		return nil, "", errors.New("invalid image download URL")
	}
	resp, err := u.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download image: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, "", &imageDownloadStatusError{status: resp.StatusCode}
	}
	limit := u.maxDownloadBytes
	if limit <= 0 {
		limit = defaultImageMaxDownloadBytes
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", fmt.Errorf("read image body: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, "", classifyImageStorageFailure("download", imageResultTooLarge, fmt.Errorf("downloaded image exceeds %d bytes", limit))
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	if !strings.HasPrefix(contentType, "image/") {
		// 非图片响应不能伪装成 PNG；Rewrite 仍会进一步拒绝 SVG 等非位图内容。
		contentType = detectedImageContentType(data)
		if contentType == "" {
			return nil, "", classifyImageStorageFailure("download", imageResultInvalid, errors.New("download image: content is not an image"))
		}
	}
	return data, contentType, nil
}

// RefreshExpiredURLs 只为已过期的签名链接重签，返回新的结果副本，不修改调用方保存的记录。
// 对象键按转存规则（前缀 + 任务 ID + 序号 + 扩展名）推导，且必须与原链接路径完全一致，
// 存储位置或前缀已变化时保留原链接；未过期或公开直链原样返回。签名期限不超过 deadline。
// @project-doc docs/domains/media_tasks.md#async_image_object_retention
func (u *ImageResultUploader) RefreshExpiredURLs(ctx context.Context, taskID string, result json.RawMessage, deadline time.Time) json.RawMessage {
	if u == nil || len(result) == 0 || taskID == "" || !deadline.After(time.Now()) {
		return result
	}
	signer, ok := u.storage.(ImageObjectURLSigner)
	if !ok {
		return result
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(result, &root) != nil {
		return result
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(root["data"], &items) != nil {
		return result
	}
	changed := false
	for i, item := range items {
		var rawURL string
		if json.Unmarshal(item["url"], &rawURL) != nil || !imageSignedURLExpired(rawURL, time.Now()) {
			continue
		}
		parsed, err := url.Parse(rawURL)
		if err != nil {
			continue
		}
		key := u.prefix + taskID + "-" + strconv.Itoa(i) + path.Ext(parsed.Path)
		if !strings.HasSuffix(parsed.Path, "/"+key) && !strings.HasSuffix(parsed.EscapedPath(), "/"+key) {
			continue
		}
		signed, err := signer.SignObjectURL(ctx, key, deadline)
		if err != nil {
			continue
		}
		encoded, err := json.Marshal(signed)
		if err != nil {
			continue
		}
		item["url"] = encoded
		changed = true
	}
	if !changed {
		return result
	}
	data, err := json.Marshal(items)
	if err != nil {
		return result
	}
	root["data"] = data
	out, err := json.Marshal(root)
	if err != nil {
		return result
	}
	return out
}

// imageSignedURLExpired 识别 S3 预签名（X-Amz-Date + X-Amz-Expires）与本地签名（exp）链接是否已过期；
// 无签名参数的公开直链视为不过期。
func imageSignedURLExpired(rawURL string, now time.Time) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	query := parsed.Query()
	if signedAt, err := time.Parse("20060102T150405Z", query.Get("X-Amz-Date")); err == nil {
		seconds, err := strconv.ParseInt(query.Get("X-Amz-Expires"), 10, 64)
		return err == nil && !signedAt.Add(time.Duration(seconds)*time.Second).After(now)
	}
	if exp, err := strconv.ParseInt(query.Get("exp"), 10, 64); err == nil && query.Get("sig") != "" {
		return exp <= now.Unix()
	}
	return false
}

func (u *ImageResultUploader) buildKey(taskID string, index int, contentType string) string {
	return u.prefix + taskID + "-" + strconv.Itoa(index) + extensionForContentType(contentType)
}

// isDecodableStoredImage 要求图片能完整解码出正的宽高：只有合法头部而像素数据截断或缺失的结果不能算可交付。
// 先读头部做尺寸上限检查，避免解压炸弹在完整解码时放大内存；WebP 解码器由本包的 x/image/webp 注册。
func isDecodableStoredImage(data []byte) bool {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return false
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxStoredImagePixels {
		return false
	}
	_, _, err = image.Decode(bytes.NewReader(data))
	return err == nil
}

// maxStoredImagePixels 限制完整解码的像素数（约 1 亿像素，覆盖 8K 以上分辨率）。
const maxStoredImagePixels = 100_000_000

func detectImageContentType(data []byte) string {
	if ct := detectedImageContentType(data); ct != "" {
		return ct
	}
	return "image/png"
}

func detectedImageContentType(data []byte) string {
	ct := strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0])
	if strings.HasPrefix(ct, "image/") {
		return ct
	}
	return ""
}

func extensionForContentType(ct string) string {
	switch {
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "jpeg"), strings.Contains(ct, "jpg"):
		return ".jpg"
	case strings.Contains(ct, "webp"):
		return ".webp"
	case strings.Contains(ct, "gif"):
		return ".gif"
	default:
		return ".png"
	}
}
