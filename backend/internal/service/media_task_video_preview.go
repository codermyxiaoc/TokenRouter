package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"mime"
	"net/http"
	"regexp"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/util/urlvalidator"
)

const mediaTaskPreviewTicketTTL = 10 * time.Minute
const mediaTaskPreviewMaxBytes int64 = 512 << 20

var ErrMediaTaskPreviewUnavailable = infraerrors.New(http.StatusBadGateway, "MEDIA_TASK_PREVIEW_UNAVAILABLE", "媒体预览暂时不可用")
var mediaTaskPreviewRange = regexp.MustCompile(`^bytes=(?:[0-9]+-[0-9]*|-[0-9]+)$`)

// MediaTaskVideoPreviewObserver 只接收已有合法完成响应的媒体快照，不能包含请求正文或凭据。
type MediaTaskVideoPreviewObserver interface {
	ObserveMediaTaskVideoPreview(context.Context, MediaTaskObservation, *MediaTaskVideoSnapshot) error
}

// MediaTaskPreviewIdentity 同时约束来源、原任务、用户及 Key，避免不同供应商任务 ID 碰撞。
type MediaTaskPreviewIdentity struct {
	ID       int64  `json:"id,omitempty"`
	Source   string `json:"source"`
	TaskID   string `json:"task_id"`
	UserID   int64  `json:"user_id"`
	APIKeyID int64  `json:"api_key_id"`
}

type MediaTaskVideoPreviewRecord struct {
	Identity  MediaTaskPreviewIdentity `json:"identity"`
	Media     MediaTaskVideoSnapshot   `json:"media"`
	ExpiresAt time.Time                `json:"expires_at"`
}

type MediaTaskPreviewTicket struct {
	Identity  MediaTaskPreviewIdentity `json:"identity"`
	ExpiresAt time.Time                `json:"expires_at"`
}

// MediaTaskPreviewCache 与持久任务分离；结果最多保留一天，票据仅保留十分钟。
type MediaTaskPreviewCache interface {
	SaveVideo(context.Context, *MediaTaskVideoPreviewRecord, time.Duration) error
	GetVideo(context.Context, MediaTaskPreviewIdentity) (*MediaTaskVideoPreviewRecord, error)
	SaveTicket(context.Context, string, *MediaTaskPreviewTicket, time.Duration) error
	GetTicket(context.Context, string) (*MediaTaskPreviewTicket, error)
}

// ProvideMediaTaskService 在生产装配中启用短期缓存，测试与无缓存实例仍可读取图片结果。
func ProvideMediaTaskService(repo MediaTaskRepository, cache MediaTaskPreviewCache) *MediaTaskService {
	s := NewMediaTaskService(repo)
	s.previewCache = cache
	return s
}

func mediaTaskPreviewIdentity(task *MediaTask) MediaTaskPreviewIdentity {
	return MediaTaskPreviewIdentity{ID: task.ID, Source: task.Source, TaskID: task.TaskID, UserID: task.UserID, APIKeyID: task.APIKeyID}
}

func sameMediaTaskPreviewIdentity(a, b MediaTaskPreviewIdentity) bool {
	return a.Source == b.Source && a.TaskID == b.TaskID && a.UserID == b.UserID && a.APIKeyID == b.APIKeyID
}

// ObserveMediaTaskVideoPreview 是完成响应的附属缓存；调用方应忽略其失败，避免影响原结算。
func (s *MediaTaskService) ObserveMediaTaskVideoPreview(ctx context.Context, o MediaTaskObservation, media *MediaTaskVideoSnapshot) error {
	if s == nil || s.previewCache == nil || media == nil || o.Status != "completed" ||
		(o.Source != "grok_video" && o.Source != "seedance_video") || o.TaskID == "" || len(o.TaskID) > 255 || o.UserID <= 0 || o.APIKeyID <= 0 || !safeMediaPreviewURL(media.URL) {
		return nil
	}
	// 地址还会在实际拨号时校验，缓存阶段不解析 DNS 或访问媒体服务。
	if media.Width < 0 || media.Height < 0 || media.DurationSeconds < 0 || media.SizeBytes < 0 {
		return nil
	}
	expires := time.Now().UTC().Add(24 * time.Hour)
	if o.ExpiresAt != nil && o.ExpiresAt.Before(expires) {
		expires = *o.ExpiresAt
	}
	if !expires.After(time.Now()) {
		return nil
	}
	record := &MediaTaskVideoPreviewRecord{
		Identity: MediaTaskPreviewIdentity{Source: o.Source, TaskID: o.TaskID, UserID: o.UserID, APIKeyID: o.APIKeyID},
		Media:    *media, ExpiresAt: expires,
	}
	return s.previewCache.SaveVideo(ctx, record, time.Until(expires))
}

