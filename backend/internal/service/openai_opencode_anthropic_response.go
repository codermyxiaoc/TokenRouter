package service

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apicompat"
	"github.com/TokenFlux/TokenRouter/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// @project-doc docs/interfaces/opencode_upstream.md#opencode_session_and_billing
// OpenCode 独立校验原生 Anthropic 终态，避免旧桥把截断流补成成功。
// 只缓存没有正文/用量的前导事件；产生内容后立即转换发送，断连后仍排水计量。
func (s *OpenAIGatewayService) handleOpenCodeNativeAnthropicResponse(
	resp *http.Response, c *gin.Context, account *Account, ingress string, clientStream bool,
	originalModel, billingModel, upstreamModel string, reasoningEffort *string, start time.Time,
	toolMapping apicompat.ResponsesClientToolMapping, includeUsage bool,
) (*OpenAIForwardResult, error) {
	var usage ClaudeUsage
	var scan ccStreamScanState
	var finalResponse *apicompat.AnthropicResponse
	var originalJSON []byte
	var firstToken *int
	clientDisconnected := false
	seenStop := false
	responseState := apicompat.NewAnthropicEventToResponsesState()
	responseState.Model = originalModel
	chatState := apicompat.NewResponsesEventToChatState()
	chatState.Model, chatState.IncludeUsage = originalModel, includeUsage
	restorer := apicompat.NewResponsesClientToolStreamRestorer(toolMapping)
	result := func() *OpenAIForwardResult {
		return &OpenAIForwardResult{RequestID: resp.Header.Get("x-request-id"), UpstreamHeaders: resp.Header,
			Model: originalModel, BillingModel: billingModel, UpstreamModel: upstreamModel, UpstreamEndpoint: GetActualOpenAIUpstreamEndpoint(c),
			Usage: claudeUsageToOpenAIUsage(&usage), ReasoningEffort: reasoningEffort, Stream: clientStream,
			Duration: time.Since(start), FirstTokenMs: firstToken, ClientDisconnect: clientDisconnected || c.Request.Context().Err() != nil}
	}
	writeSSE := func(event string, payload []byte) {
		if clientDisconnected || c.Request.Context().Err() != nil {
			clientDisconnected = true
			return
		}
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("X-Accel-Buffering", "no")
		MarkResponseCommitted(c)
		var err error
		if event == "" {
			_, err = fmt.Fprintf(c.Writer, "data: %s\n\n", payload)
		} else {
			_, err = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, payload)
		}
		if err != nil {
			clientDisconnected = true
		} else {
			c.Writer.Flush()
		}
	}
	writeResponsesEvent := func(event apicompat.ResponsesStreamEvent) {
		payload, err := json.Marshal(event)
		if err != nil {
			return
		}
		payload = reverseToolNamesIfPresent(c, payload)
		payloads, _, err := restorer.RestoreEvent(payload)
		if err != nil {
			scan.Err = fmt.Errorf("restore responses tools: %w", err)
			return
		}
		for _, restored := range payloads {
			writeSSE(gjson.GetBytes(restored, "type").String(), restored)
		}
	}
	emit := func(event *apicompat.AnthropicStreamEvent, payload []byte) {
		if !clientStream {
			return
		}
		if ingress == APIProtocolAnthropic {
			writeSSE(event.Type, reverseToolNamesIfPresent(c, payload))
			return
		}
		for _, converted := range apicompat.AnthropicEventToResponsesEvents(event, responseState) {
			if ingress == APIProtocolResponses {
				writeResponsesEvent(converted)
				continue
			}
			for _, chunk := range apicompat.ResponsesEventToChatChunks(&converted, chatState) {
				if encoded, err := json.Marshal(chunk); err == nil {
					writeSSE("", reverseToolNamesIfPresent(c, encoded))
				}
			}
		}
	}
	var pending [][]byte
	pendingBytes := 0
	process := func(payload []byte, eventName string) bool {
		if len(payload) == 0 {
			return true
		}
		if !gjson.ValidBytes(payload) {
			scan.Err = errors.New("invalid upstream Anthropic event")
			return false
		}
		mergeOpenCodeAnthropicUsage(&usage, parseClaudeUsageFromResponseBody(payload))
		if message := gjson.GetBytes(payload, "message"); message.Exists() {
			mergeOpenCodeAnthropicUsage(&usage, parseClaudeUsageFromResponseBody([]byte(message.Raw)))
		}
		scan.SawUsage = usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.CacheCreationInputTokens > 0 || usage.CacheReadInputTokens > 0
		if failure := ccStreamEventErrorPayload(payload, eventName); len(failure) > 0 {
			scan.FailurePayload, scan.Err = failure, errors.New("upstream Anthropic stream failed")
			return false
		}
		var event apicompat.AnthropicStreamEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			scan.Err = err
			return false
		}
		if event.Type == "" {
			event.Type = eventName
		}
		if openCodeAnthropicEventHasOutput(payload) {
			scan.SawOutput = true
			if firstToken == nil {
				ms := int(time.Since(start).Milliseconds())
				firstToken = &ms
			}
		}
		switch event.Type {
		case "message_start":
			if event.Message == nil {
				scan.Err = errors.New("missing Anthropic message_start body")
				return false
			}
			finalResponse = event.Message
		case "message_delta":
			if event.Delta != nil && event.Delta.StopReason != "" && finalResponse != nil {
				finalResponse.StopReason = apicompat.AnthropicStopReasonPtr(event.Delta.StopReason)
			}
		case "content_block_start":
			if !clientStream && finalResponse != nil && event.ContentBlock != nil {
				finalResponse.Content = append(finalResponse.Content, *event.ContentBlock)
			}
		case "content_block_delta":
			if !clientStream && finalResponse != nil && event.Delta != nil && event.Index != nil {
				idx := *event.Index
				if idx < 0 || idx >= len(finalResponse.Content) {
					scan.Err = errors.New("invalid Anthropic content index")
					return false
				}
				switch event.Delta.Type {
				case "text_delta":
					finalResponse.Content[idx].Text += event.Delta.Text
				case "thinking_delta":
					finalResponse.Content[idx].Thinking += event.Delta.Thinking
				case "input_json_delta":
					finalResponse.Content[idx].Input = appendRawJSON(finalResponse.Content[idx].Input, event.Delta.PartialJSON)
				}
			}
		case "message_stop":
			seenStop = finalResponse != nil
		}
		if clientStream {
			if !scan.SawOutput && !scan.SawUsage && !seenStop {
				// 前导上限只限制尚不能交付的元数据，不对正常长响应做全量缓冲。
				pendingBytes += len(payload)
				if pendingBytes > 64*1024 {
					scan.Err = errors.New("Anthropic stream prelude too large")
					return false
				}
				pending = append(pending, append([]byte(nil), payload...))
			} else {
				for _, buffered := range pending {
					var prior apicompat.AnthropicStreamEvent
					_ = json.Unmarshal(buffered, &prior)
					emit(&prior, buffered)
				}
				pending = nil
				emit(&event, payload)
			}
		}
		return !seenStop && scan.Err == nil
	}

	// Messages 非流式使用原生 JSON；流式请求若收到 JSON 错误也必须提取真实状态。
	if (ingress == APIProtocolAnthropic && !clientStream) || strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
		body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, anthropicTooLargeError)
		if ingress == APIProtocolAnthropic && !clientStream {
			originalJSON = body
		}
		mergeOpenCodeAnthropicUsage(&usage, parseClaudeUsageFromResponseBody(body))
		scan.SawUsage = usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.CacheCreationInputTokens > 0 || usage.CacheReadInputTokens > 0
		scan.SawOutput = openCodeAnthropicEventHasOutput(body)
		scan.FailurePayload = ccStreamErrorPayload(body)
		scan.Err = err
		if len(scan.FailurePayload) > 0 {
			scan.Err = errors.New("upstream Anthropic response failed")
		}
		if scan.Err == nil {
			if err := json.Unmarshal(body, &finalResponse); err != nil {
				scan.Err = err
			} else if finalResponse == nil || finalResponse.Type != "message" || finalResponse.StopReason == nil {
				scan.Err = errors.New("incomplete Anthropic response")
			} else {
				seenStop = true
			}
		}
		if clientStream && scan.Err == nil {
			// 已生成的 JSON 不能冒充已交付的 SSE 成功，也不能因此重新执行请求。
			seenStop = false
			scan.Err = errors.New("upstream returned JSON for an Anthropic stream request")
		}
	} else {
		scanner := bufio.NewScanner(resp.Body)
		limit := defaultMaxLineSize
		if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
			limit = s.cfg.Gateway.MaxLineSize
		}
		scanner.Buffer(make([]byte, 64*1024), limit)
		pump := newAnthropicNativeLinePump(scanner, s.anthropicNativeStreamInterval())
		defer pump.stop()
		var data strings.Builder
		eventName := ""
		keepReading := true
		flush := func() {
			if data.Len() > 0 {
				keepReading = process([]byte(strings.TrimSuffix(data.String(), "\n")), eventName)
			}
			data.Reset()
			eventName = ""
		}
		for keepReading {
			line, err := pump.next()
			if err != nil {
				flush()
				if !seenStop && scan.Err == nil {
					scan.Err = err
				}
				if errors.Is(err, errAnthropicNativeStreamIdle) {
					_ = resp.Body.Close()
				}
				break
			}
			if strings.TrimSpace(line) == "" {
				flush()
				continue
			}
			if event, ok := extractOpenAISSEEventLine(line); ok {
				eventName = event
				continue
			}
			if payload, ok := extractOpenAISSEDataLine(line); ok {
				if data.Len()+len(payload) > limit {
					scan.Err = errors.New("Anthropic event too large")
					break
				}
				data.WriteString(payload)
				data.WriteByte('\n')
			}
		}
	}
	if !seenStop && scan.Err == nil {
		scan.Err = io.ErrUnexpectedEOF
	}
	if scan.Err != nil {
		status, errType, message, forwardErr := s.resolveCCStreamFailure(c, resp, account, scan, upstreamModel)
		var failover *UpstreamFailoverError
		if errors.As(forwardErr, &failover) {
			return nil, failover
		}
		if status > 0 {
			MarkOpsStreamError(c, errType, message, status)
		}
		if status > 0 && !clientDisconnected && c.Request.Context().Err() == nil {
			if clientStream && (scan.SawOutput || scan.SawUsage || c.Writer.Written()) {
				if ingress == APIProtocolResponses {
					writeResponsesEvent(apicompat.ResponsesStreamEvent{Type: "response.failed", SequenceNumber: responseState.SequenceNumber, Response: &apicompat.ResponsesResponse{ID: responseState.ResponseID, Object: "response", Model: originalModel, Status: "failed", Output: []apicompat.ResponsesOutput{}, Error: &apicompat.ResponsesError{Code: errType, Message: message, StatusCode: status}}})
				} else {
					payload, _ := json.Marshal(gin.H{"type": "error", "error": gin.H{"type": errType, "message": message}})
					writeSSE("error", payload)
				}
			} else if ingress == APIProtocolAnthropic {
				writeAnthropicError(c, status, errType, message)
			} else {
				writeOpenAIResponsesFallbackError(c, status, errType, message)
			}
		}
		if scan.SawOutput || scan.SawUsage {
			return result(), forwardErr
		}
		return nil, forwardErr
	}
	if clientStream {
		if ingress == APIProtocolChatCompletions {
			writeSSE("", []byte("[DONE]"))
		}
		return result(), nil
	}
	if originalJSON != nil {
		// 原生 JSON 成功响应保留所有供应商扩展字段与强制缓存计费转换。
		if IsForceCacheBilling(c.Request.Context()) && usage.InputTokens > 0 {
			var err error
			originalJSON, err = classifyAnthropicResponseInputAsCacheRead(originalJSON, &usage)
			if err != nil {
				return result(), err
			}
		}
		writeAnthropicPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
		c.Data(http.StatusOK, "application/json; charset=utf-8", reverseToolNamesIfPresent(c, originalJSON))
		return result(), nil
	}
	finalResponse.Usage = apicompat.AnthropicUsage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CacheCreationInputTokens: usage.CacheCreationInputTokens, CacheReadInputTokens: usage.CacheReadInputTokens}
	var output any = finalResponse
	if ingress != APIProtocolAnthropic {
		converted := apicompat.AnthropicToResponsesResponse(finalResponse)
		converted.Model = originalModel
		output = converted
		if ingress == APIProtocolChatCompletions {
			output = apicompat.ResponsesToChatCompletions(converted, originalModel)
		}
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return result(), err
	}
	encoded = reverseToolNamesIfPresent(c, encoded)
	if ingress == APIProtocolResponses {
		encoded, _, err = apicompat.RestoreResponsesClientToolPayload(encoded, toolMapping)
		if err != nil {
			return result(), err
		}
	}
	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	c.Data(http.StatusOK, "application/json; charset=utf-8", encoded)
	return result(), nil
}

