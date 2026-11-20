package apicompat

import (
	"encoding/json"
	"strings"
)

// ToolSearchToolName 是 Claude 客户端工具搜索名，原生 Responses 搜索结果需映射回该名称。
const ToolSearchToolName = "ToolSearch"

// 无文本的工具结果沿用既有转换器的空输出。
const emptyToolResultOutput = "(empty)"

// 将 Claude 延迟工具放到历史搜索结果，启用原生搜索且维持顶层工具前缀稳定。
func ApplyNativeToolSearch(req *AnthropicRequest, out *ResponsesRequest) error {
	declared := false
	deferred := make(map[string]bool)
	for _, t := range req.Tools {
		if t.DeferLoading {
			deferred[t.Name] = true
		} else if t.Name == ToolSearchToolName {
			declared = true
		}
	}
	// Responses 无法强制选择搜索工具或尚未公开的工具，保留普通函数表示。
	if !declared || forcesHiddenTool(req.ToolChoice, deferred) {
		return nil
	}

	schemas := make(map[string]AnthropicTool, len(req.Tools))
	kept := make([]AnthropicTool, 0, len(req.Tools))
	for _, t := range req.Tools {
		schemas[t.Name] = t
		if t.DeferLoading {
			continue
		}
		kept = append(kept, t)
	}
	tools := convertAnthropicToolsToResponses(kept)
	for i, t := range kept {
		if t.Name == ToolSearchToolName {
			// Codex 的搜索声明省略名称和 strict。
			tools[i] = ResponsesTool{
				Type:        "tool_search",
				Execution:   "client",
				Description: t.Description,
				Parameters:  normalizeToolParameters(t.InputSchema),
			}
		}
	}
	out.Tools = tools

	revealed := toolSearchReveals(req.Messages)
	if len(revealed) == 0 {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(out.Input, &items); err != nil {
		return err
	}
	rewritten := make([]json.RawMessage, 0, len(items))
	var pendingText []string
	flush := func(next json.RawMessage) (json.RawMessage, error) {
		text := strings.Join(pendingText, "\n\n")
		pendingText = nil
		return userMessageWithLeadingText(next, text)
	}
	for _, raw := range items {
		var item ResponsesInputItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		if len(pendingText) > 0 && item.Type != "function_call_output" {
			// 搜索结果文本附加到紧随工具结果的用户消息中。
			merged, err := flush(raw)
			if err != nil {
				return err
			}
			if item.Type == "message" && item.Role == "user" {
				raw = merged
			} else {
				rewritten = append(rewritten, merged)
			}
		}
		names, isSearch := revealed[item.CallID]
		if !isSearch {
			rewritten = append(rewritten, raw)
			continue
		}
		var native any
		switch item.Type {
		case "function_call":
			native = map[string]any{
				"type":      "tool_search_call",
				"call_id":   item.CallID,
				"execution": "client",
				"status":    "completed",
				"arguments": jsonObjectOrEmpty(item.Arguments),
			}
		case "function_call_output":
			loaded := make([]any, 0, len(names))
			for _, name := range names {
				t, ok := schemas[name]
				if !ok {
					continue
				}
				loaded = append(loaded, map[string]any{
					"type":          "function",
					"name":          name,
					"description":   t.Description,
					"defer_loading": true,
					"strict":        false,
					"parameters":    normalizeToolParameters(t.InputSchema),
				})
			}
			native = map[string]any{
				"type":      "tool_search_output",
				"call_id":   item.CallID,
				"execution": "client",
				"status":    "completed",
				"tools":     loaded,
			}
			if item.Output != "" && item.Output != emptyToolResultOutput {
				pendingText = append(pendingText, item.Output)
			}
		default:
			rewritten = append(rewritten, raw)
			continue
		}
		encoded, err := json.Marshal(native)
		if err != nil {
			return err
		}
		rewritten = append(rewritten, encoded)
	}
	if len(pendingText) > 0 {
		tail, err := flush(nil)
		if err != nil {
			return err
		}
		rewritten = append(rewritten, tail)
	}
	input, err := json.Marshal(rewritten)
	if err != nil {
		return err
	}
	out.Input = input
	return nil
}

// 将搜索说明作为独立的文本部分插入后续用户消息，保留原有内容。
func userMessageWithLeadingText(next json.RawMessage, text string) (json.RawMessage, error) {
	lead, err := json.Marshal(ResponsesContentPart{Type: "input_text", Text: text})
	if err != nil {
		return nil, err
	}
	var msg ResponsesInputItem
	if len(next) > 0 && json.Unmarshal(next, &msg) == nil && msg.Type == "message" && msg.Role == "user" {
		var parts []json.RawMessage
		if json.Unmarshal(msg.Content, &parts) == nil {
			content, err := json.Marshal(append([]json.RawMessage{lead}, parts...))
			if err != nil {
				return nil, err
			}
			msg.Content = content
			return json.Marshal(msg)
		}
	}
	content, err := json.Marshal([]json.RawMessage{lead})
	if err != nil {
		return nil, err
	}
	return json.Marshal(ResponsesInputItem{Type: "message", Role: "user", Content: content})
}

func forcesHiddenTool(raw json.RawMessage, deferred map[string]bool) bool {
	var choice struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &choice) != nil {
		return false
	}
	return choice.Type == "tool" && (choice.Name == ToolSearchToolName || deferred[choice.Name])
}

// 按工具调用 ID 汇总后续已公开的工具，并保持其首次出现顺序。
func toolSearchReveals(msgs []AnthropicMessage) map[string][]string {
	reveals := make(map[string][]string)
	for _, m := range msgs {
		var blocks []AnthropicContentBlock
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			switch {
			case b.Type == "tool_use" && b.Name == ToolSearchToolName && b.ID != "":
				reveals[toResponsesCallID(b.ID)] = nil
			case b.Type == "tool_result":
				id := toResponsesCallID(b.ToolUseID)
				if _, isSearch := reveals[id]; !isSearch {
					continue
				}
				var inner []AnthropicContentBlock
				_ = json.Unmarshal(b.Content, &inner)
				seen := make(map[string]bool)
				for _, ib := range inner {
					if ib.Type == "tool_reference" && ib.ToolName != "" && !seen[ib.ToolName] {
						seen[ib.ToolName] = true
						reveals[id] = append(reveals[id], ib.ToolName)
					}
				}
			}
		}
	}
	return reveals
}

// 保留 call_id，供下一轮 Claude 工具结果匹配。
func toolSearchCallToolUse(item ResponsesOutput) AnthropicContentBlock {
	return AnthropicContentBlock{
		Type:  "tool_use",
		ID:    fromResponsesCallID(item.CallID),
		Name:  ToolSearchToolName,
		Input: jsonObjectOrEmpty(item.Arguments),
	}
}

// 参数必须是 JSON 对象，无有效对象时返回空对象。
func jsonObjectOrEmpty(raw string) json.RawMessage {
	var probe map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &probe) != nil || probe == nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func resToAnthHandleToolSearchDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	events := closeCurrentBlock(state)
	block := toolSearchCallToolUse(*evt.Item)
	input := block.Input
	block.Input = json.RawMessage(`{}`)
	idx := state.ContentBlockIndex
	state.HasToolCall = true
	events = append(events,
		AnthropicStreamEvent{Type: "content_block_start", Index: &idx, ContentBlock: &block},
		AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: string(input)}},
		AnthropicStreamEvent{Type: "content_block_stop", Index: &idx},
	)
	state.ContentBlockIndex++
	return events
}
