package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// 两种 OpenAI 入站的显式档位均使用 Opus 5.5 唯一接受的 adaptive 模式。
func TestOpus55ReasoningBridge(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		t.Run(effort, func(t *testing.T) {
			var req ResponsesRequest
			require.NoError(t, json.Unmarshal([]byte(`{"model":"claude-opus-5-5","input":"hi","reasoning":{"effort":"`+effort+`"}}`), &req))
			got, err := ResponsesToAnthropicRequest(&req)
			require.NoError(t, err)
			require.Equal(t, effort, got.OutputConfig.Effort)
			require.Equal(t, &AnthropicThinking{Type: "adaptive"}, got.Thinking)
			var chat ChatCompletionsRequest
			require.NoError(t, json.Unmarshal([]byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"`+effort+`"}`), &chat))
			bridge, err := ChatCompletionsToResponses(&chat)
			require.NoError(t, err)
			got, err = ResponsesToAnthropicRequest(bridge)
			require.NoError(t, err)
			require.Equal(t, effort, got.OutputConfig.Effort)
			require.Equal(t, &AnthropicThinking{Type: "adaptive"}, got.Thinking)
		})
	}
}

func TestOpus55ReasoningBridgeUsesMappedModel(t *testing.T) {
	for _, tc := range []struct{ requested, mapped, wantType string }{
		{"public-alias", "claude-opus-5-5", "adaptive"},
		{"claude-opus-5-5", "claude-sonnet-4-5", "enabled"},
		{"claude-opus-5", "claude-opus-5", "enabled"},
	} {
		t.Run(tc.requested+"/"+tc.mapped, func(t *testing.T) {
			var req ResponsesRequest
			require.NoError(t, json.Unmarshal([]byte(`{"model":"`+tc.requested+`","input":"hi","reasoning":{"effort":"high"}}`), &req))
			got, err := ResponsesToAnthropicRequest(&req, tc.mapped)
			require.NoError(t, err)
			require.Equal(t, tc.wantType, got.Thinking.Type)
			require.Equal(t, tc.requested, got.Model)
			require.Equal(t, tc.wantType == "enabled", got.Thinking.BudgetTokens > 0)
		})
	}
}

func TestOpus55BridgePreservesDefaultsAndToolChoice(t *testing.T) {
	// 缺省时由上游采用 medium；不替用户降级强制工具约束，仍由上游执行校验。
	var req ResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"claude-opus-5-5","input":"hi","tool_choice":"required"}`), &req))
	got, err := ResponsesToAnthropicRequest(&req)
	require.NoError(t, err)
	require.Nil(t, got.Thinking)
	require.Nil(t, got.OutputConfig)
	require.JSONEq(t, `{"type":"any"}`, string(got.ToolChoice))
}
