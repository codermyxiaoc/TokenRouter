package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

// 内容请求全部使用内存 HTTP 替身，不访问真实生成或媒体上游。
type videoContentHTTPFixture struct {
	request  *http.Request
	response *http.Response
	calls    int
}

func (f *videoContentHTTPFixture) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	f.request = req
	f.calls++
	return f.response, nil
}

func (f *videoContentHTTPFixture) DoWithTLS(req *http.Request, proxy string, id int64, limit int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return f.Do(req, proxy, id, limit)
}

func newVideoContentFixture() (*VideoTaskService, *APIKey, *VideoTaskRecord, *videoLifecycleUpstream, *videoLifecycleBilling) {
	svc, key, upstream, billing, _ := newVideoLifecycleFixture(BillingModeVideoToken)
	task := &VideoTaskRecord{ID: "vid_content", APIKeyID: key.ID, UserID: key.UserID, AccountID: 11,
		Status: "completed", BillingStatus: "settled", UpstreamTaskID: "provider-id",
		Target: VideoUpstreamTarget{Version: 1, Endpoint: VideoEndpointOpenAIVideos, AccountID: 11, BaseURL: "https://provider.example/proxy/v1", CreatePath: "/v1/videos"}}
	svc.repo.(*videoLifecycleRepo).tasks[task.ID] = task
	svc.accounts = videoLifecycleAccounts{account: videoFixtureAccount(VideoEndpointOpenAIVideos)}
	return svc, key, task, upstream, billing
}

// 原任务归属与已结算状态是读取内容的门禁，不可通过其它 API Key 或团队成员绕过。
func TestVideoContentRequiresOriginalOwnerAndSettlement(t *testing.T) {
	for _, name := range []string{"other_key", "other_actor", "processing", "reconciliation", "failed"} {
		t.Run(name, func(t *testing.T) {
			svc, key, task, upstream, billing := newVideoContentFixture()
			switch name {
			case "other_key":
				key.ID++
			case "other_actor":
				key.ActorUser = &User{ID: key.UserID + 1}
			case "processing", "failed":
				task.Status = name
			case "reconciliation":
				task.BillingStatus = name
			}
			_, err := svc.OpenContent(context.Background(), key, task.ID, "openai_videos", "")
			require.Error(t, err)
			require.Zero(t, upstream.submitted+upstream.polls+upstream.cancels)
			require.Zero(t, billing.reserves+billing.captures+billing.releases)
		})
	}
}

// 已保存公共产物地址直接下载，复用现有受限媒体客户端而不再访问任务或凭据接口。
func TestVideoContentPublicURLStreamsRangeWithoutCredentials(t *testing.T) {
	svc, key, task, upstream, billing := newVideoContentFixture()
	task.VideoURL = "https://cdn.example/video.mp4?signature=public"
	var request *http.Request
	svc.contentHTTP = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		request = req
		return &http.Response{StatusCode: http.StatusPartialContent, ContentLength: 4,
			Header: http.Header{"Content-Type": {"video/mp4"}, "Content-Length": {"4"}, "Content-Range": {"bytes 1-4/8"}, "Accept-Ranges": {"bytes"},
				"Set-Cookie": {"secret"}, "Authorization": {"Bearer secret"}, "Location": {"https://private.example/secret"}, "X-Private": {"secret"}},
			Body: io.NopCloser(strings.NewReader("data"))}, nil
	})}
	response, err := svc.OpenContent(context.Background(), key, task.ID, "openai_videos", "bytes=1-4")
	require.NoError(t, err)
	require.Equal(t, http.MethodGet, request.Method)
	require.Equal(t, task.VideoURL, request.URL.String())
	require.Equal(t, "bytes=1-4", request.Header.Get("Range"))
	require.Empty(t, request.Header.Get("Authorization"))
	require.Empty(t, request.Header.Get("Cookie"))
	deadline, ok := request.Context().Deadline()
	require.True(t, ok)
	require.WithinDuration(t, time.Now().Add(2*time.Minute), deadline, time.Second)
	require.Equal(t, http.StatusPartialContent, response.StatusCode)
	require.Equal(t, "bytes 1-4/8", response.Header.Get("Content-Range"))
	require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	for _, name := range []string{"Set-Cookie", "Authorization", "Location", "X-Private"} {
		require.Empty(t, response.Header.Get(name))
	}
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "data", string(data))
	require.NoError(t, response.Body.Close())
	require.ErrorIs(t, request.Context().Err(), context.Canceled)
	require.Empty(t, svc.contentSlots)
	require.Zero(t, upstream.submitted+upstream.polls+upstream.cancels)
	require.Zero(t, billing.reserves+billing.captures+billing.releases)
}

