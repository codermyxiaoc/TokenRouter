package service

import (
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 同一响应经历排队、普通保活和多次 Compact 尝试，历史心跳必须只扣除一次。
func TestGatewayStreamHeartbeat_MixedAndRepeatedKeepalive(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	n, err := c.Writer.Write([]byte(": queue\n\n"))
	require.NoError(t, err)
	RecordGatewayStreamHeartbeat(c, n)
	n, err = c.Writer.Write([]byte(":\n\n"))
	require.NoError(t, err)
	recordOpenAIStreamKeepaliveBytes(c, n)

	for range 3 {
		stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
		value, ok := c.Get(openAICompactSSEKeepaliveKey)
		require.True(t, ok)
		require.True(t, value.(*openAICompactSSEKeepalive).beat())
		require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))
		require.True(t, GatewayStreamHasOnlyHeartbeats(c))
		stop()
	}

	payload := "data: {\"delta\":\"hello\"}\n\n"
	_, err = c.Writer.Write([]byte(payload))
	require.NoError(t, err)
	require.Equal(t, len(payload), OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.False(t, GatewayStreamHasOnlyHeartbeats(c))
	require.Contains(t, rec.Body.String(), payload)
}

// 复制上下文后换新写入器，旧心跳不能抵消新分组真实输出，旧上下文本身仍保持原计数。
func TestGatewayStreamHeartbeat_ResetCopiedAttempt(t *testing.T) {
	c, _ := newCompactBridgeTestContext(t, true)
	n, err := c.Writer.Write([]byte(": queued long heartbeat\n\n"))
	require.NoError(t, err)
	RecordGatewayStreamHeartbeat(c, n)
	stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
	defer stop()
	value, _ := c.Get(openAICompactSSEKeepaliveKey)
	require.True(t, value.(*openAICompactSSEKeepalive).beat())

	next := c.Copy()
	fresh, _ := gin.CreateTestContext(httptest.NewRecorder())
	next.Writer = fresh.Writer
	ResetGatewayStreamOutputAccounting(next)
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(next))
	_, err = next.Writer.Write([]byte("data: x\n\n"))
	require.NoError(t, err)
	require.Equal(t, len("data: x\n\n"), OpenAICompactKeepaliveAdjustedWrittenSize(next))
	require.False(t, GatewayStreamHasOnlyHeartbeats(next))
	stop()
	require.Same(t, fresh.Writer, next.Writer)
	require.True(t, GatewayStreamHasOnlyHeartbeats(c))
}

// 旧停止闭包晚于新心跳器执行时，不能停止新拍、重复扣除或恢复已过期包装器。
func TestGatewayStreamHeartbeat_ReplacingActiveKeepalive(t *testing.T) {
	c, _ := newCompactBridgeTestContext(t, true)
	original := c.Writer
	stopFirst := StartOpenAICompactSSEKeepalive(c, time.Hour)
	value, _ := c.Get(openAICompactSSEKeepaliveKey)
	first := value.(*openAICompactSSEKeepalive)
	require.True(t, first.beat())
	stopSecond := StartOpenAICompactSSEKeepalive(c, time.Hour)
	value, _ = c.Get(openAICompactSSEKeepaliveKey)
	second := value.(*openAICompactSSEKeepalive)
	require.False(t, first.beat())
	require.True(t, second.beat())
	stopFirst()
	require.True(t, second.beat())
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))
	_, err := c.Writer.Write([]byte("data: output\n\n"))
	require.NoError(t, err)
	require.False(t, second.beat())
	stopSecond()
	require.Same(t, original, c.Writer)
	require.Equal(t, len("data: output\n\n"), OpenAICompactKeepaliveAdjustedWrittenSize(c))
}

// 模拟传输层部分写成功，统计只扣实际 n，随后业务字节仍可阻止重试。
type partialGatewayHeartbeatWriter struct{ gin.ResponseWriter }

func (w *partialGatewayHeartbeatWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p[:min(2, len(p))])
	if err != nil {
		return n, err
	}
	return n, io.ErrShortWrite
}

func TestGatewayStreamHeartbeat_PartialWriteAndInvalidCount(t *testing.T) {
	c, _ := newCompactBridgeTestContext(t, false)
	original := c.Writer
	c.Writer = &partialGatewayHeartbeatWriter{ResponseWriter: original}
	n, err := c.Writer.Write([]byte(": heartbeat\n\n"))
	require.ErrorIs(t, err, io.ErrShortWrite)
	RecordGatewayStreamHeartbeat(c, n)
	require.Equal(t, 2, n)
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))
	c.Writer = original
	_, err = c.Writer.Write([]byte("data: output\n\n"))
	require.NoError(t, err)
	require.Equal(t, len("data: output\n\n"), OpenAICompactKeepaliveAdjustedWrittenSize(c))

	// 异常计数不扩大重放资格；不会把未知的已写内容误判成心跳。
	RecordGatewayStreamHeartbeat(c, 1_000)
	require.Equal(t, c.Writer.Size(), OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.False(t, GatewayStreamHasOnlyHeartbeats(c))
}
