//go:build unit

package routes

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 私有桥经过真实 Gin 路由与准入中间件；测试从不监听端口或使用真实模型。
func TestCreativeGrokVideoBridgeRequestAndAdmission(t *testing.T) {
	calls := 0
	adapter := newCreativeGrokVideoGatewayAdapter(nil, []gin.HandlerFunc{func(c *gin.Context) {
		require.Equal(t, "Bearer test-only-managed-key", c.GetHeader("Authorization"))
		require.Equal(t, "TokenRouter-Canvas", c.GetHeader("User-Agent"))
		c.Next()
	}}, func(c *gin.Context) {
		calls++
		require.Equal(t, "/v1/videos/generations", c.Request.URL.Path)
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"model":"grok-imagine-video","prompt":"ball"}`, string(body))
		c.JSON(http.StatusAccepted, gin.H{"request_id": "task-one"})
	}, func(c *gin.Context) {})
	response, err := adapter.Generate(context.Background(), creativeTextBridgeTestKey(), []byte(`{"model":"grok-imagine-video","prompt":"ball"}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, response.StatusCode)
	require.Contains(t, string(response.Body), "task-one")
	require.Equal(t, 1, calls)
	_, err = adapter.Generate(context.Background(), &service.APIKey{Key: "public-key"}, nil)
	require.ErrorIs(t, err, service.ErrCanvasUnavailable)
	require.Equal(t, 1, calls)
	blocked := newCreativeGrokVideoGatewayAdapter(nil, []gin.HandlerFunc{func(c *gin.Context) { c.AbortWithStatus(http.StatusForbidden) }}, func(c *gin.Context) { calls++ }, func(c *gin.Context) {})
	response, err = blocked.Generate(context.Background(), creativeTextBridgeTestKey(), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	require.Equal(t, 1, calls)
}

func TestCreativeGrokVideoBridgeStreamsDownloadWithoutBuffering(t *testing.T) {
	release := make(chan struct{})
	done := make(chan struct{})
	adapter := newCreativeGrokVideoGatewayAdapter(nil, nil, func(c *gin.Context) {}, func(c *gin.Context) {
		defer close(done)
		require.Equal(t, "task-one", c.Param("request_id"))
		c.Header("Content-Type", "video/mp4")
		c.Header("Content-Length", "11")
		c.Status(http.StatusOK)
		c.Writer.Flush()
		<-release
		_, _ = c.Writer.Write([]byte("test-video!"))
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	response, err := adapter.OpenContent(ctx, creativeTextBridgeTestKey(), "task-one")
	require.NoError(t, err)
	require.Equal(t, "video/mp4", response.Header.Get("Content-Type"))
	require.Equal(t, int64(11), response.ContentLength)
	// 元数据先返回，随后逐块读取，证明没有等待完整视频进入内存。
	close(release)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "test-video!", string(body))
	require.NoError(t, response.Body.Close())
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("download handler did not terminate")
	}
}

func TestCreativeGrokVideoBridgeClosingDownloadCancelsProducer(t *testing.T) {
	done := make(chan struct{})
	adapter := newCreativeGrokVideoGatewayAdapter(nil, nil, func(c *gin.Context) {}, func(c *gin.Context) {
		defer close(done)
		c.Header("Content-Type", "video/mp4")
		c.Writer.Flush()
		_, _ = io.Copy(c.Writer, strings.NewReader(strings.Repeat("x", 1<<20)))
		<-c.Request.Context().Done()
	})
	response, err := adapter.OpenContent(context.Background(), creativeTextBridgeTestKey(), "task-one")
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("closed download left a producer blocked")
	}
}

func TestCreativeGrokVideoBridgeContentLimitReturnsStreamError(t *testing.T) {
	reader, pipe := io.Pipe()
	writer := &creativeGrokVideoStreamWriter{header: make(http.Header), pipe: pipe, ready: make(chan *http.Response, 1), written: service.CreativeCanvasOutputLimit}
	_, err := writer.Write([]byte("x"))
	require.Error(t, err)
	require.Error(t, writer.err)
	require.NoError(t, reader.Close())
	require.NoError(t, pipe.Close())
}

func TestCreativeGrokVideoBridgeRejectsChangedGroupPlatform(t *testing.T) {
	for _, platform := range []string{service.PlatformGrok, service.PlatformVideo, service.PlatformOpenAI, ""} {
		t.Run(platform, func(t *testing.T) {
			calls := 0
			common := []gin.HandlerFunc{func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Group: &service.Group{Platform: platform}})
				c.Next()
			}, requireCreativeGrokVideoPlatform}
			adapter := newCreativeGrokVideoGatewayAdapter(nil, common, func(c *gin.Context) { calls++; c.JSON(202, gin.H{"request_id": "task"}) }, func(c *gin.Context) {})
			result, err := adapter.Generate(context.Background(), creativeTextBridgeTestKey(), []byte(`{}`))
			require.NoError(t, err)
			if platform == service.PlatformGrok {
				require.Equal(t, 1, calls)
				require.Equal(t, 202, result.StatusCode)
			} else {
				require.Zero(t, calls)
				require.Equal(t, 403, result.StatusCode)
			}
		})
	}
}
