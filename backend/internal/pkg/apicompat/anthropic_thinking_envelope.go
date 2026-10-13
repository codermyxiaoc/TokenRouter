package apicompat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

const anthropicThinkingEnvelopePrefix = "anthropic-thinking-v1:"

// encodeAnthropicThinking 只封装 Claude 签名思考白名单，供 Responses 历史透明回传。
func encodeAnthropicThinking(block AnthropicContentBlock) string {
	raw, _ := json.Marshal(struct {
		Type      string `json:"type"`
		Thinking  string `json:"thinking,omitempty"`
		Signature string `json:"signature,omitempty"`
		Data      string `json:"data,omitempty"`
	}{block.Type, block.Thinking, block.Signature, block.Data})
	return anthropicThinkingEnvelopePrefix + base64.RawStdEncoding.EncodeToString(raw)
}

// decodeAnthropicThinking 不把 OpenAI 加密推理解释成 Claude 签名，只识别本站明确封装且限制字段。
func decodeAnthropicThinking(value string) (*AnthropicContentBlock, error) {
	if !strings.HasPrefix(value, anthropicThinkingEnvelopePrefix) {
		return nil, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, anthropicThinkingEnvelopePrefix))
	if err != nil {
		return nil, fmt.Errorf("invalid Anthropic thinking envelope")
	}
	var payload struct {
		Type      string `json:"type"`
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
		Data      string `json:"data"`
	}
	if err = json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("invalid Anthropic thinking envelope")
	}
	if (payload.Type != "thinking" || payload.Signature == "") && (payload.Type != "redacted_thinking" || payload.Data == "") {
		return nil, fmt.Errorf("invalid Anthropic signed thinking block")
	}
	return &AnthropicContentBlock{Type: payload.Type, Thinking: payload.Thinking, Signature: payload.Signature, Data: payload.Data}, nil
}
