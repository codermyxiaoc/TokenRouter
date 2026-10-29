package service

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/util/urlvalidator"
)

var errVideoContentUnavailable = infraerrors.New(http.StatusBadGateway, "VIDEO_CONTENT_UNAVAILABLE", "视频内容暂时不可用")

// 内容传输是可选能力，不改变旧协议替身及生成、查询、取消的接口契约。
type videoTaskContentTransport interface {
	OpenContent(context.Context, *Account, VideoUpstreamTarget, string, string) (*http.Response, error)
}

// OpenContent 只读取原 Key 和原用户已结算的产物，不查询任务、不生成、不补扣费用。
// @project-doc docs/interfaces/video_upstream.md#video_openai_content
func (s *VideoTaskService) OpenContent(ctx context.Context, key *APIKey, id, protocol, byteRange string) (*http.Response, error) {
	if s == nil || s.repo == nil || key == nil || key.ID <= 0 || usageActorUserID(key, key.User) <= 0 {
		return nil, ErrVideoTaskNotFound
	}
	if byteRange != "" && (len(byteRange) > 100 || !mediaTaskPreviewRange.MatchString(byteRange)) {
		return nil, infraerrors.BadRequest("VIDEO_CONTENT_RANGE_INVALID", "Video content requires a single byte range")
	}
	task, err := s.repo.Find(ctx, key.ID, usageActorUserID(key, key.User), id, protocol)
	if err != nil {
		return nil, err
	}
	// 仓储负责查找，服务再次确认归属和账务状态；完成观测本身不能证明已付费。
	if task == nil || task.APIKeyID != key.ID || task.UserID != usageActorUserID(key, key.User) {
		return nil, ErrVideoTaskNotFound
	}
	if task.Status != "completed" || task.BillingStatus != "settled" {
		return nil, ErrVideoTaskConflict
	}
	client, slots := s.videoContentClient()
	transport, _ := s.upstream.(videoTaskContentTransport)
	return openVideoTaskContent(ctx, task, byteRange, s.accounts, transport, client, slots)
}

// videoContentClient 懒加载内容下载客户端与并发槽，优先复用媒体预览的安全下载器。
func (s *VideoTaskService) videoContentClient() (*http.Client, chan struct{}) {
	s.contentOnce.Do(func() {
		if s.media != nil && s.media.previewHTTP != nil && s.media.previewSlots != nil {
			s.contentHTTP, s.contentSlots = s.media.previewHTTP, s.media.previewSlots
			return
		}
		if s.contentHTTP == nil {
			s.contentHTTP = defaultImageDownloadHTTPClient()
			s.contentHTTP.Timeout = 2 * time.Minute
		}
		if s.contentSlots == nil {
			s.contentSlots = make(chan struct{}, 16)
		}
	})
	return s.contentHTTP, s.contentSlots
}

// videoDeliveryProbeSlots 限制结算前可取回性探测的并发；探测只读 1 字节，独立于用户下载槽，
// 避免下载高峰把可交付的视频误判为不可取回。
var videoDeliveryProbeSlots = make(chan struct{}, 64)

// probeVideoContent 在扣费前确认成品真实可取回：沿用用户下载的同一安全路径（公网校验、凭据不跟随跳转、
// 只接受视频类型），只请求首字节。URL 格式正确或模板声明了内容路径都不能代替实际取回。
// @project-doc docs/domains/video_tasks.md#video_task_delivery_release
func (s *VideoTaskService) probeVideoContent(ctx context.Context, t *VideoTaskRecord) error {
	if t == nil {
		return errVideoContentUnavailable
	}
	client, _ := s.videoContentClient()
	transport, _ := s.upstream.(videoTaskContentTransport)
	// 探测发生在结算之前，按“已完成且已结算”的读取规则检查同一产物，不修改原记录。
	probe := *t
	probe.Status, probe.BillingStatus = "completed", "settled"
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), videoDeliveryProbeTimeout)
	defer cancel()
	response, err := openVideoTaskContent(work, &probe, "bytes=0-0", s.accounts, transport, client, videoDeliveryProbeSlots)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	// 416 只说明范围不满足，不能证明产物存在；只有 200/206 的视频内容算可取回。
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return errVideoContentUnavailable
	}
	// 状态码和类型不能证明有内容：必须实际读到至少一个字节，空正文或首读报错都视为不可取回。
	var first [1]byte
	n, readErr := io.ReadFull(response.Body, first[:])
	if n < 1 || (readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF)) {
		return errVideoContentUnavailable
	}
	return nil
}

// videoDeliveryProbeTimeout 是单次取回探测的上限；扣费在探测之后另起独立预算，慢探测不会让扣费上下文过期。
const videoDeliveryProbeTimeout = 30 * time.Second

