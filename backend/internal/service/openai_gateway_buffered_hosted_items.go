package service

import (
	"encoding/json"
	"math"
	"sort"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
)

// 按内容下标存储各类增量，避免跨片段拼接。
type openAICompatPartText struct {
	parts map[int64]*strings.Builder
}

func (p *openAICompatPartText) write(index int64, delta string) {
	if p.parts == nil {
		p.parts = map[int64]*strings.Builder{}
	}
	if p.parts[index] == nil {
		p.parts[index] = &strings.Builder{}
	}
	_, _ = p.parts[index].WriteString(delta)
}

func (p *openAICompatPartText) any() bool {
	return len(p.parts) > 0
}

func (p *openAICompatPartText) at(index int64) string {
	if builder := p.parts[index]; builder != nil {
		return builder.String()
	}
	return ""
}

// 按原顺序补上尚未被完整片段取用的增量。
func (p *openAICompatPartText) unclaimed(claimed map[int64]bool, from int64) string {
	indexes := make([]int64, 0, len(p.parts))
	for index := range p.parts {
		if index >= from && !claimed[index] {
			indexes = append(indexes, index)
		}
	}
	sort.Slice(indexes, func(i, j int) bool { return indexes[i] < indexes[j] })
	var text strings.Builder
	for _, index := range indexes {
		_, _ = text.WriteString(p.parts[index].String())
	}
	return text.String()
}

// 收集一个输出条目的开始、增量和完成事件。
type openAICompatOutputSlot struct {
	index         int64
	indexed       bool
	itemType      string
	id            string
	added         *apicompat.ResponsesOutput
	done          *apicompat.ResponsesOutput
	reasoning     strings.Builder
	summaryText   openAICompatPartText
	reasoningText openAICompatPartText
	text          openAICompatPartText
	refusal       openAICompatPartText
	args          strings.Builder
	argsDone      string
}

// 为非流式响应按输出条目保存上游事件。
type openAICompatOutputSlots struct {
	slots []*openAICompatOutputSlot
	// xAI 的服务端搜索必须和客户端工具调用区分。
	xaiServerSideSearch bool
}

func openAICompatIsToolCallType(itemType string) bool {
	return itemType == "function_call" || itemType == "custom_tool_call"
}

func (o *openAICompatOutputSlots) Observe(payload []byte) {
	if o == nil {
		return
	}
	switch strings.TrimSpace(gjson.GetBytes(payload, "type").String()) {
	case "response.output_item.added":
		if item, ok := openAICompatEventItem(payload); ok {
			slot := o.slot(payload, item.Type, item.ID, item.CallID, false, true)
			slot.added = &item
		}
	case "response.output_item.done":
		if item, ok := openAICompatEventItem(payload); ok {
			slot := o.slot(payload, item.Type, item.ID, item.CallID, true, true)
			slot.done = &item
		}
	case "response.output_text.delta":
		if delta := gjson.GetBytes(payload, "delta").String(); delta != "" {
			o.slot(payload, "message", gjson.GetBytes(payload, "item_id").String(), "", false, true).text.write(gjson.GetBytes(payload, "content_index").Int(), delta)
		}
	case "response.refusal.delta":
		if delta := gjson.GetBytes(payload, "delta").String(); delta != "" {
			o.slot(payload, "message", gjson.GetBytes(payload, "item_id").String(), "", false, true).refusal.write(gjson.GetBytes(payload, "content_index").Int(), delta)
		}
	case "response.reasoning_summary_text.delta":
		if delta := gjson.GetBytes(payload, "delta").String(); delta != "" {
			slot := o.slot(payload, "reasoning", gjson.GetBytes(payload, "item_id").String(), "", false, true)
			_, _ = slot.reasoning.WriteString(delta)
			slot.summaryText.write(gjson.GetBytes(payload, "summary_index").Int(), delta)
		}
	case "response.reasoning_text.delta":
		if delta := gjson.GetBytes(payload, "delta").String(); delta != "" {
			slot := o.slot(payload, "reasoning", gjson.GetBytes(payload, "item_id").String(), "", false, true)
			_, _ = slot.reasoning.WriteString(delta)
			slot.reasoningText.write(gjson.GetBytes(payload, "content_index").Int(), delta)
		}
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		if delta := gjson.GetBytes(payload, "delta").String(); delta != "" {
			if slot := o.slot(payload, "function_call", gjson.GetBytes(payload, "item_id").String(), "", false, false); slot != nil {
				_, _ = slot.args.WriteString(delta)
			}
		}
	case "response.function_call_arguments.done":
		if slot := o.slot(payload, "function_call", gjson.GetBytes(payload, "item_id").String(), "", false, false); slot != nil {
			if completed := gjson.GetBytes(payload, "arguments").String(); completed != "" {
				slot.argsDone = completed
			}
		}
	case "response.custom_tool_call_input.done":
		if slot := o.slot(payload, "function_call", gjson.GetBytes(payload, "item_id").String(), "", false, false); slot != nil {
			if completed := gjson.GetBytes(payload, "input").String(); completed != "" {
				slot.argsDone = completed
			}
		}
	}
}

