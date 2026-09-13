package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apicompat"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/TokenFlux/TokenRouter/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// forwardResponsesViaRawChatCompletions 将 `/v1/responses` 入站请求桥接到
// 只支持 `/v1/chat/completions` 的上游。
func (s *OpenAIGatewayService) forwardResponsesViaRawChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	tlsRouterMatch ...TLSFingerprintRouterMatchResult,
) (*OpenAIForwardResult, error) {
	startTime := time.Now()

	var responsesReq apicompat.ResponsesRequest
	if err := json.Unmarshal(body, &responsesReq); err != nil {
		writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return nil, fmt.Errorf("parse responses request: %w", err)
	}
	originalModel := strings.TrimSpace(responsesReq.Model)
	if originalModel == "" {
		writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}

	clientStream := responsesReq.Stream
	serviceTier := extractOpenAIServiceTierFromBody(body)
	// custom 工具（如 codex 的 exec）降级为 function 工具转发，回程需按名字还原为
	// custom_tool_call 项，先记下名字集合；tool_search 工具同理，回程还原为
	// tool_search_call 项；namespace 子工具（如 MCP 工具）摊平转发，回程按映射还原
	// 为带 namespace 字段的 function_call 项。
	effectiveTools, err := apicompat.EffectiveResponsesTools(&responsesReq)
	if err != nil {
		writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, fmt.Errorf("resolve responses tools: %w", err)
	}
	customTools := apicompat.CustomToolNames(effectiveTools)
	functionTools := apicompat.FunctionToolNames(effectiveTools)
	toolSearch := apicompat.HasToolSearchTool(effectiveTools)
	namespaceTools := apicompat.NamespaceToolNames(effectiveTools)

	// 带明文 summary 的历史 reasoning 顺手刷新缓存，帮助 encrypted-only 副本自愈。
	s.recacheReasoningItemsFromInput(responsesReq.Input)
	chatReq, err := apicompat.ResponsesToChatCompletionsRequestWithOptions(&responsesReq, &apicompat.ResponsesToChatOptions{
		ReasoningContentByID: s.reasoningContentByID,
	})
	if err != nil {
		writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, fmt.Errorf("convert responses to chat completions: %w", err)
	}

	billingModel := resolveOpenAIForwardModel(account, originalModel, "")
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	chatReq.Model = upstreamModel
	if clientStream {
		chatReq.StreamOptions = &apicompat.ChatStreamOptions{IncludeUsage: true}
	}

	chatBody, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("marshal chat completions fallback request: %w", err)
	}
	chatBody, err = s.applyOpenAIFastPolicyToBody(ctx, account, upstreamModel, chatBody)
	if err != nil {
		var blocked *OpenAIFastBlockedError
		if errors.As(err, &blocked) {
			writeOpenAIFastPolicyBlockedResponse(c, blocked)
		}
		return nil, err
	}
	// Usage Log 以 Responses→Chat 转换和策略处理后的最终上游请求为准。
	reasoningEffort := extractEffectiveOpenAIReasoningEffortFromBody(chatBody, body, upstreamModel, billingModel, originalModel)
	// 国产模型没有显式 effort 档位时，thinking 启用后补默认展示值。
	reasoningEffort = ApplyThinkingEnabledFallback(reasoningEffort, chatBody, billingModel)
	if serviceTier == nil {
		serviceTier = extractOpenAIServiceTierFromBody(chatBody)
	}

	logger.L().Debug("openai responses: forwarding via raw chat completions",
		zap.Int64("account_id", account.ID),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", clientStream),
	)
	SetOpsUpstreamModel(c, upstreamModel)

	// 通过共享 CC 管线构造并发送上游请求。
	apiKey, targetURL, err := s.resolveCCFallbackTarget(account)
	if err != nil {
		return nil, err
	}
	SetActualOpenAIUpstreamEndpoint(c, "/v1/chat/completions")
	resp, err := s.sendCCUpstreamRequest(ctx, c, account, targetURL, chatBody, clientStream, apiKey, account.GetOpenAIUserAgent(), "", tlsRouterMatch...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, upstreamMsg := s.readOpenAIUpstreamError(resp)
		if foErr := s.failoverOpenAIUpstreamHTTPError(ctx, c, account, resp, respBody, upstreamMsg, upstreamModel); foErr != nil {
			return nil, foErr
		}
		return s.handleErrorResponse(ctx, resp, c, account, chatBody, billingModel)
	}

	if clientStream {
		return s.streamChatCompletionsAsResponses(c, resp, originalModel, customTools, functionTools, toolSearch, namespaceTools, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime, account)
	}
	return s.bufferChatCompletionsAsResponses(c, resp, originalModel, customTools, functionTools, toolSearch, namespaceTools, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime)
}

