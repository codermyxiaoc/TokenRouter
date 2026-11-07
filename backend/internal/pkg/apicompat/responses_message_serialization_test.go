//go:build unit

package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// marshalMessageWire 序列化任意值并解析为 gjson 结果。
func marshalMessageWire(t *testing.T, value any) gjson.Result {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return gjson.ParseBytes(raw)
}

// TestMessageContentWireConsistent 验证 message content 在流式事件与 completed 响应中的线上形态一致。
func TestMessageContentWireConsistent(t *testing.T) {
	for _, text := range []string{"", "search result"} {
		t.Run(text, func(t *testing.T) {
			part := ResponsesContentPart{Type: "output_text", Text: text}
			item := ResponsesOutput{Type: "message", ID: "msg_1", Role: "assistant", Status: "completed", Content: []ResponsesContentPart{part}}
			response := ResponsesResponse{ID: "resp_1", Object: "response", Status: "completed", Output: []ResponsesOutput{item}}
			parts := []gjson.Result{
				marshalMessageWire(t, ResponsesStreamEvent{Type: "response.content_part.done", Part: &part}).Get("part"),
				marshalMessageWire(t, ResponsesStreamEvent{Type: "response.output_item.done", Item: &item}).Get("item.content.0"),
				marshalMessageWire(t, ResponsesStreamEvent{Type: "response.completed", Response: &response}).Get("response.output.0.content.0"),
				marshalMessageWire(t, response).Get("output.0.content.0"),
			}
			for _, encoded := range parts {
				require.Equal(t, "output_text", encoded.Get("type").String())
				require.True(t, encoded.Get("text").Exists())
				require.Equal(t, text, encoded.Get("text").String())
				require.Equal(t, "[]", encoded.Get("annotations").Raw)
				require.Equal(t, "[]", encoded.Get("logprobs").Raw)
			}
		})
	}
}

// TestMessageRefusalPreserved 验证 refusal 内容保留在 refusal 字段。
func TestMessageRefusalPreserved(t *testing.T) {
	item := ResponsesOutput{Type: "message", ID: "msg_1", Role: "assistant", Content: []ResponsesContentPart{{Type: "refusal", Refusal: "Cannot provide this."}}}
	for _, encoded := range []gjson.Result{
		marshalMessageWire(t, item),
		marshalMessageWire(t, ResponsesStreamEvent{Type: "response.output_item.done", Item: &item}).Get("item"),
	} {
		require.Equal(t, "refusal", encoded.Get("content.0.type").String())
		require.Equal(t, "Cannot provide this.", encoded.Get("content.0.refusal").String())
		require.False(t, encoded.Get("content.0.text").Exists())
	}
}

// TestEmptyMessageContentArray 验证空 message content 序列化为空数组。
func TestEmptyMessageContentArray(t *testing.T) {
	item := marshalMessageWire(t, ResponsesOutput{Type: "message", ID: "msg_1", Role: "assistant"})
	require.Equal(t, "[]", item.Get("content").Raw)
}
