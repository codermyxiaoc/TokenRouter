package apicompat

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func feedResponsesEvents(events ...*ResponsesStreamEvent) []AnthropicStreamEvent {
	state := NewResponsesEventToAnthropicState()
	var out []AnthropicStreamEvent
	for _, evt := range events {
		out = append(out, ResponsesEventToAnthropicEvents(evt, state)...)
	}
	return out
}

func collectAnthropicText(events []AnthropicStreamEvent) string {
	text := ""
	for _, event := range events {
		if event.Type == "content_block_delta" && event.Delta != nil && event.Delta.Type == "text_delta" {
			text += event.Delta.Text
		}
	}
	return text
}

func requireAnthropicBlockLifecycle(t *testing.T, events []AnthropicStreamEvent) {
	t.Helper()

	open := false
	next := 0
	for _, event := range events {
		switch event.Type {
		case "content_block_start":
			require.False(t, open, "content_block_start while a block is open")
			require.NotNil(t, event.Index)
			require.Equal(t, next, *event.Index, "content block indices must increase by one")
			open = true
		case "content_block_delta":
			require.True(t, open, "content_block_delta outside an open block")
			require.NotNil(t, event.Index)
			require.Equal(t, next, *event.Index)
		case "content_block_stop":
			require.True(t, open, "content_block_stop without an open block")
			require.NotNil(t, event.Index)
			require.Equal(t, next, *event.Index)
			open = false
			next++
		}
	}
	require.False(t, open, "stream ended with an unclosed content block")
}

func responsesCreated() *ResponsesStreamEvent {
	return &ResponsesStreamEvent{
		Type:     "response.created",
		Response: &ResponsesResponse{ID: "resp_text_recovery", Model: "gpt-5.2"},
	}
}

func responsesMessageOutput(texts ...string) []ResponsesOutput {
	content := make([]ResponsesContentPart, 0, len(texts))
	for _, text := range texts {
		content = append(content, ResponsesContentPart{Type: "output_text", Text: text})
	}
	return []ResponsesOutput{{Type: "message", Role: "assistant", Content: content}}
}

func TestResponsesEventToAnthropicEvents_RecoversTextFromDoneEvent(t *testing.T) {
	const answer = "recovered from done"

	events := feedResponsesEvents(
		responsesCreated(),
		&ResponsesStreamEvent{Type: "response.output_item.added", Item: &ResponsesOutput{Type: "message"}},
		&ResponsesStreamEvent{Type: "response.output_text.done", Text: answer},
		&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}},
	)

	assert.Equal(t, answer, collectAnthropicText(events))
	requireAnthropicBlockLifecycle(t, events)
	require.NotEmpty(t, events)
	assert.Equal(t, "message_stop", events[len(events)-1].Type)
}

func TestResponsesEventToAnthropicEvents_RecoversTextFromTerminalOutput(t *testing.T) {
	const answer = "recovered from terminal"

	events := feedResponsesEvents(
		responsesCreated(),
		&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{
			Status: "completed",
			Output: responsesMessageOutput(answer),
		}},
	)

	assert.Equal(t, answer, collectAnthropicText(events))
	requireAnthropicBlockLifecycle(t, events)
	require.NotEmpty(t, events)
	assert.Equal(t, "message_stop", events[len(events)-1].Type)
}

func TestResponsesEventToAnthropicEvents_DoesNotDuplicateTextDeliveredByDeltas(t *testing.T) {
	events := feedResponsesEvents(
		responsesCreated(),
		&ResponsesStreamEvent{Type: "response.output_text.delta", Delta: "Hel"},
		&ResponsesStreamEvent{Type: "response.output_text.delta", Delta: "lo"},
		&ResponsesStreamEvent{Type: "response.output_text.done", Text: "Hello"},
		&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{
			Status: "completed",
			Output: responsesMessageOutput("Hello"),
		}},
	)

	assert.Equal(t, "Hello", collectAnthropicText(events))
	requireAnthropicBlockLifecycle(t, events)
}

func TestResponsesEventToAnthropicEvents_DoesNotDuplicateTextRecoveredFromDoneEvent(t *testing.T) {
	const answer = "recovered once"

	events := feedResponsesEvents(
		responsesCreated(),
		&ResponsesStreamEvent{Type: "response.output_text.done", Text: answer},
		&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{
			Status: "completed",
			Output: responsesMessageOutput(answer),
		}},
	)

	assert.Equal(t, answer, collectAnthropicText(events))
	requireAnthropicBlockLifecycle(t, events)
}

