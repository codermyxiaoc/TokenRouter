package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 只实现读取能力；预览若误入任务写入、生成或账务链路，未装配的依赖会直接令测试失败。
type mediaVideoContentPreviewRepo struct {
	mediaPreviewRepo
	video      *VideoTaskRecord
	videoReads int
}

func (r *mediaVideoContentPreviewRepo) GetVideoTask(context.Context, *MediaTask) (*VideoTaskRecord, error) {
	r.videoReads++
	if r.video == nil {
		return nil, ErrVideoTaskNotFound
	}
	// 特意不在仓储替身校验归属，确保服务仍独立核对持久记录的身份。
	copy := *r.video
	return &copy, nil
}

func newMediaVideoContentPreviewFixture(t *testing.T) (*MediaTaskService, *mediaVideoContentPreviewRepo, *mediaPreviewCache, *videoContentHTTPFixture) {
	t.Helper()
	completed := time.Now().UTC().Add(-time.Hour)
	r := &mediaVideoContentPreviewRepo{
		mediaPreviewRepo: mediaPreviewRepo{task: &MediaTask{ID: 1, TaskID: "vid_preview_content", Source: "video", Status: "completed", UserID: 7, APIKeyID: 9}},
		video: &VideoTaskRecord{ID: "vid_preview_content", UserID: 7, APIKeyID: 9, AccountID: 11,
			Status: "completed", BillingStatus: "settled", CompletedAt: &completed, UpstreamTaskID: "provider-id",
			Target: VideoUpstreamTarget{Version: 1, Endpoint: VideoEndpointOpenAIVideos, AccountID: 11,
				BaseURL: "https://provider.example/proxy/v1", CreatePath: "/v1/videos"},
			Metadata: VideoRequestMetadata{DurationSeconds: 8}},
	}
	cache := &mediaPreviewCache{tickets: map[string]*MediaTaskPreviewTicket{}}
	upstream := &videoContentHTTPFixture{response: &http.Response{StatusCode: http.StatusPartialContent, ContentLength: 4,
		Header: http.Header{"Content-Type": {"application/octet-stream"}, "Content-Length": {"4"}, "Content-Range": {"bytes 1-4/8"},
			"Accept-Ranges": {"bytes"}, "Set-Cookie": {"private-cookie"}, "Authorization": {"Bearer private-token"},
			"Location": {"https://private.example/secret"}, "X-Private": {"private-header"}},
		Body: io.NopCloser(strings.NewReader("data"))}}
	transport := NewVideoUpstreamService(&OpenAIGatewayService{httpUpstream: upstream, concurrencyService: NewConcurrencyService(nil)})
	s := ProvideMediaTaskServiceWithVideoContent(r, cache, transport, videoLifecycleAccounts{account: videoFixtureAccount(VideoEndpointOpenAIVideos)})
	s.previewHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("无直链预览不得自行猜测公共媒体地址")
		return nil, nil
	})}
	return s, r, cache, upstream
}

func mediaVideoContentPreviewToken(t *testing.T, s *MediaTaskService, actor MediaTaskActor) string {
	t.Helper()
	preview, err := s.Preview(context.Background(), actor, 1)
	require.NoError(t, err)
	require.Empty(t, preview.UnavailableReason)
	require.Len(t, preview.Items, 1)
	require.Equal(t, "video", preview.Items[0].MediaType)
	require.Equal(t, "video/mp4", preview.Items[0].MimeType)
	require.Equal(t, float64(8), preview.Items[0].DurationSeconds)
	require.NotNil(t, preview.ExpiresAt)
	require.WithinDuration(t, time.Now().Add(mediaTaskPreviewTicketTTL), *preview.ExpiresAt, time.Second)
	encoded, err := json.Marshal(preview)
	require.NoError(t, err)
	for _, secret := range []string{"provider.example", "provider-id", "fixture-secret", "use_content_endpoint", "base_url", "api_key"} {
		require.NotContains(t, string(encoded), secret)
	}
	prefix := "/api/v1/media-tasks/preview-content/"
	require.True(t, strings.HasPrefix(preview.Items[0].URL, prefix))
	token := strings.TrimPrefix(preview.Items[0].URL, prefix)
	require.Len(t, token, 64)
	return token
}