func (s *OpenAIGatewayService) bufferChatCompletionsAsResponses(
	c *gin.Context,
	resp *http.Response,
	originalModel string,
	customTools map[string]bool,
	functionTools map[string]bool,
	toolSearch bool,
	namespaceTools map[string]apicompat.NamespacedToolName,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	serviceTier *string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")
	ccResp, usage, err := s.readCCUpstreamJSONResponse(c, resp, writeOpenAIResponsesFallbackError)
	if err != nil {
		return nil, err
	}
	responsesResp := apicompat.ChatCompletionsResponseToResponses(ccResp, originalModel, customTools, functionTools, toolSearch, namespaceTools)
	s.cacheReasoningItemsFromOutput(responsesResp.Output)

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	c.JSON(http.StatusOK, responsesResp)

	return &OpenAIForwardResult{
		RequestID:                   requestID,
		UpstreamHeaders:             resp.Header,
		Usage:                       usage,
		Model:                       originalModel,
		BillingModel:                billingModel,
		UpstreamModel:               upstreamModel,
		UpstreamResponseServiceTier: observedUpstreamResponseServiceTier(c),
		ReasoningEffort:             reasoningEffort,
		ServiceTier:                 resolvedOpenAIUpstreamServiceTier(c, serviceTier),
		Stream:                      false,
		Duration:                    time.Since(startTime),
	}, nil
}

