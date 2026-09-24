package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// Messages 兼容桥必须结合新型号的实际推理档位处理采样参数，且保留工具能力。
func TestAnthropicToResponsesGPT6SolLunaSamplingAndTools(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna", "OPENAI/GPT-6_SOL", "OPENAI/GPT-6_LUNA"} {
		for _, effort := range []string{"", "none", "low", "medium", "high", "xhigh", "max"} {
			t.Run(model+"/"+effort, func(t *testing.T) {
				temperature, topP := 0.3, 0.7
				req := &AnthropicRequest{
					Model: model, MaxTokens: 2048, Temperature: &temperature, TopP: &topP,
					Messages: []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
					Tools:    []AnthropicTool{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)}},
				}
				if effort != "" {
					req.OutputConfig = &AnthropicOutputConfig{Effort: effort}
				}
				out, err := AnthropicToResponses(req)
				require.NoError(t, err)
				wantEffort := effort
				if wantEffort == "" {
					wantEffort = "medium"
				}
				require.Equal(t, wantEffort, out.Reasoning.Effort)
				require.Len(t, out.Tools, 1)
				require.Equal(t, "lookup", out.Tools[0].Name)
				if effort == "none" {
					require.Equal(t, &temperature, out.Temperature)
					require.Equal(t, &topP, out.TopP)
				} else {
					require.Nil(t, out.Temperature)
					require.Nil(t, out.TopP)
				}
			})
		}
	}
}