func openAICompatEventItem(payload []byte) (apicompat.ResponsesOutput, bool) {
	var item apicompat.ResponsesOutput
	raw := gjson.GetBytes(payload, "item")
	if !raw.IsObject() || !gjson.ValidBytes(payload) || json.Unmarshal([]byte(raw.Raw), &item) != nil {
		return item, false
	}
	item.Type = strings.TrimSpace(item.Type)
	return item, item.Type != ""
}

// 优先按条目 ID 或调用 ID 合并事件，同一索引可以包含多个条目。缺少标识时按类型和抵达顺序匹配，完成事件只替换对应条目。
func (o *openAICompatOutputSlots) slot(payload []byte, kind, id, callID string, done, create bool) *openAICompatOutputSlot {
	index := gjson.GetBytes(payload, "output_index")
	added := strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == "response.output_item.added"
	matches := func(slot *openAICompatOutputSlot) bool {
		if slot.itemType == "" || slot.itemType == kind {
			return true
		}
		return openAICompatIsToolCallType(kind) && openAICompatIsToolCallType(slot.itemType)
	}
	fits := func(slot *openAICompatOutputSlot) bool {
		return matches(slot) && (slot.id == "" || id == "" || slot.id == id)
	}
	newest := func(accept func(*openAICompatOutputSlot) bool) *openAICompatOutputSlot {
		for i := len(o.slots) - 1; i >= 0; i-- {
			if accept(o.slots[i]) {
				return o.slots[i]
			}
		}
		return nil
	}
	var found *openAICompatOutputSlot
	if done && id != "" {
		found = newest(func(slot *openAICompatOutputSlot) bool { return slot.done != nil && slot.done.ID == id })
	}
	if found == nil && id != "" && (done || added || !index.Exists()) {
		found = newest(func(slot *openAICompatOutputSlot) bool {
			return slot.id == id && matches(slot) && slot.done == nil && (!added || slot.added == nil)
		})
	}
	if found == nil {
		switch {
		case index.Exists():
			atIndex := func(slot *openAICompatOutputSlot) bool {
				return slot.indexed && slot.index == index.Int() && matches(slot)
			}
			switch {
			case done && id != "":
				found = newest(func(slot *openAICompatOutputSlot) bool { return atIndex(slot) && slot.done == nil })
			case added:
				found = newest(func(slot *openAICompatOutputSlot) bool { return atIndex(slot) && slot.added == nil && slot.done == nil })
			default:
				found = newest(atIndex)
			}
		case added:
		case done:
			if callID != "" {
				found = newest(func(slot *openAICompatOutputSlot) bool {
					return slot.done == nil && slot.added != nil && slot.added.CallID == callID && matches(slot)
				})
			}
			for _, slot := range o.slots {
				if found == nil && !slot.indexed && slot.done == nil && slot.itemType == kind {
					found = slot
				}
			}
		default:
			found = newest(func(slot *openAICompatOutputSlot) bool { return slot.done == nil && fits(slot) })
			if found == nil {
				found = newest(func(slot *openAICompatOutputSlot) bool { return !slot.indexed && fits(slot) })
			}
		}
	}
	if found == nil && index.Exists() && (added || done) {
		// 没有索引且先于条目声明的增量归入最近的同类待匹配条目。
		found = newest(func(slot *openAICompatOutputSlot) bool {
			return !slot.indexed && slot.added == nil && slot.done == nil && fits(slot)
		})
	}
	if found == nil {
		if !create {
			return nil
		}
		found = &openAICompatOutputSlot{}
		o.slots = append(o.slots, found)
	}
	if index.Exists() && !found.indexed {
		found.index, found.indexed = index.Int(), true
	}
	if found.id == "" {
		found.id = id
	}
	if found.itemType == "" || done || added {
		found.itemType = kind
	}
	return found
}

