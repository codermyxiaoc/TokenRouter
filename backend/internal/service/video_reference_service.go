package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"image"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/util/urlvalidator"
	_ "golang.org/x/image/webp"
)

const (
	VideoReferenceTTL         = 2 * time.Hour
	VideoReferenceMaxBytes    = 32 << 20
	videoReferenceMaxCount    = 16
	videoReferenceMaxReads    = 32
	videoReferenceHourlyBytes = 64 << 20
	videoReferenceHourlyCount = 64
	videoReferencePath        = "/api/v1/video-references/"
)

var (
	ErrVideoReferencePublicURL   = infraerrors.New(http.StatusBadRequest, "VIDEO_REFERENCE_PUBLIC_URL_REQUIRED", "该视频方案需要公网参考图，请管理员先配置可由上游访问的本站前端基础 URL；本次未提交生成或预扣费用")
	ErrVideoReferenceInvalid     = infraerrors.New(http.StatusBadRequest, "VIDEO_REFERENCE_INVALID", "参考图必须是有效的 PNG、JPEG 或 WebP 图片，单次总大小不能超过 32 MiB")
	ErrVideoReferenceUnavailable = infraerrors.New(http.StatusServiceUnavailable, "VIDEO_REFERENCE_UNAVAILABLE", "参考图交付服务暂时不可用，请稍后重试；本次未提交生成或预扣费用")
	ErrVideoReferenceQuota       = infraerrors.New(http.StatusTooManyRequests, "VIDEO_REFERENCE_QUOTA", "短期参考图上传过多，请稍后重试；本次未提交生成或预扣费用")
	ErrVideoReferenceNotFound    = infraerrors.New(http.StatusNotFound, "VIDEO_REFERENCE_NOT_FOUND", "参考图链接不存在或已失效")
)

// VideoReferenceStore 的签发与读取预算必须由共享存储原子执行；接口不暴露 Redis、文件路径或原始票据。
type VideoReferenceStore interface {
	Ready(context.Context) error
	IssueBatch(context.Context, VideoReferenceBatch) error
	Open(context.Context, string, string) (*VideoReferenceContent, error)
}

// VideoReferenceBatch 固定签发时刻、过期时刻及访问预算，读取不得刷新期限。
type VideoReferenceBatch struct {
	UserID                   int64
	IssuedAt, ExpiresAt      time.Time
	MaxReads, HourlyMaxCount int
	HourlyMaxBytes           int64
	Images                   []VideoReferenceBlob
}

// VideoReferenceBlob 只以不可逆摘要标识图片，不把具有访问能力的原始票据写入共享存储。
type VideoReferenceBlob struct {
	Digest, MIMEType string
	Body             []byte
}

// VideoReferenceService 只为已鉴权调用者提供单张图片的短期能力票据，不接受资产 ID、文件路径或远程下载目标。
// 共享 Redis 同时保存原图和访问预算，支持应用重启与多实例；票据和正文到期一起清除，不修改原始私有素材。
// @project-doc docs/interfaces/cloud_canvas.md#video_reference_delivery
type VideoReferenceService struct {
	store   VideoReferenceStore
	baseURL func(context.Context) string
	// assets 为参考视频票据提供素材读取，由画布服务装配时注入，见 video_reference_assets.go。
	assets VideoReferenceAssetSource
}

func NewVideoReferenceService(store VideoReferenceStore, settings *SettingService) *VideoReferenceService {
	s := &VideoReferenceService{store: store}
	if settings != nil {
		s.baseURL = settings.GetFrontendURL
	}
	return s
}

// ValidateReady 必须在模型调用和费用预留前执行；URL 只来自可信设置，绝不使用请求 Host 或转发头。
func (s *VideoReferenceService) ValidateReady(ctx context.Context) error {
	_, err := s.publicBase(ctx)
	if err != nil {
		return err
	}
	if s.store == nil || s.store.Ready(ctx) != nil {
		return ErrVideoReferenceUnavailable
	}
	return nil
}

func (s *VideoReferenceService) publicBase(ctx context.Context) (string, error) {
	if s == nil || s.baseURL == nil {
		return "", ErrVideoReferencePublicURL
	}
	raw := strings.TrimSpace(s.baseURL(ctx))
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || strings.Contains(u.Hostname(), "%") {
		return "", ErrVideoReferencePublicURL
	}
	// DNS 绝对名称末尾的点不能绕过 localhost 或内网地址字面量检查。
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if urlvalidator.IsBlockedHost(host) || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || (!strings.Contains(host, ".") && net.ParseIP(host) == nil) {
		return "", ErrVideoReferencePublicURL
	}
	if ip := net.ParseIP(host); ip != nil && (!ip.IsGlobalUnicast() || ip.IsPrivate()) {
		return "", ErrVideoReferencePublicURL
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", ErrVideoReferencePublicURL
		}
	}
	// 前端设置可指向某个页面，但后端 API 固定在站点根路径；不能拼成 /页面/api/v1。
	u.Path, u.RawPath = "", ""
	return strings.TrimRight(u.String(), "/"), nil
}