func (s *OpenAIGatewayService) streamChatCompletionsAsResponses(
	c *gin.Context,
	resp *http.Response,
	originalModel string,
	customTools map[string]bool,
	functionTools map[string]bool,
	toolSearch bool,
	namespaceTools map[string]apicompat.NamespacedToolName,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	serviceTier *string,
	startTime time.Time,
	account *Account,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")
	writeStreamHeaders := s.newStreamHeaderWriter(c, resp.Header)

	state := apicompat.NewChatCompletionsToResponsesStreamState(originalModel)
	state.CustomTools = customTools
	state.FunctionTools = functionTools
	state.ToolSearchDeclared = toolSearch
	state.NamespaceTools = namespaceTools
	clientDisconnected := false
	outputStarted := false
	var pendingEvents []apicompat.ResponsesStreamEvent
	var scan ccStreamScanState
	defer func() { logSmartRoutingChatStreamOutcome(c, account, scan, clientDisconnected) }()

	writeEvents := func(events []apicompat.ResponsesStreamEvent) {
		// created/role-only 前导先留在本 attempt，首个业务输出或正常终态才提交。
		if !outputStarted {
			pendingEvents = append(pendingEvents, events...)
			return
		}
		if len(pendingEvents) > 0 {
			events = append(pendingEvents, events...)
			pendingEvents = nil
		}
		if clientDisconnected || len(events) == 0 {
			return
		}
		writeStreamHeaders()
		for _, event := range events {
			sse, err := apicompat.ResponsesEventToSSE(event)
			if err != nil {
				logger.L().Warn("openai responses chat fallback: failed to marshal stream event",
					zap.Error(err),
					zap.String("request_id", requestID),
				)
				continue
			}
			if _, err := fmt.Fprint(c.Writer, sse); err != nil {
				clientDisconnected = true
				logger.L().Debug("openai responses chat fallback: client disconnected, continuing to drain upstream for billing",
					zap.Error(err),
					zap.String("request_id", requestID),
				)
				return
			}
		}
		c.Writer.Flush()
	}

	scan = s.scanCCStream(c, resp, "openai responses chat fallback", requestID, startTime, func(chunk *apicompat.ChatCompletionsChunk) {
		outputStarted = outputStarted || chatChunkStartsResponsesOutput(chunk)
		events := apicompat.ChatCompletionsChunkToResponsesEvents(chunk, state)
		s.cacheReasoningItemsFromEvents(events)
		writeEvents(events)
	})
	if scan.Err == nil {
		if err := state.ValidateToolCallArguments(); err != nil {
			// 工具参数截断也属于上游流失败，统一返回明确终态并保留已观测用量。
			scan.Err = fmt.Errorf("invalid tool call arguments from upstream: %w", err)
			scan.FailurePayload = []byte(`{"error":{"type":"server_error","status":502,"message":"Upstream returned invalid JSON tool call arguments"}}`)
		}
	}

	if scan.Err != nil {
		status, errType, message, streamErr := s.resolveCCStreamFailure(c, resp, account, scan, upstreamModel)
		var failover *UpstreamFailoverError
		if errors.As(streamErr, &failover) {
			return nil, failover
		}
		// 错误记录独立于下游是否仍可写；取消后排水得到的显式上游错误也不能丢失。
		if status > 0 {
			MarkOpsStreamError(c, errType, message, status)
		}
		if status > 0 && !clientDisconnected && c.Request.Context().Err() == nil {
			if scan.SawOutput || scan.SawUsage || c.Writer.Written() {
				outputStarted = true
				MarkResponseCommitted(c)
				writeEvents([]apicompat.ResponsesStreamEvent{{
					Type: "response.failed", SequenceNumber: state.SequenceNumber,
					Response: &apicompat.ResponsesResponse{
						ID: state.ResponseID, Object: "response", CreatedAt: state.Created,
						Model: originalModel, Status: "failed", Output: []apicompat.ResponsesOutput{},
						Error: &apicompat.ResponsesError{Code: errType, Message: message, StatusCode: status},
					},
				}})
			} else {
				writeOpenAIResponsesFallbackError(c, status, errType, message)
			}
		}
		if !scan.SawOutput && !scan.SawUsage {
			return nil, streamErr
		}
		return &OpenAIForwardResult{
			RequestID:                   requestID,
			UpstreamHeaders:             resp.Header,
			Usage:                       scan.Usage,
			Model:                       originalModel,
			BillingModel:                billingModel,
			UpstreamModel:               upstreamModel,
			UpstreamResponseServiceTier: normalizeObservedOpenAIServiceTier(scan.ServiceTier),
			ReasoningEffort:             reasoningEffort,
			ServiceTier:                 resolvedOpenAIUpstreamServiceTier(c, serviceTier),
			Stream:                      true,
			Duration:                    time.Since(startTime),
			FirstTokenMs:                scan.FirstTokenMs,
			ClientDisconnect:            clientDisconnected || c.Request.Context().Err() != nil,
		}, streamErr
	}
	outputStarted = true
	finalEvents := apicompat.FinalizeChatCompletionsResponsesStream(state)
	s.cacheReasoningItemsFromEvents(finalEvents)
	writeEvents(finalEvents)
	if !clientDisconnected {
		writeStreamHeaders()
		if _, err := fmt.Fprint(c.Writer, "data: [DONE]\n\n"); err != nil {
			clientDisconnected = true
		}
		if !clientDisconnected {
			c.Writer.Flush()
		}
	}
	if !scan.SawDone {
		logCCStreamMissingDoneSentinel("openai responses chat fallback", requestID)
	}

	return &OpenAIForwardResult{
		RequestID:                   requestID,
		UpstreamHeaders:             resp.Header,
		Usage:                       scan.Usage,
		Model:                       originalModel,
		BillingModel:                billingModel,
		UpstreamModel:               upstreamModel,
		UpstreamResponseServiceTier: normalizeObservedOpenAIServiceTier(scan.ServiceTier),
		ReasoningEffort:             reasoningEffort,
		ServiceTier:                 resolvedOpenAIUpstreamServiceTier(c, serviceTier),
		Stream:                      true,
		Duration:                    time.Since(startTime),
		FirstTokenMs:                scan.FirstTokenMs,
		ClientDisconnect:            clientDisconnected || c.Request.Context().Err() != nil,
	}, nil
}

