package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 内容替身记录调用，验证下载不会误走生成或重复触发查询结算。
type videoContentLifecycleFixture struct {
	videoLifecycleFixture
	byteRange string
	closed    bool
}

type videoContentBodyFixture struct {
	io.Reader
	closed *bool
}

func (body *videoContentBodyFixture) Close() error { *body.closed = true; return nil }

func (f *videoContentLifecycleFixture) OpenContent(_ context.Context, _ *service.APIKey, id, protocol, byteRange string) (*http.Response, error) {
	f.id, f.protocol, f.byteRange = id, protocol, byteRange
	return &http.Response{StatusCode: 206, Header: http.Header{
		"Content-Type": {"video/mp4"}, "Content-Range": {"bytes 0-3/100"}, "Content-Length": {"4"},
		"Authorization": {"private"}, "Set-Cookie": {"private"}, "Location": {"https://private.example"}, "Cache-Control": {"public"},
	}, Body: &videoContentBodyFixture{Reader: strings.NewReader("film"), closed: &f.closed}}, nil
}

func TestOpenAIVideoContentStreamsSafeHeadersWithoutBilling(t *testing.T) {
	f := &videoContentLifecycleFixture{}
	c, w := videoHandlerContext(http.MethodGet, "/v1/videos/vid_owned/content", "", service.PlatformVideo)
	c.Request.Header.Set("Range", "bytes=0-3")
	NewVideoHandler(f).Handle(c)
	require.Equal(t, 206, w.Code)
	require.Equal(t, "film", w.Body.String())
	require.Equal(t, "bytes=0-3", f.byteRange)
	require.Equal(t, "openai_videos", f.protocol)
	require.Equal(t, "vid_owned", f.id)
	require.True(t, f.closed)
	require.Zero(t, f.creates+f.queries+f.cancels)
	require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
	require.Equal(t, "bytes 0-3/100", w.Header().Get("Content-Range"))
	for _, name := range []string{"Authorization", "Set-Cookie", "Location"} {
		require.Empty(t, w.Header().Get(name))
	}
}

func TestOpenAIVideoCreatePreservesJSONAndSelectsUnifiedProtocol(t *testing.T) {
	body := `{"model":"seedance-2.5","duration":10,"resolution":"720p","ratio":"16:9","images":["https://example.com/input.png"],"future":false}`
	f := &videoLifecycleFixture{response: &service.VideoTaskResponse{StatusCode: 200, Body: []byte(`{"id":"vid_owned","status":"queued"}`)}}
	c, w := videoHandlerContext(http.MethodPost, "/v1/videos", body, service.PlatformVideo)
	NewVideoHandler(f).Handle(c)
	require.Equal(t, 200, w.Code)
	require.Equal(t, 1, f.creates)
	require.Equal(t, "openai_videos", f.request.InboundProtocol)
	require.Equal(t, "/v1/videos", f.request.NativePath)
	require.False(t, f.request.Native)
	require.Equal(t, body, string(f.request.Body))
}

func TestOpenAIVideoMissingServiceDoesNotFallBackFromVideoGroup(t *testing.T) {
	c, w := videoHandlerContext(http.MethodGet, "/v1/videos/vid_owned", "", service.PlatformVideo)
	var legacyCalls int
	NewVideoHandler(nil).OpenAIVideos(c, func(c *gin.Context) { legacyCalls++; c.String(200, "legacy") })
	require.Equal(t, 503, w.Code)
	require.Zero(t, legacyCalls)
}

// 慢写替身验证新接口设置独立写超时，写失败仍关闭内容流，并清除连接上的临时期限。
type videoDeadlineWriterFixture struct {
	gin.ResponseWriter
	deadlines []time.Time
}

func (w *videoDeadlineWriterFixture) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func (w *videoDeadlineWriterFixture) Write(_ []byte) (int, error) {
	return 0, context.DeadlineExceeded
}

func TestOpenAIVideoContentWriteFailureClosesStreamAndClearsDeadline(t *testing.T) {
	f := &videoContentLifecycleFixture{}
	c, _ := videoHandlerContext(http.MethodGet, "/v1/videos/vid_owned/content", "", service.PlatformVideo)
	key, _ := middleware.GetAPIKeyFromContext(c)
	var w *videoDeadlineWriterFixture
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), key)
		w = &videoDeadlineWriterFixture{ResponseWriter: c.Writer}
		c.Writer = w
		c.Next()
	})
	// 使用真实运维包装链，防止直接调用 handler 掩盖 ResponseController 无法穿透的情况。
	router.Use(OpsErrorLoggerMiddleware(nil))
	router.GET("/v1/videos/:id/content", NewVideoHandler(f).Handle)
	start := time.Now()
	router.ServeHTTP(httptest.NewRecorder(), c.Request)
	require.True(t, f.closed)
	require.Zero(t, f.creates+f.queries+f.cancels)
	require.Len(t, w.deadlines, 2)
	require.WithinDuration(t, start.Add(2*time.Minute), w.deadlines[0], time.Second)
	require.True(t, w.deadlines[1].IsZero())
}
