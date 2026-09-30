package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 仅实现只读查询；若预览误入生成、写入或结算链路，嵌入接口的未实现方法会令测试失败。
type mediaPreviewRepo struct {
	MediaTaskRepository
	task       *MediaTask
	image      *ImageTaskRecord
	imageReads int
	filter     MediaTaskFilter
	actor      MediaTaskActor
}

func (r *mediaPreviewRepo) Get(_ context.Context, actor MediaTaskActor, id int64) (*MediaTask, error) {
	if r.task == nil || r.task.ID != id || (!actor.IsAdmin && r.task.UserID != actor.UserID) {
		return nil, ErrMediaTaskNotFound
	}
	copy := *r.task
	return &copy, nil
}
func (r *mediaPreviewRepo) GetImageResult(_ context.Context, _ *MediaTask) (*ImageTaskRecord, error) {
	r.imageReads++
	if r.image == nil {
		return nil, ErrImageTaskNotFound
	}
	return r.image, nil
}
func (r *mediaPreviewRepo) ListModels(_ context.Context, actor MediaTaskActor, filter MediaTaskFilter) ([]string, error) {
	r.filter, r.actor = filter, actor
	return []string{"image-a", "image-b"}, nil
}

func TestMediaTaskModelsKeepScopeAndIgnoreCurrentModelAndPage(t *testing.T) {
	r := &mediaPreviewRepo{}
	s := NewMediaTaskService(r)
	_, err := s.ListModels(context.Background(), MediaTaskActor{UserID: 7}, MediaTaskFilter{UserID: 9, Page: 999, Model: "image-a", MediaType: "image", Status: "completed", Source: "async_image"})
	require.NoError(t, err)
	require.EqualValues(t, 7, r.filter.UserID)
	require.Empty(t, r.filter.Model)
	require.Equal(t, "completed", r.filter.Status)
	require.Equal(t, "async_image", r.filter.Source)
	_, err = s.ListModels(context.Background(), MediaTaskActor{UserID: 1, IsAdmin: true}, MediaTaskFilter{UserID: 9})
	require.NoError(t, err)
	require.EqualValues(t, 9, r.filter.UserID)
	_, err = s.ListModels(context.Background(), MediaTaskActor{UserID: 1}, MediaTaskFilter{MediaType: "audio"})
	require.ErrorIs(t, err, ErrMediaTaskInvalid)
}

func TestMediaTaskImagePreviewAuthorizationExpiryAndWhitelist(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	r := &mediaPreviewRepo{task: &MediaTask{ID: 1, TaskID: "image-1", Source: "async_image", Status: "completed", UserID: 7, APIKeyID: 9, ExpiresAt: &expires}, image: &ImageTaskRecord{
		ID: "image-1", UserID: 7, APIKeyID: 9, Status: "completed", ExpiresAt: expires.Unix(), Result: json.RawMessage(`{"prompt":"secret prompt","usage":{"key":"secret-key"},"data":[{"url":"https://storage.example/image.png?signature=ok","revised_prompt":"private"},{"url":"javascript:alert(1)"},{"url":"data:image/png;base64,private"},{"url":"https://user:pass@storage.example/image.png"}]}`)}}
	s := NewMediaTaskService(r)
	_, err := s.Preview(context.Background(), MediaTaskActor{UserID: 8}, 1)
	require.ErrorIs(t, err, ErrMediaTaskNotFound)
	require.Zero(t, r.imageReads)
	for _, actor := range []MediaTaskActor{{UserID: 7}, {UserID: 8, IsAdmin: true}} {
		preview, err := s.Preview(context.Background(), actor, 1)
		require.NoError(t, err)
		require.Len(t, preview.Items, 1)
		require.Equal(t, "image", preview.Items[0].MediaType)
		body, err := json.Marshal(preview)
		require.NoError(t, err)
		require.NotContains(t, string(body), "secret")
		require.NotContains(t, string(body), "private")
	}
	r.image.UserID = 8
	preview, err := s.Preview(context.Background(), MediaTaskActor{UserID: 7}, 1)
	require.NoError(t, err)
	require.Equal(t, "unavailable", preview.UnavailableReason)
	past := time.Now().Add(-time.Second)
	r.task.ExpiresAt = &past
	preview, err = s.Preview(context.Background(), MediaTaskActor{UserID: 7}, 1)
	require.NoError(t, err)
	require.Equal(t, "expired", preview.UnavailableReason)
	r.task.ExpiresAt, r.task.Status = &expires, "processing"
	preview, err = s.Preview(context.Background(), MediaTaskActor{UserID: 7}, 1)
	require.NoError(t, err)
	require.Equal(t, "pending", preview.UnavailableReason)
}

