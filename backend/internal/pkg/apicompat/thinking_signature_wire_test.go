package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// 思考块必须保留 signature 字段，满足严格客户端的报文解析要求。
func TestAnthropicContentBlock_ThinkingAlwaysHasSignature(t *testing.T) {
	data, err := json.Marshal(AnthropicContentBlock{Type: "thinking", Thinking: "plan"})
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"thinking","thinking":"plan","signature":""}`, string(data))

	data, err = json.Marshal(AnthropicContentBlock{Type: "thinking", Signature: "sig"})
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"thinking","thinking":"","signature":"sig"}`, string(data))

	data, err = json.Marshal(AnthropicContentBlock{Type: "text", Text: "hi"})
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"text","text":"hi"}`, string(data))
}
