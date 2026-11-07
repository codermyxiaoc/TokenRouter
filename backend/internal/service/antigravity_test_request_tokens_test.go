//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 连接测试给思考模型留出回答空间：Gemini 与 Claude 两种测试请求的输出上限都不再是 1。
func TestAntigravityTestRequestsLeaveRoomToAnswer(t *testing.T) {
	svc := &AntigravityGatewayService{}

	claudeBody, err := svc.buildClaudeTestRequest("project-1", "claude-sonnet-4-5", "hi")
	require.NoError(t, err)
	require.Equal(t, int64(antigravityTestMaxOutputTokens), gjson.GetBytes(claudeBody, "request.generationConfig.maxOutputTokens").Int())

	geminiBody, err := svc.buildGeminiTestRequestWithPrompt("project-1", "gemini-3.8-flash", "hi")
	require.NoError(t, err)
	require.Equal(t, int64(antigravityTestMaxOutputTokens), gjson.GetBytes(geminiBody, "request.generationConfig.maxOutputTokens").Int())
	require.Equal(t, "hi", gjson.GetBytes(geminiBody, "request.contents.0.parts.0.text").String())
}