// 面板用户与管理员都可取得本站票据；仅实际播放访问固定内容路径，不触发状态查询或生成。
func TestMediaTaskVideoContentPreviewAuthorizationAndRange(t *testing.T) {
	for _, actor := range []MediaTaskActor{{UserID: 7}, {UserID: 8, IsAdmin: true}} {
		t.Run(map[bool]string{false: "owner", true: "admin"}[actor.IsAdmin], func(t *testing.T) {
			s, repo, cache, upstream := newMediaVideoContentPreviewFixture(t)
			token := mediaVideoContentPreviewToken(t, s, actor)
			require.Zero(t, upstream.calls, "签票据不得调用上游")
			require.Len(t, cache.tickets, 1)
			record, err := s.videoPreviewRecord(context.Background(), mediaTaskPreviewIdentity(repo.task))
			require.NoError(t, err)
			require.NotNil(t, record)
			require.True(t, record.UseContentEndpoint)
			require.Empty(t, record.Media.URL, "缓存只保存服务端能力标志，不生成凭据 URL")
			response, err := s.OpenPreviewContent(context.Background(), token, http.MethodGet, "bytes=1-4")
			require.NoError(t, err)
			t.Cleanup(func() { _ = response.Body.Close() })
			require.Equal(t, 1, upstream.calls, "播放仅能发出一次内容请求")
			require.Equal(t, http.MethodGet, upstream.request.Method)
			require.Equal(t, "https://provider.example/proxy/v1/videos/provider-id/content", upstream.request.URL.String())
			require.Equal(t, "bytes=1-4", upstream.request.Header.Get("Range"))
			require.Equal(t, "Bearer fixture-secret", upstream.request.Header.Get("Authorization"))
			require.Empty(t, upstream.request.Header.Get("Cookie"))
			require.True(t, HTTPUpstreamRedirectsDisabled(upstream.request.Context()))
			require.Equal(t, http.StatusPartialContent, response.StatusCode)
			require.Equal(t, "bytes 1-4/8", response.Header.Get("Content-Range"))
			require.Equal(t, "video/mp4", response.Header.Get("Content-Type"))
			require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
			for _, name := range []string{"Authorization", "Set-Cookie", "Location", "X-Private"} {
				require.Empty(t, response.Header.Get(name))
			}
			data, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, "data", string(data))
			require.NoError(t, response.Body.Close())
			require.NoError(t, response.Body.Close())
			require.Empty(t, s.previewSlots)
			require.Zero(t, repo.imageReads)
		})
	}
	t.Run("other_owner", func(t *testing.T) {
		s, repo, cache, upstream := newMediaVideoContentPreviewFixture(t)
		_, err := s.Preview(context.Background(), MediaTaskActor{UserID: 8}, 1)
		require.ErrorIs(t, err, ErrMediaTaskNotFound)
		require.Zero(t, repo.videoReads)
		require.Empty(t, cache.tickets)
		require.Zero(t, upstream.calls)
	})
}

// HEAD 可向供应商使用 GET 取头；处理器跳过正文并关闭服务返回的流后，必须归还连接与下载槽。
func TestMediaTaskVideoContentPreviewHeadClosesBody(t *testing.T) {
	s, _, _, upstream := newMediaVideoContentPreviewFixture(t)
	token := mediaVideoContentPreviewToken(t, s, MediaTaskActor{UserID: 7})
	body := &mediaPreviewTrackedBody{Reader: strings.NewReader("data")}
	upstream.response.Body = body
	response, err := s.OpenPreviewContent(context.Background(), token, http.MethodHead, "bytes=1-4")
	require.NoError(t, err)
	require.Equal(t, http.StatusPartialContent, response.StatusCode)
	require.Equal(t, "bytes 1-4/8", response.Header.Get("Content-Range"))
	require.Equal(t, int64(4), response.ContentLength)
	require.Equal(t, http.MethodGet, upstream.request.Method)
	require.Equal(t, 1, upstream.calls)
	// 模拟处理器的 HEAD 分支：仅读取响应头，不读取正文，仍执行关闭。
	require.NoError(t, response.Body.Close())
	require.True(t, body.closed)
	require.Empty(t, s.previewSlots)
}