func TestResponsesEventToAnthropicEvents_DoesNotDuplicateTextWhenBlockClosedBeforeDone(t *testing.T) {
	events := feedResponsesEvents(
		responsesCreated(),
		&ResponsesStreamEvent{Type: "response.output_text.delta", Delta: "Hello"},
		&ResponsesStreamEvent{
			Type:        "response.output_item.added",
			OutputIndex: 1,
			Item:        &ResponsesOutput{Type: "function_call", CallID: "call_1", Name: "lookup"},
		},
		&ResponsesStreamEvent{Type: "response.output_text.done", Text: "Hello"},
		&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}},
	)

	assert.Equal(t, "Hello", collectAnthropicText(events))
	requireAnthropicBlockLifecycle(t, events)
}

func TestResponsesEventToAnthropicEvents_AppendsOnlySuffixMissingFromDeltas(t *testing.T) {
	events := feedResponsesEvents(
		responsesCreated(),
		&ResponsesStreamEvent{Type: "response.output_text.delta", Delta: "Hel"},
		&ResponsesStreamEvent{Type: "response.output_text.done", Text: "Hello world"},
		&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{
			Status: "completed",
			Output: responsesMessageOutput("Hello world"),
		}},
	)

	assert.Equal(t, "Hello world", collectAnthropicText(events))
	requireAnthropicBlockLifecycle(t, events)
}

func TestResponsesEventToAnthropicEvents_IgnoresDonePayloadDivergingFromDeltas(t *testing.T) {
	events := feedResponsesEvents(
		responsesCreated(),
		&ResponsesStreamEvent{Type: "response.output_text.delta", Delta: "Hello"},
		&ResponsesStreamEvent{Type: "response.output_text.done", Text: "Goodbye"},
		&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}},
	)

	assert.Equal(t, "Hello", collectAnthropicText(events))
	requireAnthropicBlockLifecycle(t, events)
}

func TestResponsesEventToAnthropicEvents_DoesNotDuplicateWhenTerminalOutputIsIndexedDifferently(t *testing.T) {
	events := feedResponsesEvents(
		responsesCreated(),
		&ResponsesStreamEvent{
			Type:        "response.output_item.added",
			OutputIndex: 0,
			Item:        &ResponsesOutput{Type: "reasoning", ID: "rs_1"},
		},
		&ResponsesStreamEvent{Type: "response.output_text.delta", OutputIndex: 1, Delta: "Hello"},
		&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{
			Status: "completed",
			Output: responsesMessageOutput("Hello"),
		}},
	)

	assert.Equal(t, "Hello", collectAnthropicText(events))
	requireAnthropicBlockLifecycle(t, events)
}

func TestResponsesEventToAnthropicEvents_LeavesTerminalOutputAloneOnceTextStreamed(t *testing.T) {
	events := feedResponsesEvents(
		responsesCreated(),
		&ResponsesStreamEvent{Type: "response.output_text.delta", ContentIndex: 0, Delta: "first"},
		&ResponsesStreamEvent{Type: "response.output_text.done", ContentIndex: 0, Text: "first"},
		&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{
			Status: "completed",
			Output: responsesMessageOutput("first", "second"),
		}},
	)

	assert.Equal(t, "first", collectAnthropicText(events))
	requireAnthropicBlockLifecycle(t, events)
}

func TestResponsesEventToAnthropicEvents_RecoversEveryTerminalPartWhenNothingStreamed(t *testing.T) {
	events := feedResponsesEvents(
		responsesCreated(),
		&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{
			Status: "completed",
			Output: responsesMessageOutput("first", "second"),
		}},
	)

	assert.Equal(t, "firstsecond", collectAnthropicText(events))
	requireAnthropicBlockLifecycle(t, events)

	starts := 0
	for _, event := range events {
		if event.Type == "content_block_start" {
			starts++
		}
	}
	assert.Equal(t, 1, starts, "contiguous recovered text belongs in one block")
}

func TestResponsesEventToAnthropicEvents_LeavesToolOnlyTurnTextFree(t *testing.T) {
	events := feedResponsesEvents(
		responsesCreated(),
		&ResponsesStreamEvent{
			Type:        "response.output_item.added",
			OutputIndex: 0,
			Item:        &ResponsesOutput{Type: "function_call", CallID: "call_1", Name: "lookup"},
		},
		&ResponsesStreamEvent{
			Type:        "response.function_call_arguments.done",
			OutputIndex: 0,
			Arguments:   `{"id":1}`,
		},
		&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{
			Status: "completed",
			Output: []ResponsesOutput{{Type: "function_call", CallID: "call_1", Name: "lookup", Arguments: `{"id":1}`}},
		}},
	)

	assert.Empty(t, collectAnthropicText(events))
	requireAnthropicBlockLifecycle(t, events)
}