func chatChunkStartsResponsesOutput(chunk *apicompat.ChatCompletionsChunk) bool {
	if chunk == nil {
		return false
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != nil && *choice.Delta.Content != "" ||
			choice.Delta.ReasoningText() != nil && *choice.Delta.ReasoningText() != "" || len(choice.Delta.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

const responsesReasoningCacheTTL = 7 * 24 * time.Hour

// reasoningContentByID 按 reasoning item id 回查缓存。缓存不可用或未命中时
// 返回空字符串，保持桥接原有 fail-open 行为。
func (s *OpenAIGatewayService) reasoningContentByID(itemID string) string {
	if s == nil || s.cache == nil {
		return ""
	}
	cache, ok := s.cache.(ReasoningContentCache)
	if !ok {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	content, err := cache.GetReasoningContent(ctx, itemID)
	if err != nil {
		return ""
	}
	return content
}

// recacheReasoningItemsFromInput 用请求历史里仍带明文的 reasoning item 刷新缓存，
// 帮助 Redis 清理或跨实例漂移后的 encrypted-only 副本恢复。
func (s *OpenAIGatewayService) recacheReasoningItemsFromInput(inputRaw json.RawMessage) {
	if s == nil || s.cache == nil {
		return
	}
	if _, ok := s.cache.(ReasoningContentCache); !ok {
		return
	}
	inputRaw = bytes.TrimSpace(inputRaw)
	if len(inputRaw) == 0 || inputRaw[0] != '[' {
		return
	}
	var items []json.RawMessage
	if err := json.Unmarshal(inputRaw, &items); err != nil {
		return
	}
	for _, raw := range items {
		id, text, ok := apicompat.ExtractResponsesReasoningItem(raw)
		if ok && id != "" && text != "" {
			s.setReasoningContent(id, text)
		}
	}
}

// cacheReasoningItemsFromEvents 从 Responses 流事件里提取已完成的 reasoning item。
func (s *OpenAIGatewayService) cacheReasoningItemsFromEvents(events []apicompat.ResponsesStreamEvent) {
	for _, event := range events {
		if event.Type == "response.output_item.done" && event.Item != nil {
			s.cacheReasoningItem(event.Item)
		}
	}
}

// cacheReasoningItemsFromOutput 从非流式 Responses 输出中提取 reasoning item。
func (s *OpenAIGatewayService) cacheReasoningItemsFromOutput(output []apicompat.ResponsesOutput) {
	for i := range output {
		s.cacheReasoningItem(&output[i])
	}
}

func (s *OpenAIGatewayService) cacheReasoningItem(item *apicompat.ResponsesOutput) {
	if item == nil || item.Type != "reasoning" || item.ID == "" {
		return
	}
	parts := make([]string, 0, len(item.Summary))
	for _, summary := range item.Summary {
		if text := strings.TrimSpace(summary.Text); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) > 0 {
		s.setReasoningContent(item.ID, strings.Join(parts, "\n"))
	}
}

// setReasoningContent 使用 detached context 写入缓存，客户端断连后仍可完成
// 上游 drain；缓存失败只记录日志，不影响当前响应。
func (s *OpenAIGatewayService) setReasoningContent(itemID, content string) {
	if s == nil || s.cache == nil {
		return
	}
	cache, ok := s.cache.(ReasoningContentCache)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := cache.SetReasoningContent(ctx, itemID, content, responsesReasoningCacheTTL); err != nil {
		logger.L().Warn("openai responses chat fallback: cache reasoning content failed",
			zap.Error(err),
			zap.String("item_id", itemID),
		)
	}
}