// PrepareDataURLs 的 userID 必须由登录身份或已验证的持久任务提供；调用方先完成素材归属验证。
// 公网 URL 不在此服务抓取，避免新增 SSRF 或把任意文件变成公网资源。
func (s *VideoReferenceService) PrepareDataURLs(ctx context.Context, userID int64, inputs []string) ([]string, error) {
	if len(inputs) == 0 {
		return []string{}, nil
	}
	if userID <= 0 || len(inputs) > videoReferenceMaxCount {
		return nil, ErrVideoReferenceInvalid
	}
	if err := s.ValidateReady(ctx); err != nil {
		return nil, err
	}
	base, err := s.publicBase(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	batch := VideoReferenceBatch{UserID: userID, IssuedAt: now, ExpiresAt: now.Add(VideoReferenceTTL), MaxReads: videoReferenceMaxReads, HourlyMaxBytes: videoReferenceHourlyBytes, HourlyMaxCount: videoReferenceHourlyCount}
	urls := make([]string, 0, len(inputs))
	total := 0
	for _, raw := range inputs {
		mime, body, err := decodeVideoReferenceDataURL(raw, VideoReferenceMaxBytes-total)
		if err != nil {
			return nil, err
		}
		total += len(body)
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			return nil, ErrVideoReferenceUnavailable
		}
		token := hex.EncodeToString(secret[:])
		batch.Images = append(batch.Images, VideoReferenceBlob{Digest: videoReferenceDigest(token), MIMEType: mime, Body: body})
		urls = append(urls, base+videoReferencePath+token)
	}
	if err := s.store.IssueBatch(ctx, batch); err != nil {
		return nil, err
	}
	return urls, nil
}

func decodeVideoReferenceDataURL(raw string, maxBytes int) (string, []byte, error) {
	if maxBytes <= 0 || len(raw) > 64+base64.StdEncoding.EncodedLen(maxBytes) {
		return "", nil, ErrVideoReferenceInvalid
	}
	header, payload, ok := strings.Cut(raw, ",")
	if !ok {
		return "", nil, ErrVideoReferenceInvalid
	}
	var mime string
	switch strings.ToLower(header) {
	case "data:image/png;base64":
		mime = "image/png"
	case "data:image/jpeg;base64":
		mime = "image/jpeg"
	case "data:image/webp;base64":
		mime = "image/webp"
	default:
		return "", nil, ErrVideoReferenceInvalid
	}
	body, err := base64.StdEncoding.Strict().DecodeString(payload)
	if err != nil || len(body) == 0 || len(body) > maxBytes || http.DetectContentType(body) != mime {
		return "", nil, ErrVideoReferenceInvalid
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 65536 || cfg.Height > 65536 || int64(cfg.Width)*int64(cfg.Height) > 100000000 {
		return "", nil, ErrVideoReferenceInvalid
	}
	return mime, body, nil
}

func videoReferenceDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

type VideoReferenceContent struct {
	MIMEType string
	Body     []byte
	Size     int64
}

// Open 只消费固定票据，不接受重定向、范围、资产编号或路径，读取不会延长原到期时间。
func (s *VideoReferenceService) Open(ctx context.Context, token, method string) (*VideoReferenceContent, error) {
	if s == nil || s.store == nil || len(token) != 64 || (method != http.MethodGet && method != http.MethodHead) {
		return nil, ErrVideoReferenceNotFound
	}
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 || token != strings.ToLower(token) {
		return nil, ErrVideoReferenceNotFound
	}
	content, err := s.store.Open(ctx, videoReferenceDigest(token), method)
	if err != nil || content == nil || content.Size < 1 || content.Size > VideoReferenceMaxBytes || (method == http.MethodGet && int64(len(content.Body)) != content.Size) {
		return nil, ErrVideoReferenceNotFound
	}
	if content.MIMEType != "image/png" && content.MIMEType != "image/jpeg" && content.MIMEType != "image/webp" {
		return nil, ErrVideoReferenceNotFound
	}
	return content, nil
}
