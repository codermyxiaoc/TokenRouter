//go:build unit

package antigravity

import (
	"github.com/stretchr/testify/require"
	"testing"
)

// 后续空计量心跳不能清空已经观测的累计用量；显式零值可以覆盖旧快照。
func TestAntigravityStability_EmptyMetadataKeepsCumulativeUsage(t *testing.T) {
	for _, tail := range []string{`{}`, `null`, `{"totalTokenCount":15}`, `{"promptTokenCount":null}`, `{"cachedContentTokenCount":null}`} {
		t.Run(tail, func(t *testing.T) {
			p := NewStreamingProcessor("gemini-3-flash-preview")
			p.ProcessLine(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"cachedContentTokenCount":3}}}`)
			p.ProcessLine(`data: {"response":{"usageMetadata":` + tail + `}}`)
			_, usage := p.Finish()
			require.Equal(t, 7, usage.InputTokens)
			require.Equal(t, 3, usage.CacheReadInputTokens)
			require.Equal(t, 5, usage.OutputTokens)
		})
	}
	p := NewStreamingProcessor("gemini-3-flash-preview")
	p.ProcessLine(`data: {"response":{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}}`)
	p.ProcessLine(`data: {"response":{"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0}}}`)
	_, usage := p.Finish()
	require.Zero(t, usage.InputTokens)
	require.Zero(t, usage.OutputTokens)
}

// 被拦截的包装响应可以没有 candidates，但已报告的缓存用量必须保留。
func TestAntigravityStability_WrappedUsageWithoutCandidates(t *testing.T) {
	_, usage, err := TransformGeminiToClaude([]byte(`{"response":{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"cachedContentTokenCount":30}}}`), "gemini-3-flash-preview")
	require.NoError(t, err)
	require.Zero(t, usage.InputTokens)
	require.Equal(t, 30, usage.CacheReadInputTokens)
}

// 直接 Gemini SSE 的 JSON 也能成功解码为包装结构零值，不能只靠 Unmarshal 错误触发回退。
func TestAntigravityStability_DirectStreamKeepsOutputAndUsage(t *testing.T) {
	p := NewStreamingProcessor("gemini-3-flash-preview")
	out := p.ProcessLine(`data: {"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`)
	require.Contains(t, string(out), "hello")
	_, usage := p.Finish()
	require.Equal(t, 10, usage.InputTokens)
	require.Equal(t, 5, usage.OutputTokens)
}