// 固定内容路径继续使用原账号和冻结地址，响应按视频流处理并剥离上游私有头。
func TestVideoContentFrozenUpstreamPathDisablesCredentialRedirects(t *testing.T) {
	svc, key, task, _, billing := newVideoContentFixture()
	fixture := &videoContentHTTPFixture{response: &http.Response{StatusCode: http.StatusOK, ContentLength: 5,
		Header: http.Header{"Content-Type": {"application/octet-stream"}, "Set-Cookie": {"secret"}}, Body: io.NopCloser(strings.NewReader("video"))}}
	svc.upstream = NewVideoUpstreamService(&OpenAIGatewayService{httpUpstream: fixture, concurrencyService: NewConcurrencyService(nil)})
	response, err := svc.OpenContent(context.Background(), key, task.ID, "openai_videos", "bytes=0-")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, "https://provider.example/proxy/v1/videos/provider-id/content", fixture.request.URL.String())
	require.Equal(t, http.MethodGet, fixture.request.Method)
	require.Equal(t, "Bearer fixture-secret", fixture.request.Header.Get("Authorization"))
	require.Equal(t, "bytes=0-", fixture.request.Header.Get("Range"))
	require.True(t, HTTPUpstreamRedirectsDisabled(fixture.request.Context()))
	require.Equal(t, "video/mp4", response.Header.Get("Content-Type"))
	require.Empty(t, response.Header.Get("Set-Cookie"))
	require.Equal(t, 1, fixture.calls)
	require.Zero(t, billing.reserves+billing.captures+billing.releases)
}

// 带鉴权的 3xx 只提取受控公共目标；新的下载请求绝不继承原账号的认证头。
func TestVideoContentRedirectUsesPublicDownloader(t *testing.T) {
	for _, location := range []string{"https://cdn.example/video.mp4?signature=public", "/signed/video.mp4"} {
		t.Run(location, func(t *testing.T) {
			svc, key, task, _, _ := newVideoContentFixture()
			fixture := &videoContentHTTPFixture{response: &http.Response{StatusCode: http.StatusFound,
				Header: http.Header{"Location": {location}, "Set-Cookie": {"secret"}}, Body: io.NopCloser(strings.NewReader("secret"))}}
			svc.upstream = NewVideoUpstreamService(&OpenAIGatewayService{httpUpstream: fixture})
			calls := 0
			svc.contentHTTP = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				require.Empty(t, req.Header.Get("Authorization"))
				require.Empty(t, req.Header.Get("Cookie"))
				require.Equal(t, "bytes=4-", req.Header.Get("Range"))
				if strings.HasPrefix(location, "/") {
					require.Equal(t, "https://provider.example"+location, req.URL.String())
				} else {
					require.Equal(t, location, req.URL.String())
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"video/mp4"}}, Body: io.NopCloser(strings.NewReader("safe"))}, nil
			})}
			response, err := svc.OpenContent(context.Background(), key, task.ID, "openai_videos", "bytes=4-")
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, 1, calls)
			require.Equal(t, 1, fixture.calls)
			require.Empty(t, response.Header.Get("Location"))
		})
	}
}

// 非公网、含凭据和可执行地址不能进入公共下载器，包含重定向来源的同类地址。
func TestVideoContentRejectsUnsafeURLsAndRanges(t *testing.T) {
	for _, address := range []string{"http://127.0.0.1/private", "http://localhost/private", "http://[::1]/private", "https://user:pass@cdn.example/video", "file:///private"} {
		for _, redirected := range []bool{false, true} {
			svc, key, task, _, _ := newVideoContentFixture()
			if redirected {
				fixture := &videoContentHTTPFixture{response: &http.Response{StatusCode: 302, Header: http.Header{"Location": {address}}, Body: io.NopCloser(strings.NewReader(""))}}
				svc.upstream = NewVideoUpstreamService(&OpenAIGatewayService{httpUpstream: fixture})
			} else {
				task.VideoURL = address
			}
			svc.contentHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("unsafe address reached the download transport")
				return nil, nil
			})}
			_, err := svc.OpenContent(context.Background(), key, task.ID, "openai_videos", "")
			require.Error(t, err, address)
			require.Empty(t, svc.contentSlots)
		}
	}
	svc, key, task, _, _ := newVideoContentFixture()
	for _, value := range []string{"bytes=0-1,3-4", "items=0-1", "bytes=", "bytes=0-\r\nAuthorization: secret"} {
		_, err := svc.OpenContent(context.Background(), key, task.ID, "openai_videos", value)
		require.Error(t, err)
	}
}