// 仅为成功或未完成的终态重建空输出，保留相同索引的抵达顺序。完成条目优先，空缺部分按原索引补入增量。
func (o *openAICompatOutputSlots) fillTerminalOutput(eventType string, response *apicompat.ResponsesResponse) {
	switch strings.TrimSpace(eventType) {
	case "response.completed", "response.done", "response.incomplete", "response.cancelled", "response.canceled":
	default:
		return
	}
	if o == nil || response == nil || len(response.Output) > 0 {
		return
	}
	slots := append([]*openAICompatOutputSlot(nil), o.slots...)
	sort.SliceStable(slots, func(i, j int) bool {
		a, b := slots[i], slots[j]
		if a.indexed != b.indexed {
			return a.indexed
		}
		return a.indexed && a.index < b.index
	})
	for _, slot := range slots {
		if item, ok := slot.build(); ok {
			// 仅将已经完成的搜索交给流式协议转换。
			if item.Type == "web_search_call" && item.Status != "completed" {
				continue
			}
			response.Output = append(response.Output, item)
			if item.Type == "custom_tool_call" {
				response.RenderCustomToolCalls = true
				response.XAIServerSideSearch = o.xaiServerSideSearch
			}
		}
	}
}

func (s *openAICompatOutputSlot) build() (apicompat.ResponsesOutput, bool) {
	switch s.itemType {
	case "reasoning":
		return s.buildReasoning()
	case "message":
		return s.buildMessage()
	case "function_call", "custom_tool_call":
		return s.buildToolCall()
	}
	if s.done == nil {
		return apicompat.ResponsesOutput{}, false
	}
	return *s.done, true
}

func (s *openAICompatOutputSlot) buildReasoning() (apicompat.ResponsesOutput, bool) {
	item := apicompat.ResponsesOutput{Type: "reasoning"}
	if s.done != nil {
		item = *s.done
	}
	// 推理签名可能只出现在开始事件。
	if s.added != nil && strings.TrimSpace(item.EncryptedContent) == "" {
		item.EncryptedContent = s.added.EncryptedContent
	}
	// 有摘要时优先呈现摘要，否则使用推理正文。
	if s.reasoning.Len() > 0 {
		var summary, content bool
		for _, part := range item.Summary {
			summary = summary || (part.Type == "summary_text" && part.Text != "")
		}
		for _, part := range item.Content {
			content = content || (part.Type == "reasoning_text" && part.Text != "")
		}
		switch {
		case summary == content:
			item.Summary = []apicompat.ResponsesSummary{{Type: "summary_text", Text: s.reasoning.String()}}
			item.Content = nil
		case summary:
			// 保留完成事件中的文本，仅用对应增量补充空摘要。
			item.Summary = append([]apicompat.ResponsesSummary(nil), item.Summary...)
			claimed := map[int64]bool{}
			for i := range item.Summary {
				if part := &item.Summary[i]; part.Type == "summary_text" {
					claimed[int64(i)] = true
					if part.Text == "" {
						part.Text = s.summaryText.at(int64(i))
					}
				}
			}
			if rest := s.summaryText.unclaimed(claimed, int64(len(item.Summary))); rest != "" {
				item.Summary = append(item.Summary, apicompat.ResponsesSummary{Type: "summary_text", Text: rest})
			}
		default:
			item.Content = append([]apicompat.ResponsesContentPart(nil), item.Content...)
			claimed := map[int64]bool{}
			for i := range item.Content {
				if part := &item.Content[i]; part.Type == "reasoning_text" {
					claimed[int64(i)] = true
					if part.Text == "" {
						part.Text = s.reasoningText.at(int64(i))
					}
				}
			}
			if rest := s.reasoningText.unclaimed(claimed, int64(len(item.Content))); rest != "" {
				item.Content = append(item.Content, apicompat.ResponsesContentPart{Type: "reasoning_text", Text: rest})
			}
		}
	}
	return item, s.done != nil || s.reasoning.Len() > 0 || strings.TrimSpace(item.EncryptedContent) != ""
}