// 保留 Anthropic 缓存子桶，零值增量不能覆盖之前累计的用量。
func mergeOpenCodeAnthropicUsage(dst, src *ClaudeUsage) {
	if src == nil {
		return
	}
	if src.InputTokens > 0 {
		dst.InputTokens = src.InputTokens
	}
	if src.OutputTokens > 0 {
		dst.OutputTokens = src.OutputTokens
	}
	if src.CacheReadInputTokens > 0 {
		dst.CacheReadInputTokens = src.CacheReadInputTokens
	}
	if src.CacheCreationInputTokens > 0 {
		dst.CacheCreationInputTokens = src.CacheCreationInputTokens
	}
	if src.CacheCreation5mTokens > 0 {
		dst.CacheCreation5mTokens = src.CacheCreation5mTokens
	}
	if src.CacheCreation1hTokens > 0 {
		dst.CacheCreation1hTokens = src.CacheCreation1hTokens
	}
}

func openCodeAnthropicEventHasOutput(payload []byte) bool {
	for _, field := range []string{"delta.text", "delta.thinking", "delta.partial_json", "content_block.text", "content_block.thinking"} {
		if gjson.GetBytes(payload, field).String() != "" {
			return true
		}
	}
	for _, path := range []string{"content", "message.content"} {
		for _, item := range gjson.GetBytes(payload, path).Array() {
			if item.Get("text").String() != "" || item.Get("thinking").String() != "" || item.Get("type").String() == "tool_use" {
				return true
			}
		}
	}
	return gjson.GetBytes(payload, "content_block.type").String() == "tool_use"
}
