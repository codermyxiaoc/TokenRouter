//go:build unit

package handler

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestBuildCyberSessionBlockCombinesExplicitAndTranscriptKeys 验证屏蔽写入计划同时包含显式会话 key、
// transcript 前缀 key 及其历史长度，并在存在 transcript key 时激活来源 scope。
func TestBuildCyberSessionBlockCombinesExplicitAndTranscriptKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"messages":[{"role":"user","content":"setup"},{"role":"assistant","content":"ready"},{"role":"user","content":"trigger"}]}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/openai/v1/responses", strings.NewReader(string(body)))
	c.Request.RemoteAddr = "203.0.113.44:12345"
	c.Request.Header.Set("User-Agent", "client/1.2.3")

	block := buildCyberSessionBlock(7, c, body)
	require.Len(t, block.Keys, 2)
	require.Equal(t, []int{3, 2}, block.TranscriptLengths)
	require.Equal(t, int64(7), block.APIKeyID)
	require.NotEmpty(t, block.ScopeKey)

	c.Request.Header.Set("session_id", "sess-explicit")
	block = buildCyberSessionBlock(7, c, body)
	require.Len(t, block.Keys, 3)
	require.Equal(t, []int{3, 2}, block.TranscriptLengths)
	require.NotEmpty(t, block.ScopeKey)
}