// openVideoTaskContent 只读取已授权任务的产物，供站内 Key 与面板票据共用，不依赖生成、查询或账务服务。
func openVideoTaskContent(ctx context.Context, task *VideoTaskRecord, byteRange string, accounts AccountRepository,
	transport videoTaskContentTransport, client *http.Client, slots chan struct{}) (*http.Response, error) {
	if task == nil || task.Status != "completed" || task.BillingStatus != "settled" || client == nil || slots == nil {
		return nil, errVideoContentUnavailable
	}
	// 历史原包明确属于其它任务时，已保存的 URL 同样不能作为可信产物播放。
	if !videoStoredResponseMatchesTask(task) {
		return nil, errVideoTaskResponseMismatch
	}
	if byteRange != "" && (len(byteRange) > 100 || !mediaTaskPreviewRange.MatchString(byteRange)) {
		return nil, errVideoContentUnavailable
	}
	select {
	case slots <- struct{}{}:
	default:
		return nil, errVideoContentUnavailable
	}
	work, cancel := context.WithTimeout(ctx, 2*time.Minute)
	release := func() { cancel(); <-slots }
	var response *http.Response
	var err error
	if task.VideoURL != "" {
		response, err = openPublicVideoContent(work, client, task.VideoURL, byteRange)
	} else {
		if _, credentialed, _ := videoContentPath(task.Target, task.UpstreamTaskID); transport == nil || !credentialed || accounts == nil {
			release()
			return nil, errVideoContentUnavailable
		}
		account, accountErr := accounts.GetByID(work, task.AccountID)
		if accountErr != nil {
			release()
			return nil, errVideoContentUnavailable
		}
		response, err = transport.OpenContent(work, account, task.Target, task.UpstreamTaskID, byteRange)
		if err == nil && response != nil && response.Body != nil && videoContentRedirect(response.StatusCode) {
			// 凭据请求绝不跟随跳转；公开媒体另建空凭据请求并重新执行公共地址校验。
			location := response.Header.Get("Location")
			_ = response.Body.Close()
			path, _, pathErr := videoContentPath(task.Target, task.UpstreamTaskID)
			base, baseErr := url.Parse(videoEndpointURL(task.Target.BaseURL, path))
			target, targetErr := url.Parse(location)
			if pathErr != nil || baseErr != nil || targetErr != nil || strings.TrimSpace(location) == "" {
				err = errVideoContentUnavailable
			} else {
				response, err = openPublicVideoContent(work, client, base.ResolveReference(target).String(), byteRange)
			}
		}
	}
	if err != nil || response == nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		release()
		return nil, errVideoContentUnavailable
	}
	result, err := sanitizeVideoContentResponse(response, release)
	if err == nil {
		// 客户端断开或超时即主动归还下载及账号槽，不依赖下游写入先结束。
		context.AfterFunc(work, func() { _ = result.Body.Close() })
	}
	return result, err
}

// 复用预览下载器的固定 DNS 拨号与无代理策略，只传递白名单请求头。
func openPublicVideoContent(ctx context.Context, client *http.Client, address, byteRange string) (*http.Response, error) {
	if !safeMediaPreviewURL(address) {
		return nil, errVideoContentUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil || urlvalidator.IsBlockedHost(request.URL.Hostname()) {
		return nil, errVideoContentUnavailable
	}
	request.Header.Set("Accept", "video/*, application/octet-stream")
	if byteRange != "" {
		request.Header.Set("Range", byteRange)
	}
	return client.Do(request)
}

func videoContentRedirect(status int) bool {
	return status == http.StatusMovedPermanently || status == http.StatusFound || status == http.StatusSeeOther || status == http.StatusTemporaryRedirect || status == http.StatusPermanentRedirect
}

// 响应只保留播放必需的内容头，拒绝 HTML、错误正文、超大响应及任何凭据或跳转头。
func sanitizeVideoContentResponse(response *http.Response, release func()) (*http.Response, error) {
	fail := func() (*http.Response, error) {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		release()
		return nil, errVideoContentUnavailable
	}
	if response.Body == nil || response.ContentLength > mediaTaskPreviewMaxBytes ||
		(response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent && response.StatusCode != http.StatusRequestedRangeNotSatisfiable) {
		return fail()
	}
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if contentType == "application/octet-stream" {
		contentType = "video/mp4"
	}
	if response.StatusCode != http.StatusRequestedRangeNotSatisfiable && contentType != "video/mp4" && contentType != "video/webm" && contentType != "video/ogg" && contentType != "video/quicktime" {
		return fail()
	}
	header := make(http.Header)
	for _, name := range []string{"Content-Length", "Content-Range", "Accept-Ranges"} {
		if value := response.Header.Get(name); value != "" {
			header.Set(name, value)
		}
	}
	header.Set("Content-Type", contentType)
	header.Set("Cache-Control", "no-store")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	length, body := response.ContentLength, response.Body
	if response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		// Range 错误只返回范围头，不允许供应商正文带出认证或调试信息。
		_ = body.Close()
		body, length = io.NopCloser(strings.NewReader("")), 0
		header.Set("Content-Type", "video/mp4")
		header.Set("Content-Length", "0")
	}
	return &http.Response{StatusCode: response.StatusCode, Header: header, ContentLength: length,
		Body: &videoContentBody{Reader: io.LimitReader(body, mediaTaskPreviewMaxBytes), closer: body, release: release}}, nil
}

// 处理器关闭和上下文取消可能并发发生，底层流及其并发槽必须恰好释放一次。
type videoContentBody struct {
	io.Reader
	closer  io.Closer
	release func()
	once    sync.Once
	err     error
}

func (b *videoContentBody) Close() error {
	b.once.Do(func() {
		b.err = b.closer.Close()
		b.release()
	})
	return b.err
}