func TestResponsesEventToAnthropicEvents_RecoversTextOnEveryTerminalAlias(t *testing.T) {
	for _, terminal := range []string{
		"response.completed",
		"response.done",
		"response.incomplete",
		"response.failed",
	} {
		t.Run(terminal, func(t *testing.T) {
			const answer = "recovered on a terminal alias"

			events := feedResponsesEvents(
				responsesCreated(),
				&ResponsesStreamEvent{Type: terminal, Response: &ResponsesResponse{
					Status: "completed",
					Output: responsesMessageOutput(answer),
				}},
			)

			assert.Equal(t, answer, collectAnthropicText(events))
			requireAnthropicBlockLifecycle(t, events)
			require.NotEmpty(t, events)
			assert.Equal(t, "message_stop", events[len(events)-1].Type)
		})
	}
}

func TestResponsesEventToAnthropicEvents_DoesNotDuplicateWhenDoneIsIndexedDifferently(t *testing.T) {
	for _, tc := range []struct {
		name string
		done *ResponsesStreamEvent
	}{
		{"content index diverges", &ResponsesStreamEvent{Type: "response.output_text.done", ContentIndex: 1, Text: "Hello world"}},
		{"output index diverges", &ResponsesStreamEvent{Type: "response.output_text.done", OutputIndex: 1, Text: "Hello world"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := feedResponsesEvents(
				responsesCreated(),
				&ResponsesStreamEvent{Type: "response.output_text.delta", Delta: "Hello world"},
				tc.done,
				&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}},
			)

			assert.Equal(t, "Hello world", collectAnthropicText(events))
			requireAnthropicBlockLifecycle(t, events)
		})
	}
}

func TestResponsesEventToAnthropicEvents_LeavesUnmatchableDoneAloneAfterPartialDeltas(t *testing.T) {
	events := feedResponsesEvents(
		responsesCreated(),
		&ResponsesStreamEvent{Type: "response.output_text.delta", ContentIndex: 0, Delta: "Hello"},
		&ResponsesStreamEvent{Type: "response.output_text.done", ContentIndex: 1, Text: "Hello world"},
		&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}},
	)

	assert.Equal(t, "Hello", collectAnthropicText(events))
	requireAnthropicBlockLifecycle(t, events)
}

func TestResponsesEventToAnthropicEvents_KeysDeliveredTextByBothIndices(t *testing.T) {
	events := feedResponsesEvents(
		responsesCreated(),
		&ResponsesStreamEvent{Type: "response.output_text.delta", OutputIndex: 0, ContentIndex: 0, Delta: "first"},
		&ResponsesStreamEvent{Type: "response.output_text.done", OutputIndex: 0, ContentIndex: 0, Text: "first"},
		&ResponsesStreamEvent{Type: "response.output_text.delta", OutputIndex: 0, ContentIndex: 1, Delta: "second"},
		&ResponsesStreamEvent{Type: "response.output_text.done", OutputIndex: 0, ContentIndex: 1, Text: "second-tail"},
		&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}},
	)

	assert.Equal(t, "firstsecond-tail", collectAnthropicText(events))
	requireAnthropicBlockLifecycle(t, events)
}

func TestResponsesEventToAnthropicEvents_IgnoresDoneEventAfterMessageStop(t *testing.T) {
	events := feedResponsesEvents(
		responsesCreated(),
		&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{
			Status: "completed",
			Output: responsesMessageOutput("the answer"),
		}},
		&ResponsesStreamEvent{Type: "response.output_text.done", OutputIndex: 7, Text: "late text"},
	)

	assert.Equal(t, "the answer", collectAnthropicText(events))
	requireAnthropicBlockLifecycle(t, events)
	require.NotEmpty(t, events)
	assert.Equal(t, "message_stop", events[len(events)-1].Type, "no content may follow message_stop")
}

func TestResponsesEventToAnthropicEvents_LeavesToolArgumentsDoneTextFree(t *testing.T) {
	events := feedResponsesEvents(
		responsesCreated(),
		&ResponsesStreamEvent{
			Type:        "response.output_item.added",
			OutputIndex: 0,
			Item:        &ResponsesOutput{Type: "function_call", CallID: "call_1", Name: "lookup"},
		},
		&ResponsesStreamEvent{
			Type:        "response.function_call_arguments.done",
			OutputIndex: 0,
			Arguments:   `{"id":1}`,
			Text:        "must not be emitted",
		},
		&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}},
	)

	assert.Empty(t, collectAnthropicText(events))
	requireAnthropicBlockLifecycle(t, events)
}