func (s *MediaTaskService) videoPreview(ctx context.Context, task *MediaTask, preview *MediaTaskPreview) (*MediaTaskPreview, error) {
	preview.UnavailableReason = "unavailable"
	if s.previewCache == nil {
		return preview, nil
	}
	identity := mediaTaskPreviewIdentity(task)
	record, err := s.previewCache.GetVideo(ctx, identity)
	if err != nil || record == nil || !sameMediaTaskPreviewIdentity(record.Identity, identity) || !safeMediaPreviewURL(record.Media.URL) {
		return preview, nil
	}
	expires := time.Now().UTC().Add(mediaTaskPreviewTicketTTL)
	for _, bound := range []*time.Time{task.ExpiresAt, &record.ExpiresAt} {
		if bound != nil && bound.Before(expires) {
			expires = *bound
		}
	}
	if !expires.After(time.Now()) {
		preview.UnavailableReason = "expired"
		return preview, nil
	}
	var secret [32]byte
	if _, err = rand.Read(secret[:]); err != nil {
		return nil, ErrMediaTaskPreviewUnavailable
	}
	token := hex.EncodeToString(secret[:])
	if err = s.previewCache.SaveTicket(ctx, token, &MediaTaskPreviewTicket{Identity: identity, ExpiresAt: expires}, time.Until(expires)); err != nil {
		return preview, nil
	}
	preview.Items = []MediaTaskPreviewItem{{URL: "/api/v1/media-tasks/preview-content/" + token, MediaType: "video", MimeType: record.Media.MimeType,
		Width: record.Media.Width, Height: record.Media.Height, DurationSeconds: record.Media.DurationSeconds, SizeBytes: record.Media.SizeBytes}}
	preview.ExpiresAt, preview.UnavailableReason = &expires, ""
	return preview, nil
}

// OpenPreviewContent 只下载票据绑定的缓存媒体地址，完全不访问模型状态接口或账务服务。
func (s *MediaTaskService) OpenPreviewContent(ctx context.Context, token, method, byteRange string) (*http.Response, error) {
	if s == nil || s.previewCache == nil || s.previewHTTP == nil || len(token) != 64 || (method != http.MethodGet && method != http.MethodHead) {
		return nil, ErrMediaTaskNotFound
	}
	if _, err := hex.DecodeString(token); err != nil {
		return nil, ErrMediaTaskNotFound
	}
	if byteRange != "" && (len(byteRange) > 100 || !mediaTaskPreviewRange.MatchString(byteRange)) {
		return nil, ErrMediaTaskInvalid
	}
	ticket, err := s.previewCache.GetTicket(ctx, token)
	if err != nil || ticket == nil || !ticket.ExpiresAt.After(time.Now()) {
		return nil, ErrMediaTaskNotFound
	}
	// 再读当前任务，删除任务或身份不一致后票据不能继续使用；票据不允许改目标。
	task, err := s.Get(ctx, MediaTaskActor{UserID: ticket.Identity.UserID}, ticket.Identity.ID)
	if err != nil || !sameMediaTaskPreviewIdentity(ticket.Identity, mediaTaskPreviewIdentity(task)) || task.Status != "completed" || (task.ExpiresAt != nil && !task.ExpiresAt.After(time.Now())) {
		return nil, ErrMediaTaskNotFound
	}
	record, err := s.previewCache.GetVideo(ctx, ticket.Identity)
	if err != nil || record == nil || !sameMediaTaskPreviewIdentity(record.Identity, ticket.Identity) || !record.ExpiresAt.After(time.Now()) || !safeMediaPreviewURL(record.Media.URL) {
		return nil, ErrMediaTaskNotFound
	}
	select {
	case s.previewSlots <- struct{}{}:
	default:
		return nil, ErrMediaTaskPreviewUnavailable
	}
	release := func() { <-s.previewSlots }
	req, err := http.NewRequestWithContext(ctx, method, record.Media.URL, nil)
	if err != nil || urlvalidator.IsBlockedHost(req.URL.Hostname()) {
		release()
		return nil, ErrMediaTaskPreviewUnavailable
	}
	// 只允许 Range 和固定 Accept；上游 API Key、Cookie 及面板 JWT 永不进入媒体请求。
	req.Header.Set("Accept", "video/*, application/octet-stream")
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}
	resp, err := s.previewHTTP.Do(req)
	if err != nil {
		release()
		return nil, ErrMediaTaskPreviewUnavailable
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusRequestedRangeNotSatisfiable || resp.ContentLength > mediaTaskPreviewMaxBytes {
		_ = resp.Body.Close()
		release()
		return nil, ErrMediaTaskPreviewUnavailable
	}
	contentType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if contentType == "application/octet-stream" {
		contentType = "video/mp4"
	}
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable && contentType != "video/mp4" && contentType != "video/webm" && contentType != "video/ogg" && contentType != "video/quicktime" {
		_ = resp.Body.Close()
		release()
		return nil, ErrMediaTaskPreviewUnavailable
	}
	resp.Header.Set("Content-Type", contentType)
	resp.Body = &mediaTaskPreviewBody{Reader: io.LimitReader(resp.Body, mediaTaskPreviewMaxBytes), closer: resp.Body, release: release}
	return resp, nil
}

// mediaTaskPreviewBody 随连接关闭归还本机下载槽，限制匿名票据流的同时下载量。
type mediaTaskPreviewBody struct {
	io.Reader
	closer  io.Closer
	release func()
}

func (b *mediaTaskPreviewBody) Close() error {
	if b.release == nil {
		return nil
	}
	err := b.closer.Close()
	b.release()
	b.release = nil
	return err
}