// 临近完成后 24 小时的任务不能通过新签票据延长内容保留期。
func TestMediaTaskVideoContentPreviewTicketRespectsResultExpiry(t *testing.T) {
	s, repo, cache, upstream := newMediaVideoContentPreviewFixture(t)
	completed := time.Now().UTC().Add(-24*time.Hour + 3*time.Minute)
	repo.video.CompletedAt = &completed
	preview, err := s.Preview(context.Background(), MediaTaskActor{UserID: 7}, 1)
	require.NoError(t, err)
	require.Len(t, preview.Items, 1)
	require.NotNil(t, preview.ExpiresAt)
	require.Equal(t, completed.Add(24*time.Hour), *preview.ExpiresAt)
	require.Len(t, cache.tickets, 1)
	for _, ticket := range cache.tickets {
		require.Equal(t, *preview.ExpiresAt, ticket.ExpiresAt)
	}
	require.Zero(t, upstream.calls)
}

// 只有原身份、已结算且仍在完成后保留期内的 OpenAI Videos 任务可启用无直链预览。
func TestMediaTaskVideoContentPreviewRejectsIneligibleRecords(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*VideoTaskRecord)
	}{
		{"processing", func(v *VideoTaskRecord) { v.Status = "processing" }},
		{"failed", func(v *VideoTaskRecord) { v.Status = "failed" }},
		{"unsettled", func(v *VideoTaskRecord) { v.BillingStatus = "reconciliation" }},
		{"no_completion", func(v *VideoTaskRecord) { v.CompletedAt = nil }},
		{"expired", func(v *VideoTaskRecord) { past := time.Now().Add(-25 * time.Hour); v.CompletedAt = &past }},
		{"unknown_endpoint", func(v *VideoTaskRecord) { v.Target.Endpoint = VideoEndpoint("unknown") }},
		{"other_endpoint", func(v *VideoTaskRecord) { v.Target.Endpoint = VideoEndpointSeedance }},
		{"unsupported_target_version", func(v *VideoTaskRecord) { v.Target.Version++ }},
		{"target_account_mismatch", func(v *VideoTaskRecord) { v.Target.AccountID++ }},
		{"empty_base_url", func(v *VideoTaskRecord) { v.Target.BaseURL = "" }},
		{"empty_upstream_id", func(v *VideoTaskRecord) { v.UpstreamTaskID = "" }},
		{"unsafe_upstream_id", func(v *VideoTaskRecord) { v.UpstreamTaskID = "../private?token=secret" }},
		{"other_task", func(v *VideoTaskRecord) { v.ID = "vid_other" }},
		{"other_user", func(v *VideoTaskRecord) { v.UserID++ }},
		{"other_key", func(v *VideoTaskRecord) { v.APIKeyID++ }},
		{"unsafe_saved_url", func(v *VideoTaskRecord) { v.VideoURL = "file:///private" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, cache, upstream := newMediaVideoContentPreviewFixture(t)
			tc.mutate(repo.video)
			preview, err := s.Preview(context.Background(), MediaTaskActor{UserID: 7}, 1)
			require.NoError(t, err)
			require.Empty(t, preview.Items)
			require.NotEmpty(t, preview.UnavailableReason)
			if tc.name == "expired" {
				require.Equal(t, "expired", preview.UnavailableReason, "已有产物的保留期结束不能提示为未保存链接")
			}
			require.Empty(t, cache.tickets)
			require.Zero(t, upstream.calls)
		})
	}
}