// 错误、HTML 和超大流不能透传；416 仅暴露范围头，不回显供应商调试正文。
func TestVideoContentResponseBoundsAndRangeFailure(t *testing.T) {
	for _, tc := range []struct {
		status int
		mime   string
		length int64
	}{
		{200, "text/html", 10}, {401, "video/mp4", 10}, {200, "video/mp4", mediaTaskPreviewMaxBytes + 1},
	} {
		svc, key, task, _, _ := newVideoContentFixture()
		task.VideoURL = "https://cdn.example/video"
		svc.contentHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: tc.status, ContentLength: tc.length, Header: http.Header{"Content-Type": {tc.mime}}, Body: io.NopCloser(strings.NewReader("private error"))}, nil
		})}
		_, err := svc.OpenContent(context.Background(), key, task.ID, "openai_videos", "")
		require.Error(t, err)
		require.Empty(t, svc.contentSlots)
	}
	svc, key, task, _, _ := newVideoContentFixture()
	task.VideoURL = "https://cdn.example/video"
	svc.contentHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 416, Header: http.Header{"Content-Type": {"text/plain"}, "Content-Range": {"bytes */4"}}, Body: io.NopCloser(strings.NewReader("secret"))}, nil
	})}
	response, err := svc.OpenContent(context.Background(), key, task.ID, "openai_videos", "bytes=100-")
	require.NoError(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Empty(t, data)
	require.Equal(t, "bytes */4", response.Header.Get("Content-Range"))
	require.Equal(t, "0", response.Header.Get("Content-Length"))
}

// 未声明长度的上游流也必须受字节上限保护，测试体按需计数而不分配大视频内存。
type videoContentCountingBody struct {
	read   int64
	closed bool
}

func (b *videoContentCountingBody) Read(p []byte) (int, error) {
	b.read += int64(len(p))
	return len(p), nil
}

func (b *videoContentCountingBody) Close() error { b.closed = true; return nil }

func TestVideoContentUnknownLengthCannotExceedByteLimit(t *testing.T) {
	svc, key, task, _, _ := newVideoContentFixture()
	task.VideoURL = "https://cdn.example/video"
	source := &videoContentCountingBody{}
	svc.contentHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, ContentLength: -1, Header: http.Header{"Content-Type": {"video/mp4"}}, Body: source}, nil
	})}
	response, err := svc.OpenContent(context.Background(), key, task.ID, "openai_videos", "")
	require.NoError(t, err)
	written, err := io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.Equal(t, mediaTaskPreviewMaxBytes, written)
	require.Equal(t, mediaTaskPreviewMaxBytes, source.read)
	require.NoError(t, response.Body.Close())
	require.True(t, source.closed)
	require.Empty(t, svc.contentSlots)
}

// 达到下载并发上限时拒绝新流，关闭任意已有流后才允许新下载进入。
func TestVideoContentSlotsRemainHeldUntilBodyClosed(t *testing.T) {
	svc, key, task, _, _ := newVideoContentFixture()
	task.VideoURL = "https://cdn.example/video"
	calls := 0
	svc.contentHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"video/mp4"}}, Body: io.NopCloser(strings.NewReader("video"))}, nil
	})}
	var open []*http.Response
	t.Cleanup(func() {
		for _, response := range open {
			_ = response.Body.Close()
		}
	})
	for index := 0; index < 16; index++ {
		response, err := svc.OpenContent(context.Background(), key, task.ID, "openai_videos", "")
		require.NoError(t, err)
		open = append(open, response)
	}
	_, err := svc.OpenContent(context.Background(), key, task.ID, "openai_videos", "")
	require.Error(t, err)
	require.Equal(t, 16, calls)
	require.NoError(t, open[0].Body.Close())
	require.NoError(t, open[0].Body.Close())
	response, err := svc.OpenContent(context.Background(), key, task.ID, "openai_videos", "")
	require.NoError(t, err)
	open = append(open, response)
	require.Equal(t, 17, calls)
}

// 真实有限账号槽用原子计数模拟，验证取消和重复关闭都不会丢失或重复归还槽。
type videoContentSlotFixture struct {
	ConcurrencyCache
	acquired atomic.Int64
	released atomic.Int64
}

func (c *videoContentSlotFixture) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	c.acquired.Add(1)
	return true, nil
}

func (c *videoContentSlotFixture) ReleaseAccountSlot(context.Context, int64, string) error {
	c.released.Add(1)
	return nil
}

func TestVideoContentCancellationReleasesAccountAndDownloadSlots(t *testing.T) {
	svc, key, task, _, _ := newVideoContentFixture()
	account := videoFixtureAccount(VideoEndpointOpenAIVideos)
	account.Concurrency = 1
	svc.accounts = videoLifecycleAccounts{account: account}
	fixture := &videoContentHTTPFixture{response: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"video/mp4"}}, Body: io.NopCloser(strings.NewReader("video"))}}
	slots := &videoContentSlotFixture{}
	svc.upstream = NewVideoUpstreamService(&OpenAIGatewayService{httpUpstream: fixture, concurrencyService: NewConcurrencyService(slots)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	response, err := svc.OpenContent(ctx, key, task.ID, "openai_videos", "")
	require.NoError(t, err)
	require.Equal(t, int64(1), slots.acquired.Load())
	require.Zero(t, slots.released.Load())
	cancel()
	require.Eventually(t, func() bool { return slots.released.Load() == 1 && len(svc.contentSlots) == 0 }, time.Second, time.Millisecond)
	require.NoError(t, response.Body.Close())
	require.NoError(t, response.Body.Close())
	require.Equal(t, int64(1), slots.released.Load())
}