type mediaPreviewCache struct {
	video   *MediaTaskVideoPreviewRecord
	tickets map[string]*MediaTaskPreviewTicket
}

func (c *mediaPreviewCache) SaveVideo(_ context.Context, video *MediaTaskVideoPreviewRecord, _ time.Duration) error {
	c.video = video
	return nil
}
func (c *mediaPreviewCache) GetVideo(_ context.Context, id MediaTaskPreviewIdentity) (*MediaTaskVideoPreviewRecord, error) {
	if c.video == nil || !sameMediaTaskPreviewIdentity(c.video.Identity, id) {
		return nil, ErrMediaTaskNotFound
	}
	return c.video, nil
}
func (c *mediaPreviewCache) SaveTicket(_ context.Context, token string, ticket *MediaTaskPreviewTicket, _ time.Duration) error {
	c.tickets[token] = ticket
	return nil
}
func (c *mediaPreviewCache) GetTicket(_ context.Context, token string) (*MediaTaskPreviewTicket, error) {
	return c.tickets[token], nil
}

func TestMediaTaskVideoPreviewTicketDownloadAndIsolation(t *testing.T) {
	ctx := context.Background()
	expires := time.Now().Add(time.Hour)
	r := &mediaPreviewRepo{task: &MediaTask{ID: 1, TaskID: "video-1", Source: "seedance_video", Status: "completed", UserID: 7, APIKeyID: 9, ExpiresAt: &expires}}
	cache := &mediaPreviewCache{tickets: map[string]*MediaTaskPreviewTicket{}}
	s := ProvideMediaTaskService(r, cache)
	preview, err := s.Preview(ctx, MediaTaskActor{UserID: 7}, 1)
	require.NoError(t, err)
	require.Equal(t, "unavailable", preview.UnavailableReason)
	require.NoError(t, s.ObserveMediaTaskVideoPreview(ctx, MediaTaskObservation{Source: "seedance_video", TaskID: "video-1", Status: "completed", UserID: 7, APIKeyID: 9, ExpiresAt: &expires}, &MediaTaskVideoSnapshot{URL: "https://cdn.example/result.mp4?signature=private", MimeType: "video/mp4", Width: 1920}))
	preview, err = s.Preview(ctx, MediaTaskActor{UserID: 7}, 1)
	require.NoError(t, err)
	require.Len(t, preview.Items, 1)
	require.NotContains(t, preview.Items[0].URL, "cdn.example")
	require.NotContains(t, preview.Items[0].URL, "signature")
	require.WithinDuration(t, time.Now().Add(10*time.Minute), *preview.ExpiresAt, time.Second)
	token := strings.TrimPrefix(preview.Items[0].URL, "/api/v1/media-tasks/preview-content/")
	require.Len(t, token, 64)
	requests := 0
	s.previewHTTP = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		require.Equal(t, http.MethodGet, req.Method)
		require.Equal(t, "bytes=0-3", req.Header.Get("Range"))
		require.Empty(t, req.Header.Get("Authorization"))
		require.Empty(t, req.Header.Get("Cookie"))
		return &http.Response{StatusCode: 206, Header: http.Header{"Content-Type": []string{"video/mp4"}, "Content-Range": []string{"bytes 0-3/100"}}, Body: io.NopCloser(strings.NewReader("test")), ContentLength: 4}, nil
	})}
	resp, err := s.OpenPreviewContent(ctx, token, http.MethodGet, "bytes=0-3")
	require.NoError(t, err)
	require.Equal(t, 206, resp.StatusCode)
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "test", string(data))
	require.NoError(t, resp.Body.Close())
	require.Empty(t, s.previewSlots)
	for _, invalid := range []struct{ token, rangeHeader string }{{"bad", ""}, {strings.Repeat("f", 64), ""}, {token, "bytes=0-1,3-4"}} {
		_, err = s.OpenPreviewContent(ctx, invalid.token, http.MethodGet, invalid.rangeHeader)
		require.Error(t, err)
	}
	require.Equal(t, 1, requests)
	cache.video.Media.URL = "http://127.0.0.1/private"
	_, err = s.OpenPreviewContent(ctx, token, http.MethodGet, "")
	require.ErrorIs(t, err, ErrMediaTaskPreviewUnavailable)
	require.Equal(t, 1, requests)
	cache.video.Media.URL = "https://cdn.example/result.mp4"
	cache.tickets[token].Identity.APIKeyID++
	_, err = s.OpenPreviewContent(ctx, token, http.MethodGet, "")
	require.ErrorIs(t, err, ErrMediaTaskNotFound)
	require.Equal(t, 1, requests)
}