// 已签发票据与缓存标志都不能替代播放时的持久状态、账务状态和身份复核。
func TestMediaTaskVideoContentPreviewRechecksPersistedRecord(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*mediaVideoContentPreviewRepo)
	}{
		{"deleted", func(r *mediaVideoContentPreviewRepo) { r.video = nil }},
		{"unsettled", func(r *mediaVideoContentPreviewRepo) { r.video.BillingStatus = "reconciliation" }},
		{"not_completed", func(r *mediaVideoContentPreviewRepo) { r.video.Status = "processing" }},
		{"expired", func(r *mediaVideoContentPreviewRepo) {
			past := time.Now().Add(-25 * time.Hour)
			r.video.CompletedAt = &past
		}},
		{"other_user", func(r *mediaVideoContentPreviewRepo) { r.video.UserID++ }},
		{"other_key", func(r *mediaVideoContentPreviewRepo) { r.video.APIKeyID++ }},
		{"other_task", func(r *mediaVideoContentPreviewRepo) { r.video.ID = "vid_other" }},
		{"changed_endpoint", func(r *mediaVideoContentPreviewRepo) { r.video.Target.Endpoint = VideoEndpointSeedance }},
		{"changed_target_version", func(r *mediaVideoContentPreviewRepo) { r.video.Target.Version++ }},
		{"target_account_mismatch", func(r *mediaVideoContentPreviewRepo) { r.video.Target.AccountID++ }},
		{"empty_base_url", func(r *mediaVideoContentPreviewRepo) { r.video.Target.BaseURL = "" }},
		{"unsafe_upstream_id", func(r *mediaVideoContentPreviewRepo) { r.video.UpstreamTaskID = "../private" }},
		{"projection_deleted", func(r *mediaVideoContentPreviewRepo) { r.task = nil }},
		{"projection_user_changed", func(r *mediaVideoContentPreviewRepo) { r.task.UserID++ }},
		{"projection_key_changed", func(r *mediaVideoContentPreviewRepo) { r.task.APIKeyID++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, cache, upstream := newMediaVideoContentPreviewFixture(t)
			token := mediaVideoContentPreviewToken(t, s, MediaTaskActor{UserID: 7})
			record, err := s.videoPreviewRecord(context.Background(), mediaTaskPreviewIdentity(repo.task))
			require.NoError(t, err)
			require.NotNil(t, record)
			cache.video = record
			tc.mutate(repo)
			response, err := s.OpenPreviewContent(context.Background(), token, http.MethodGet, "")
			require.Error(t, err)
			require.Nil(t, response)
			require.Zero(t, upstream.calls, "失效后必须在访问上游之前拒绝")
			require.Empty(t, s.previewSlots)
		})
	}
}

// Redis 中旧的安全直链仍不能绕过独立视频的持久已结算检查，无论重新签票据还是使用旧票据。
func TestMediaTaskVideoContentPreviewCachedURLCannotBypassSettlement(t *testing.T) {
	s, repo, cache, upstream := newMediaVideoContentPreviewFixture(t)
	token := mediaVideoContentPreviewToken(t, s, MediaTaskActor{UserID: 7})
	cache.video = &MediaTaskVideoPreviewRecord{Identity: mediaTaskPreviewIdentity(repo.task),
		Media:     MediaTaskVideoSnapshot{URL: "https://cdn.example/cached-video.mp4", MimeType: "video/mp4"},
		ExpiresAt: time.Now().Add(time.Hour)}
	repo.video.VideoURL = "https://cdn.example/persisted-video.mp4"
	repo.video.BillingStatus = "reconciliation"
	preview, err := s.Preview(context.Background(), MediaTaskActor{UserID: 7}, 1)
	require.NoError(t, err)
	require.Empty(t, preview.Items)
	require.NotEmpty(t, preview.UnavailableReason)
	require.Len(t, cache.tickets, 1, "待核对任务不能再签新票据")
	response, err := s.OpenPreviewContent(context.Background(), token, http.MethodGet, "")
	require.Error(t, err)
	require.Nil(t, response)
	require.Zero(t, upstream.calls)
	require.Empty(t, s.previewSlots)
}
