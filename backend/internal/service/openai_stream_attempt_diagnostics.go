package service

import (
	"context"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// 只把字段集合已知、没有内容与副作用的事件当作前导；未知字段继续保守提交。
// @project-doc docs/interfaces/openai_upstream.md#openai_stream_failover_observation
func openAIStreamSafeEmptyEvent(payload string, eventType string) bool {
	heartbeat := eventType == "ping" || eventType == "heartbeat" || eventType == "keepalive"
	switch eventType {
	case "ping", "heartbeat", "keepalive":
	case "response.output_text.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta", "response.audio_transcript.delta":
	default:
		return false
	}
	if !gjson.Valid(payload) {
		return false
	}
	value := gjson.Parse(payload)
	if !value.IsObject() {
		return false
	}
	if !heartbeat {
		delta := value.Get("delta")
		if !delta.Exists() || delta.Type != gjson.String || delta.String() != "" {
			return false
		}
	}
	safe := true
	var seen uint16
	value.ForEach(func(key, field gjson.Result) bool {
		var bit uint16
		switch key.String() {
		case "type":
			bit = 1
			safe = field.Type == gjson.String && field.String() == eventType
		case "sequence_number":
			bit = 2
			safe = openAIStreamSafeIndex(field)
		case "delta":
			bit = 4
			safe = !heartbeat && field.Type == gjson.String && field.String() == ""
		case "item_id":
			bit = 8
			safe = !heartbeat && field.Type == gjson.String
		case "output_index", "content_index", "summary_index":
			bit = map[string]uint16{"output_index": 16, "content_index": 32, "summary_index": 64}[key.String()]
			safe = !heartbeat && openAIStreamSafeIndex(field)
		default:
			// 包括 usage、工具、密文及未来新增字段，不能仅因看不到文本就重放。
			safe = false
		}
		if seen&bit != 0 {
			safe = false
		}
		seen |= bit
		return safe
	})
	return safe
}

func openAIStreamSafeIndex(value gjson.Result) bool {
	if value.Type != gjson.Number {
		return false
	}
	_, err := strconv.ParseUint(value.Raw, 10, 64)
	return err == nil
}

// 诊断只保留固定分类和有界事件名，不保存正文、工具参数或推理密文。
type openAIStreamAttemptDiagnostic struct {
	firstCommitEvent  string
	firstCommitReason string
	retryBlockReason  string
	logged            bool
}

func openAIStreamDiagnosticEventName(eventType string) string {
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		return "missing_type"
	}
	switch eventType {
	case "error", "response.failed", "response.incomplete", "response.cancelled", "response.completed", "response.done",
		"response.output_text.delta", "response.output_text.done", "response.reasoning_text.delta", "response.reasoning_text.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "response.audio_transcript.delta", "response.audio_transcript.done",
		"response.output_item.added", "response.output_item.done", "response.content_part.added", "response.content_part.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done", "response.function_call_arguments.delta", "response.function_call_arguments.done",
		"response.custom_tool_call_input.delta", "response.custom_tool_call_input.done", "response.image_generation_call.partial_image":
		return eventType
	}
	return "unrecognized"
}

func (d *openAIStreamAttemptDiagnostic) commit(eventType string, visible bool) {
	if d.firstCommitEvent != "" {
		return
	}
	d.firstCommitEvent = openAIStreamDiagnosticEventName(eventType)
	d.firstCommitReason = "conservative_non_visible_event"
	if visible {
		d.firstCommitReason = "visible_output"
	} else if eventType == "error" || eventType == "response.failed" {
		d.firstCommitReason = "terminal_error"
	}
}

func (d *openAIStreamAttemptDiagnostic) failoverBlocked(outputStarted, usageObserved bool) {
	if d.retryBlockReason != "" {
		return
	}
	if usageObserved {
		d.retryBlockReason = "upstream_usage_observed"
	} else if outputStarted {
		d.retryBlockReason = "client_output_started"
		if d.firstCommitEvent == "" {
			d.firstCommitReason = "preexisting_writer_output"
		}
	}
}

func (d *openAIStreamAttemptDiagnostic) log(ctx context.Context, account *Account, path, requestID, outcome string, disconnected bool) {
	if d.logged {
		return
	}
	d.logged = true
	fields := []zap.Field{
		zap.String("stream_path", path),
		zap.String("upstream_request_id", strings.TrimSpace(requestID)),
		zap.String("outcome", outcome),
		zap.String("first_commit_event", d.firstCommitEvent),
		zap.String("first_commit_reason", d.firstCommitReason),
		zap.String("retry_block_reason", d.retryBlockReason),
		zap.Bool("client_disconnected", disconnected),
	}
	if account != nil {
		fields = append(fields, zap.Int64("account_id", account.ID), zap.String("platform", account.Platform))
	}
	logger.FromContext(ctx).Info("openai.stream_attempt_diagnostic", fields...)
}
