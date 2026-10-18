//go:build unit

package routes

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func creativeTextBridgeTestKey() *service.APIKey {
	managed := service.CreativeManagedBy
	return &service.APIKey{Key: "test-only-managed-key", ManagedBy: &managed}
}

// 用真正的 Gin 路由验证桥接方法、鉴权和字节响应，不监听端口、不调用上游模型。
func TestCreativeTextBridgePreservesRequestAndResponse(t *testing.T) {
	calls := 0
	body := []byte(`{"model":"text-model","messages":[{"role":"user","content":"你好"}],"stream":false}`)
	adapter := newCreativeTextGatewayAdapter(func(c *gin.Context) {
		require.Equal(t, "Bearer test-only-managed-key", c.GetHeader("Authorization"))
		require.Equal(t, "application/json", c.GetHeader("Content-Type"))
		require.Equal(t, "/v1/chat/completions", c.Request.URL.Path)
		data, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.Equal(t, body, data)
		c.Next()
	}, func(c *gin.Context) {
		calls++
		c.Data(http.StatusOK, "application/json", []byte(`{"choices":[{"message":{"content":"回答"}}]}`))
	})
	got, err := adapter.Generate(context.Background(), creativeTextBridgeTestKey(), body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, got.StatusCode)
	require.Contains(t, string(got.Body), "回答")
	require.Equal(t, 1, calls)
}

func TestCreativeTextBridgeRejectsUnmanagedAndHonorsAdmission(t *testing.T) {
	calls := 0
	adapter := newCreativeTextGatewayAdapter(func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "group denied"})
	}, func(c *gin.Context) { calls++ })
	_, err := adapter.Generate(context.Background(), &service.APIKey{Key: "ordinary-key"}, nil)
	require.ErrorIs(t, err, service.ErrCanvasUnavailable)
	got, err := adapter.Generate(context.Background(), creativeTextBridgeTestKey(), []byte(`{}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, got.StatusCode)
	require.Contains(t, string(got.Body), "group denied")
	require.Zero(t, calls)
}

func TestCreativeTextBridgeBoundsResponseAndCancelsProducer(t *testing.T) {
	adapter := newCreativeTextGatewayAdapter(func(c *gin.Context) {
		_, err := c.Writer.Write(bytes.Repeat([]byte("x"), creativeTextResponseLimit+1))
		require.Error(t, err)
		require.ErrorIs(t, c.Request.Context().Err(), context.Canceled)
	})
	got, err := adapter.Generate(context.Background(), creativeTextBridgeTestKey(), []byte(`{}`))
	require.Error(t, err)
	require.Empty(t, got.Body)
}

func TestCreativeTextResponseWriterStatusAndSnapshots(t *testing.T) {
	w := &creativeTextResponseWriter{header: make(http.Header)}
	w.WriteHeader(http.StatusContinue)
	w.WriteHeader(http.StatusCreated)
	w.Flush()
	_, err := w.Write([]byte("done"))
	require.NoError(t, err)
	status, body := w.snapshot()
	require.Equal(t, http.StatusCreated, status)
	body[0] = 'x'
	_, original := w.snapshot()
	require.Equal(t, "done", string(original))
}
