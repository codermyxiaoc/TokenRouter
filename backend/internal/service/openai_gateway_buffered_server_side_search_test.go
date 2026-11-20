//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 非流式终态补全后，应将服务端调用保留为原本的搜索类型。
func TestGrokBufferedRebuiltOutputKeepsServerSideSearchOutOfToolCalls(t *testing.T) {
	account := healthyGrokOAuthGatewayTestAccount(4242, "access-token")
	for _, path := range []string{"/v1/messages", "/v1/chat/completions"} {
		t.Run(path+" search only", func(t *testing.T) {
			sse := grokCustomToolCallStream([]string{grokXSearchItemJSON}, `[]`)
			got := forwardGrokCustomToolCallStream(t, path, false, account, sse)

			require.Equal(t, 1, got.searchCount)
			require.NotContains(t, got.body, "tool_use", got.body)
			require.NotContains(t, got.body, "tool_calls", got.body)
			require.NotContains(t, got.body, "x_keyword_search", got.body)
			require.Contains(t, got.body, "found it", got.body)
			if path == "/v1/messages" {
				require.Contains(t, got.body, `"stop_reason":"end_turn"`, got.body)
			} else {
				require.Contains(t, got.body, `"finish_reason":"stop"`, got.body)
			}
		})
		t.Run(path+" search next to a client tool", func(t *testing.T) {
			sse := grokCustomToolCallStream([]string{grokXSearchItemJSON, grokClientToolJSON}, `[]`)
			got := forwardGrokCustomToolCallStream(t, path, false, account, sse)

			require.Equal(t, 1, got.searchCount)
			require.NotContains(t, got.body, "x_keyword_search", got.body)
			require.Contains(t, got.body, "exec", got.body)
			if path == "/v1/messages" {
				require.Contains(t, got.body, `"stop_reason":"tool_use"`, got.body)
			} else {
				require.Contains(t, got.body, `"finish_reason":"tool_calls"`, got.body)
			}
		})
	}
}

// 非 xAI 线路遇到相同调用 ID 时仍按普通自定义工具处理。
func TestOpenAIBufferedRebuiltOutputKeepsXSCallIDAsToolCall(t *testing.T) {
	account := bufferedHostedItemsOAuthAccount()
	for _, path := range []string{"/v1/messages", "/v1/chat/completions"} {
		t.Run(path, func(t *testing.T) {
			sse := grokCustomToolCallStream([]string{grokXSearchItemJSON}, `[]`)
			got := forwardGrokCustomToolCallStream(t, path, false, account, sse)

			require.Equal(t, 0, got.searchCount)
			require.Contains(t, got.body, "x_keyword_search", got.body)
			if path == "/v1/messages" {
				require.Contains(t, got.body, "tool_use", got.body)
			} else {
				require.Contains(t, got.body, "tool_calls", got.body)
			}
		})
	}
}