func TestResponsesEventToAnthropicEvents_PreservesThinkingSignatureWhenRecovering(t *testing.T) {
	events := feedResponsesEvents(
		responsesCreated(),
		&ResponsesStreamEvent{
			Type:        "response.output_item.added",
			OutputIndex: 0,
			Item:        &ResponsesOutput{Type: "reasoning", EncryptedContent: "sig-abc"},
		},
		&ResponsesStreamEvent{Type: "response.output_text.done", OutputIndex: 1, Text: "recovered"},
		&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}},
	)

	assert.Equal(t, "recovered", collectAnthropicText(events))
	requireAnthropicBlockLifecycle(t, events)

	var signatures, stopsBefore int
	for _, event := range events {
		if event.Type == "content_block_delta" && event.Delta != nil && event.Delta.Type == "signature_delta" {
			assert.Equal(t, "sig-abc", event.Delta.Signature)
			signatures++
			assert.Equal(t, 0, stopsBefore, "signature_delta must precede the thinking block stop")
		}
		if event.Type == "content_block_stop" {
			stopsBefore++
		}
	}
	assert.Equal(t, 1, signatures, "the thinking signature must survive recovery")
}

func TestResponsesEventToAnthropicEvents_RecoveredTextSurvivesTheSyntheticFinalizer(t *testing.T) {
	state := NewResponsesEventToAnthropicState()
	var events []AnthropicStreamEvent
	for _, evt := range []*ResponsesStreamEvent{
		responsesCreated(),
		{Type: "response.output_text.done", Text: "recovered before the stream died"},
	} {
		events = append(events, ResponsesEventToAnthropicEvents(evt, state)...)
	}
	events = append(events, FinalizeResponsesAnthropicStream(state)...)

	assert.Equal(t, "recovered before the stream died", collectAnthropicText(events))
	requireAnthropicBlockLifecycle(t, events)
	require.NotEmpty(t, events)
	assert.Equal(t, "message_stop", events[len(events)-1].Type)
}

func TestResponsesEventToAnthropicEvents_DeclinesTerminalRecoveryWithoutMessageText(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *ResponsesResponse
	}{
		{"no response payload", nil},
		{"no message item", &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{{Type: "reasoning"}}}},
		{"message without output_text", &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{{
			Type:    "message",
			Role:    "assistant",
			Content: []ResponsesContentPart{{Type: "refusal", Text: "refused"}},
		}}}},
		{"empty output_text", &ResponsesResponse{Status: "completed", Output: responsesMessageOutput("")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := feedResponsesEvents(
				responsesCreated(),
				&ResponsesStreamEvent{Type: "response.completed", Response: tc.response},
			)

			assert.Empty(t, collectAnthropicText(events))
			requireAnthropicBlockLifecycle(t, events)
		})
	}
}

func TestResponsesEventToAnthropicEvents_RecoversEveryTerminalMessageItem(t *testing.T) {
	events := feedResponsesEvents(
		responsesCreated(),
		&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{
			Status: "completed",
			Output: []ResponsesOutput{
				{Type: "reasoning"},
				{Type: "message", Role: "assistant", Content: []ResponsesContentPart{{Type: "output_text", Text: "first"}}},
				{Type: "function_call", CallID: "call_1", Name: "lookup"},
				{Type: "message", Role: "assistant", Content: []ResponsesContentPart{{Type: "output_text", Text: "second"}}},
			},
		}},
	)

	assert.Equal(t, "firstsecond", collectAnthropicText(events))
	requireAnthropicBlockLifecycle(t, events)
}

func TestResponsesEventToAnthropicEvents_EmitsDoneTextWhileAToolBlockIsOpen(t *testing.T) {
	events := feedResponsesEvents(
		responsesCreated(),
		&ResponsesStreamEvent{
			Type:        "response.output_item.added",
			OutputIndex: 0,
			Item:        &ResponsesOutput{Type: "function_call", CallID: "call_1", Name: "lookup"},
		},
		&ResponsesStreamEvent{Type: "response.output_text.done", OutputIndex: 1, Text: "spoken after the call"},
		&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}},
	)

	assert.Equal(t, "spoken after the call", collectAnthropicText(events))
	requireAnthropicBlockLifecycle(t, events)
}
