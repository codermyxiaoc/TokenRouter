package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// 签名和隐去的思考必须原样回传，同时保留文本与思考交错顺序。
func TestClaude55SignedThinkingResponsesRoundTrip(t *testing.T) {
	for _, model := range []string{"claude-haiku-5-5", "claude-sonnet-5-5", "claude-opus-5-5"} {
		t.Run(model, func(t *testing.T) {
			blocks := []AnthropicContentBlock{{Type: "text", Text: "先回答"}, {Type: "thinking", Thinking: "分析", Signature: "opaque-signature"}, {Type: "redacted_thinking", Data: "opaque-data"}, {Type: "text", Text: "继续回答"}}
			resp := AnthropicToResponsesResponse(&AnthropicResponse{Model: model, Content: blocks})
			require.Len(t, resp.Output, 4)
			input, err := json.Marshal(resp.Output)
			require.NoError(t, err)
			req, err := ResponsesToAnthropicRequest(&ResponsesRequest{Model: model, Input: input})
			require.NoError(t, err)
			require.Len(t, req.Messages, 1)
			var actual []AnthropicContentBlock
			require.NoError(t, json.Unmarshal(req.Messages[0].Content, &actual))
			require.Equal(t, blocks, actual)
		})
	}
}

// 流式签名分片不能遗漏；未知提供方的不透明思考不能伪造为 Claude 签名。
func TestClaude55SignedThinkingStreamAndForeignHistory(t *testing.T) {
	s := NewAnthropicEventToResponsesState()
	for _, raw := range []string{
		`{"type":"message_start","message":{"model":"claude-haiku-5-5"}}`,
		`{"type":"content_block_start","content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"分析"}}`,
		`{"type":"content_block_delta","delta":{"type":"signature_delta","signature":"sig-"}}`,
		`{"type":"content_block_delta","delta":{"type":"signature_delta","signature":"end"}}`,
		`{"type":"content_block_stop"}`, `{"type":"message_stop"}`,
	} {
		var evt AnthropicStreamEvent
		require.NoError(t, json.Unmarshal([]byte(raw), &evt))
		AnthropicEventToResponsesEvents(&evt, s)
	}
	require.Len(t, s.Outputs, 1)
	block, err := decodeAnthropicThinking(s.Outputs[0].EncryptedContent)
	require.NoError(t, err)
	require.Equal(t, &AnthropicContentBlock{Type: "thinking", Thinking: "分析", Signature: "sig-end"}, block)
	foreign, err := decodeAnthropicThinking("opaque-openai-encrypted-content")
	require.NoError(t, err)
	require.Nil(t, foreign)
	_, err = decodeAnthropicThinking(anthropicThinkingEnvelopePrefix + "bad-base64")
	require.Error(t, err)
	_, err = decodeAnthropicThinking(encodeAnthropicThinking(AnthropicContentBlock{Type: "tool_use", ID: "fake"}))
	require.Error(t, err)
}
