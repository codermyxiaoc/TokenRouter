package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// 常规、默认序列化及工具事件都必须保留首帧的 0 序号。
func TestResponsesSync_SequenceNumberRequired(t *testing.T) {
	for _, kind := range []string{"response.created", "response.completed", "response.failed", "response.output_text.delta", "response.function_call_arguments.done", "response.custom_tool_call_input.done"} {
		t.Run(kind, func(t *testing.T) {
			for _, seq := range []int{0, 17} {
				out := marshalEvent(t, ResponsesStreamEvent{Type: kind, SequenceNumber: seq})
				require.Contains(t, out, "sequence_number")
				require.EqualValues(t, seq, out["sequence_number"])
			}
		})
	}
}

// 多模态前导指令不能在合并时只剩文本；中途指令仍留在工具结果之后。
func TestResponsesSync_InstructionRolesPreserveMultimodalAndToolOrder(t *testing.T) {
	request := &ResponsesRequest{
		Instructions: " first \n",
		Input: json.RawMessage(`[
			{"role":"developer","content":[{"type":"input_text","text":"second"},{"type":"input_image","image_url":"https://example.invalid/prompt.png"}]},
			{"role":"user","content":"start"},
			{"type":"function_call","call_id":"c1","name":"tool","arguments":"{}"},
			{"type":"function_call_output","call_id":"c1","output":"ok"},
			{"role":"developer","content":[{"type":"input_image","image_url":"https://example.invalid/notice.png"}]},
			{"role":"user","content":"continue"}
		]`),
	}
	out, err := ResponsesToChatCompletionsRequest(request)
	require.NoError(t, err)
	require.Equal(t, []string{"system", "user", "assistant", "tool", "user", "user"}, chatMessageRoles(out.Messages))
	var parts []ChatContentPart
	require.NoError(t, json.Unmarshal(out.Messages[0].Content, &parts))
	require.Len(t, parts, 4)
	require.Equal(t, " first \n", parts[0].Text)
	require.Equal(t, "\n\n", parts[1].Text)
	require.Equal(t, "second", parts[2].Text)
	require.Equal(t, "https://example.invalid/prompt.png", parts[3].ImageURL.URL)
	require.Equal(t, "c1", out.Messages[2].ToolCalls[0].ID)
	require.Equal(t, "c1", out.Messages[3].ToolCallID)
	require.Contains(t, string(out.Messages[4].Content), "notice.png")
}

// 单条前导指令保留原始 JSON 字节，归一化不修改调用方的消息切片。
func TestResponsesSync_SingleInstructionNoReencoding(t *testing.T) {
	content := json.RawMessage(` [ {"type":"text", "text":"keep", "cache_control":{"type":"ephemeral"}} ] `)
	messages := []ChatMessage{{Role: "system", Content: content}, {Role: "user", Content: json.RawMessage(`"hi"`)}, {Role: "system", Content: json.RawMessage(`"later"`)}}
	out := normalizeResponsesDerivedChatMessageRoles(messages)
	require.Equal(t, content, out[0].Content)
	require.Equal(t, "user", out[2].Role)
	require.Equal(t, "system", messages[2].Role)
}

// 已转换的块扩展字段必须保留，避免合并引入额外的数据丢失。
func TestResponsesSync_MergeKeepsUnknownInstructionParts(t *testing.T) {
	out := normalizeResponsesDerivedChatMessageRoles([]ChatMessage{
		{Role: "system", Content: json.RawMessage(`"prefix"`)},
		{Role: "system", Content: json.RawMessage(`[{"type":"future_part","payload":{"large":9007199254740993}}]`)},
	})
	require.Len(t, out, 1)
	require.Contains(t, string(out[0].Content), `"future_part"`)
	require.Contains(t, string(out[0].Content), "9007199254740993")
}

// 即便任务消息没有正文，也结束上一轮推理，避免把旧思考串入后续调用。
func TestResponsesSync_AgentMessageStartsIndependentReasoningTurn(t *testing.T) {
	for _, content := range []string{`[]`, `"new task"`, `[{"type":"encrypted_content","encrypted_content":"client-provided-opaque-value"}]`} {
		input := json.RawMessage(`[
			{"type":"reasoning","summary":[{"type":"summary_text","text":"old thought"}]},
			{"type":"agent_message","content":` + content + `},
			{"type":"function_call","call_id":"new","name":"tool","arguments":"{}"},
			{"type":"function_call_output","call_id":"new","output":"ok"}
		]`)
		out, err := responsesInputToChatMessages("", input)
		require.NoError(t, err)
		for _, message := range out {
			require.Empty(t, message.ReasoningContent)
		}
		if content != `[]` {
			require.Equal(t, "user", out[0].Role)
		}
	}
}