type mediaPreviewTrackedBody struct {
	io.Reader
	closed bool
}

func (b *mediaPreviewTrackedBody) Close() error { b.closed = true; return nil }

// 文件代理在 HEAD、上游错误、容量拒绝和取消时都必须释放连接与并发槽，不能拖垮后续播放。
func TestMediaTaskPreviewContentResponseBoundariesAndSlotRelease(t *testing.T) {
	for _, tc := range []struct {
		name, method, mime string
		status             int
		size               int64
		transportErr       error
		full, reject       bool
	}{
		{name: "HEAD仅取头", method: http.MethodHead, mime: "video/mp4", status: 200, size: 4},
		{name: "416保留范围错误状态", method: http.MethodGet, mime: "text/html", status: 416, size: 4},
		{name: "拒绝HTML错误页", method: http.MethodGet, mime: "text/html", status: 200, size: 4, reject: true},
		{name: "拒绝超限响应", method: http.MethodGet, mime: "video/mp4", status: 200, size: mediaTaskPreviewMaxBytes + 1, reject: true},
		{name: "上游错误释放槽", method: http.MethodGet, mime: "video/mp4", status: 503, size: 4, reject: true},
		{name: "网络失败释放槽", method: http.MethodGet, transportErr: errors.New("connection failed"), reject: true},
		{name: "取消下载释放槽", method: http.MethodGet, transportErr: context.Canceled, reject: true},
		{name: "槽满拒绝且不发请求", method: http.MethodGet, full: true, reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expires := time.Now().Add(time.Hour)
			task := &MediaTask{ID: 1, TaskID: "video-1", Source: "seedance_video", Status: "completed", UserID: 7, APIKeyID: 9, ExpiresAt: &expires}
			identity := mediaTaskPreviewIdentity(task)
			token := strings.Repeat("a", 64)
			cache := &mediaPreviewCache{
				video:   &MediaTaskVideoPreviewRecord{Identity: identity, Media: MediaTaskVideoSnapshot{URL: "https://cdn.example/video.mp4"}, ExpiresAt: expires},
				tickets: map[string]*MediaTaskPreviewTicket{token: {Identity: identity, ExpiresAt: expires}},
			}
			s := ProvideMediaTaskService(&mediaPreviewRepo{task: task}, cache)
			body := &mediaPreviewTrackedBody{Reader: strings.NewReader("data")}
			calls := 0
			s.previewHTTP = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, tc.method, req.Method)
				if tc.transportErr != nil {
					return nil, tc.transportErr
				}
				return &http.Response{StatusCode: tc.status, ContentLength: tc.size, Header: http.Header{"Content-Type": []string{tc.mime}}, Body: body}, nil
			})}
			if tc.full {
				for range cap(s.previewSlots) {
					s.previewSlots <- struct{}{}
				}
			}
			resp, err := s.OpenPreviewContent(context.Background(), token, tc.method, "")
			if tc.reject {
				require.ErrorIs(t, err, ErrMediaTaskPreviewUnavailable)
				require.Nil(t, resp)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.status, resp.StatusCode)
				require.Len(t, s.previewSlots, 1)
				require.NoError(t, resp.Body.Close())
				require.NoError(t, resp.Body.Close(), "重复关闭必须幂等归还槽位")
			}
			if tc.full {
				require.Zero(t, calls)
				require.Len(t, s.previewSlots, cap(s.previewSlots), "不能释放其他请求持有的槽位")
			} else {
				require.Equal(t, 1, calls)
				require.Empty(t, s.previewSlots)
				if tc.transportErr == nil {
					require.True(t, body.closed, "成功和被拒绝响应都必须关闭上游响应体")
				}
			}
		})
	}
}
