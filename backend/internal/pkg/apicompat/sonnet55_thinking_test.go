package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// 兼容桥按实际映射型号选择自适应思考，同时保留公开别名。
func TestSonnet55ResponsesThinking(t *testing.T) {
	for _, tc := range []struct{ effort, thinking, output string }{
		{"", "adaptive", "high"}, {"none", "between_tools", "low"}, {"low", "adaptive", "low"}, {"max", "adaptive", "max"},
	} {
		req := &ResponsesRequest{Model: "public-alias", Input: json.RawMessage(`"hello"`)}
		if tc.effort != "" {
			req.Reasoning = &ResponsesReasoning{Effort: tc.effort}
		}
		got, err := ResponsesToAnthropicRequest(req, "claude-sonnet-5-5")
		require.NoError(t, err)
		require.Equal(t, "public-alias", got.Model)
		require.Equal(t, tc.thinking, got.Thinking.Type)
		require.Equal(t, tc.output, got.OutputConfig.Effort)
		require.Zero(t, got.Thinking.BudgetTokens)
	}
	for _, effort := range []string{"minimal", "ultra", "unsupported"} {
		_, err := ResponsesToAnthropicRequest(&ResponsesRequest{Model: "claude-sonnet-5-5", Input: json.RawMessage(`"hello"`), Reasoning: &ResponsesReasoning{Effort: effort}})
		require.Error(t, err)
	}
	_, err := ResponsesToAnthropicRequest(&ResponsesRequest{Model: "public-alias", Input: json.RawMessage(`"hello"`), ToolChoice: json.RawMessage(`"required"`)}, "claude-sonnet-5-5")
	require.Error(t, err)
}