func (s *openAICompatOutputSlot) buildMessage() (apicompat.ResponsesOutput, bool) {
	item := apicompat.ResponsesOutput{Type: "message", Role: "assistant"}
	if s.done != nil {
		item = *s.done
		item.Content = append([]apicompat.ResponsesContentPart(nil), item.Content...)
	}
	declared := int64(len(item.Content))
	if s.text.any() {
		blank := func(text string) bool { return strings.TrimSpace(text) == "" }
		if rest := completeContentParts(item.Content, &s.text, "output_text", declared, blank); rest != "" {
			item.Content = append(item.Content, apicompat.ResponsesContentPart{Type: "output_text", Text: rest})
		}
	}
	if s.refusal.any() {
		blank := func(text string) bool { return text == "" }
		if rest := completeContentParts(item.Content, &s.refusal, "refusal", declared, blank); rest != "" {
			item.Content = append(item.Content, apicompat.ResponsesContentPart{Type: "refusal", Refusal: rest})
		}
	}
	return item, s.done != nil || s.text.any() || s.refusal.any()
}

// 按内容索引补齐空片段，保留已带文本的片段。
func completeContentParts(content []apicompat.ResponsesContentPart, deltas *openAICompatPartText, partType string, declared int64, blank func(string) bool) string {
	claimed := map[int64]bool{}
	present := false
	for i := range content {
		part := &content[i]
		if part.Type != partType {
			continue
		}
		field := &part.Text
		if partType == "refusal" {
			field = &part.Refusal
		}
		claimed[int64(i)] = true
		if blank(*field) {
			if delta := deltas.at(int64(i)); delta != "" {
				*field = delta
			}
		}
		present = present || !blank(*field)
	}
	from := declared
	if !present {
		from = math.MinInt64
	}
	return deltas.unclaimed(claimed, from)
}

// 还原函数与自定义工具参数，并保留完成事件声明的值。
func (s *openAICompatOutputSlot) buildToolCall() (apicompat.ResponsesOutput, bool) {
	if s.done == nil && s.added == nil {
		return apicompat.ResponsesOutput{}, false
	}
	var item apicompat.ResponsesOutput
	if s.done != nil {
		item = *s.done
	} else {
		item = *s.added
	}
	if s.added != nil {
		if item.CallID == "" {
			item.CallID = s.added.CallID
		}
		if item.Name == "" {
			item.Name = s.added.Name
		}
		if item.Namespace == "" {
			item.Namespace = s.added.Namespace
		}
		if item.EncryptedFunctionArgs == nil {
			item.EncryptedFunctionArgs = s.added.EncryptedFunctionArgs
		}
	}
	arguments := item.Arguments
	if item.Type == "custom_tool_call" {
		arguments = item.Input
	}
	if arguments == "" {
		arguments = s.argsDone
	}
	if arguments == "" {
		arguments = s.args.String()
	}
	if item.Type == "custom_tool_call" {
		item.Input = arguments
	} else {
		item.Arguments = arguments
	}
	return item, true
}
