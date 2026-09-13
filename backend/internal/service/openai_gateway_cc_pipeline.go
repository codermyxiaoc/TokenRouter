package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apicompat"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/TokenFlux/TokenRouter/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// 本文件收敛三个 CC（Chat Completions）forwarder 之间重复的 HTTP 管线与 SSE
// 循环骨架（PR #3802 遗留项）：
//
//   - forwardAsRawChatCompletions          （原生 CC 直转）
//   - forwardResponsesViaRawChatCompletions（/v1/responses → CC 回退）
//   - forwardAnthropicViaRawChatCompletions（/v1/messages → CC 回退）
//
// 以及 messages / chat_completions 两条 Responses 主路径中逐字相同的错误处理块。
// 共享流扫描统一识别流内错误、用量及完成边界；各路径的协议事件转换和
// ClientDisconnect 语义仍留在调用方。

// newUpstreamSSEScanner 构造读取上游 SSE 流的行扫描器，按配置放大单行上限。
func (s *OpenAIGatewayService) newUpstreamSSEScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	return scanner
}

// newStreamHeaderWriter 返回幂等的 SSE 响应头写入闭包：首次调用时透传过滤后的
// 上游响应头并写入标准 SSE 头 + 200 状态码，后续调用为 no-op。延迟到首个事件
// 写出前才提交响应头，使上游早期失败仍可改走 failover 或非流式错误响应。
func (s *OpenAIGatewayService) newStreamHeaderWriter(c *gin.Context, upstream http.Header) func() {
	headersWritten := false
	return func() {
		if headersWritten {
			return
		}
		headersWritten = true
		if s.responseHeaderFilter != nil {
			responseheaders.WriteFilteredHeaders(c.Writer.Header(), upstream, s.responseHeaderFilter)
		}
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("X-Accel-Buffering", "no")
		c.Writer.WriteHeader(http.StatusOK)
	}
}

// readOpenAIUpstreamError 读取上游错误体并把 resp.Body 回卷为可重读的副本
// （下游 handleXxxErrorResponse 需要再次读取），返回原始错误体与脱敏后的
// 上游错误消息。
func (s *OpenAIGatewayService) readOpenAIUpstreamError(resp *http.Response) ([]byte, string) {
	respBody := s.readUpstreamErrorBody(resp)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(respBody))

	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
	return respBody, upstreamMsg
}

// failoverOpenAIUpstreamHTTPError 对 >=400 的上游响应做 failover 判定：命中时
// 记录 ops 事件、执行账号级错误处置并返回 *UpstreamFailoverError；未命中返回
// nil，调用方继续走各自端点格式的非 failover 错误处理链。
func (s *OpenAIGatewayService) failoverOpenAIUpstreamHTTPError(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	resp *http.Response,
	respBody []byte,
	upstreamMsg string,
	upstreamModel string,
) *UpstreamFailoverError {
	shouldFailover := s.shouldFailoverOpenAIUpstreamResponse(resp.StatusCode, upstreamMsg, respBody)
	if account != nil && account.Platform == PlatformGrok {
		shouldFailover = s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody)
	}
	// 请求级拒绝不能触发账号策略或池模式重试。
	if detectHit, _, _ := detectOpenAICyberPolicy(respBody); detectHit ||
		IsOpenAICyberWarningPayload(respBody, upstreamMsg) ||
		isOpenAIClientInvalidRequestError(resp.StatusCode, upstreamMsg, respBody) ||
		isOpenAIContextWindowError(upstreamMsg, respBody) ||
		(account != nil && account.Platform == PlatformGrok && isGrokContentPolicyRejection(resp.StatusCode, respBody)) {
		return nil
	}
	// 没有 gin 上下文时无法安全评估请求级临时规则；保持上游语义，
	// 仅让默认已判定为可故障转移的错误继续进入账号策略管线。
	if c == nil && !shouldFailover && (account == nil || account.Platform != PlatformGrok) {
		return nil
	}
	var decision UpstreamErrorDecision
	if account != nil && account.Platform == PlatformGrok {
		decision = s.applyGrokAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, upstreamModel)
	} else {
		decision = s.applyOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, upstreamModel)
	}
	if decision.ShouldReturnGenericError() || !decision.ShouldFailover(account, resp.StatusCode, shouldFailover) {
		return nil
	}
	upstreamDetail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = truncateString(string(respBody), maxBytes)
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  resp.Header.Get("x-request-id"),
		Kind:               "failover",
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})
	return newOpenAIUpstreamFailoverError(
		resp.StatusCode,
		resp.Header,
		respBody,
		upstreamMsg,
		decision.RetryableOnSameAccount(account, resp.StatusCode),
	)
}

// openAIChatCompletionsTargetURL 解析账号的（非 Grok）Chat Completions 上游端点。
func (s *OpenAIGatewayService) openAIChatCompletionsTargetURL(account *Account) (string, error) {
	baseURL := account.GetOpenAIBaseURL()
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	validatedURL, err := s.validateUpstreamBaseURL(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}
	return buildOpenAIChatCompletionsURL(validatedURL), nil
}

// resolveCCFallbackTarget 解析两条 CC 回退路径共用的账号凭证与上游端点
// （回退路径仅面向 APIKey 账号，凭证恒为 openai api_key）。
func (s *OpenAIGatewayService) resolveCCFallbackTarget(account *Account) (apiKey string, targetURL string, err error) {
	apiKey = strings.TrimSpace(account.GetOpenAIProtocolAPIKey())
	if apiKey == "" {
		return "", "", fmt.Errorf("account %d missing api_key", account.ID)
	}
	targetURL, err = s.openAIChatCompletionsTargetURL(account)
	if err != nil {
		return "", "", err
	}
	return apiKey, targetURL, nil
}

// sendCCUpstreamRequest 构建并发送 CC 上游请求：分离的上游 context、OpenAI HTTP
// profile、标准头（含流式 Accept 切换）、客户端 header 白名单透传、自定义 UA 与
// 账号级 header 覆写，最后经代理发出。传输层失败（DNS/TCP/TLS，无 HTTP 响应）
// 统一由 handleOpenAIUpstreamTransportError 归一为 failover。
//
// OpenAI 账号统一通过 fork 的 UA 路由规则选择；userAgent 参数仅用于 Grok
// 等非 OpenAI 平台的显式 UA。
func (s *OpenAIGatewayService) sendCCUpstreamRequest(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	targetURL string,
	body []byte,
	stream bool,
	bearerToken string,
	userAgent string,
	grokCacheIdentity string,
	tlsRouterMatch ...TLSFingerprintRouterMatchResult,
) (*http.Response, error) {
	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	upstreamReq, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, targetURL, bytes.NewReader(body))
	releaseUpstreamCtx()
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}
	// 记录本次实际选择的协议端点，供错误日志和用量日志在没有
	// OpenAIForwardResult（例如 503/传输失败）时使用。每次发送都覆盖，
	// 避免 Gin context 在账号 failover 尝试之间残留旧端点。
	SetActualOpenAIUpstreamEndpoint(c, "/v1/chat/completions")
	upstreamReq = upstreamReq.WithContext(WithHTTPUpstreamProfile(upstreamReq.Context(), HTTPUpstreamProfileOpenAI))
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Authorization", "Bearer "+bearerToken)
	if stream {
		upstreamReq.Header.Set("Accept", "text/event-stream")
	} else {
		upstreamReq.Header.Set("Accept", "application/json")
	}

	// 透传白名单中的客户端 header。详见 openaiCCRawAllowedHeaders 的设计说明。
	for key, values := range c.Request.Header {
		lowerKey := strings.ToLower(key)
		if openaiCCRawAllowedHeaders[lowerKey] {
			for _, v := range values {
				upstreamReq.Header.Add(key, v)
			}
		}
	}
	if len(tlsRouterMatch) == 0 {
		tlsRouterMatch = []TLSFingerprintRouterMatchResult{s.matchTLSFingerprintRouter(c, account)}
	}
	if account.Platform == PlatformGrok && userAgent != "" {
		upstreamReq.Header.Set("user-agent", userAgent)
	} else if account.Platform != PlatformGrok {
		s.applyOpenAIUpstreamUserAgent(c.Request.Context(), c, account, upstreamReq, false, tlsRouterMatch[0])
	}

	if account.Platform == PlatformGrok {
		if account.IsGrokOAuth() {
			applyGrokCLIHeaders(upstreamReq.Header)
		}
		applyGrokCacheHeaders(upstreamReq.Header, grokCacheIdentity)
	}
	// 账号级请求头覆写：放在所有内置默认头（含 Grok CLI 身份头）之后应用，
	// 使配置值获得除共享传输层强制头之外的最高优先级。
	account.ApplyHeaderOverrides(upstreamReq.Header)
	applyOpenCodeSessionHeader(c, account, targetURL, upstreamReq.Header)

	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.DoWithTLS(upstreamReq, proxyURL, account.ID, account.Concurrency, s.resolveOpenAITLSProfile(account, tlsRouterMatch...))
	if err != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}
	return resp, nil
}

// ccStreamScanState 是 scanCCStream 返回的读取状态快照。
type ccStreamScanState struct {
	// Usage 为 include_usage chunk 中最近一次出现的用量（上游可能重复发送，
	// 总是保留最新值）；终态事件中的用量由调用方在 finalize 阶段自行覆盖。
	Usage OpenAIUsage
	// FirstTokenMs 为首个实际输出 chunk（排除 usage-only chunk）的到达时延。
	FirstTokenMs *int
	// ServiceTier 为 SSE chunk 中无歧义的上游实际档位。
	ServiceTier string
	// SawDone 表示上游发出了 [DONE] 哨兵。
	SawDone bool
	// SawFinish 为有效 finish_reason；兼容上游完成后省略 [DONE] 的情况。
	SawFinish bool
	// FinishReason 仅保留标准终态名称，供诊断使用，不记录供应商自由文本。
	FinishReason string
	// 输出或用量已经出现后，失败也不能再重放本次上游调用。
	SawOutput bool
	SawUsage  bool
	// FailurePayload 保留 HTTP 200 内错误的原始状态与分类信息。
	FailurePayload []byte
	// Err 为 scanner 读错误（客户端 context 取消不属于此类，会原样带出）。
	// 非 nil 时调用方必须跳过 finalize 并返回 usage-incomplete 错误，避免
	// 把上游截断伪装成正常收尾。
	Err error
}

// scanCCStream 驱动两条 CC 回退路径共享的 SSE 读循环：提取 data 行、在 [DONE]
// 哨兵处停止、保留最新 usage、记录首 token 时延，并把每个解析成功的 chunk 交给
// emit 回调做各自的协议转换与写出。读错误按既有约定过滤 context 取消类噪声后
// 记入 Warn 日志。
func (s *OpenAIGatewayService) scanCCStream(
	c *gin.Context,
	resp *http.Response,
	logPrefix string,
	requestID string,
	startTime time.Time,
	emit func(*apicompat.ChatCompletionsChunk),
) ccStreamScanState {
	var st ccStreamScanState
	tierObserver := &upstreamResponseModelObserver{}
	// 少数兼容上游对流式请求返回 HTTP 200 JSON 错误，不能当作没有 data 行的空成功。
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
		body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
		if usage := extractCCStreamUsage(string(body)); usage != nil {
			st.Usage, st.SawUsage = *usage, true
		}
		st.FailurePayload = ccStreamErrorPayload(body)
		st.Err = err
		if st.Err == nil {
			st.Err = errors.New("chat stream returned a JSON response")
		}
		return st
	}

	processPayload := func(payload, eventType string) bool {
		payload = strings.TrimSpace(payload)
		// 错误帧也可能附带已计量的用量，必须先保存，禁止把该请求重新执行。
		if usage := extractCCStreamUsage(payload); usage != nil {
			st.Usage, st.SawUsage = *usage, true
		}
		// SSE 的 event 字段也决定错误语义；扁平错误对象不能被当成空 Chat chunk。
		if failure := ccStreamEventErrorPayload([]byte(payload), eventType); len(failure) > 0 {
			st.FailurePayload = failure
			st.Err = errors.New("upstream chat stream returned an error")
			return false
		}
		if payload == "" {
			return true
		}
		if payload == "[DONE]" {
			st.SawDone = true
			return false
		}
		tierObserver.ObserveOpenAI([]byte(payload), openAIChatCompletionServiceTierEventType([]byte(payload)))
		// 观察上游 CC chunk 回显的 model / service_tier（计费以回显为准）。
		// CC chunk 无 type 字段，按 untyped payload 观察（上游约束：只有终止
		// 事件与无类型 body 报告实际处理档位）。
		if observer := upstreamResponseModelObserverFromContext(c); observer != nil {
			observer.ObserveOpenAI([]byte(payload), "")
		}

		var chunk apicompat.ChatCompletionsChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			logger.L().Warn(logPrefix+": failed to parse chat stream chunk",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
			// 单个无法解析的 chunk 不应阻断后续合法事件；最终工具参数由
			// Responses 转换状态在收尾时单独校验。
			return true
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != nil && strings.TrimSpace(*choice.FinishReason) != "" {
				st.SawFinish = true
				switch reason := strings.TrimSpace(*choice.FinishReason); reason {
				case "stop", "length", "tool_calls", "function_call", "content_filter":
					st.FinishReason = reason
				}
			}
		}
		if chatChunkStartsResponsesOutput(&chunk) {
			st.SawOutput = true
		}
		if st.FirstTokenMs == nil && !isOpenAIChatUsageOnlyStreamChunk(payload) && st.SawOutput {
			ms := int(time.Since(startTime).Milliseconds())
			st.FirstTokenMs = &ms
		}
		emit(&chunk)
		return true
	}

	// 复用现有 SSE frame 解析器，并限制所有多行 data 的总大小，不能靠拆行绕过上限。
	var parser openAICompatSSEFrameParser
	frameBytes := 0
	maxFrameBytes := maxOpenAIConcatenatedJSONBytes
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 && s.cfg.Gateway.MaxLineSize < maxFrameBytes {
		maxFrameBytes = s.cfg.Gateway.MaxLineSize
	}
	processFrame := func(frame openAICompatSSEFrame) bool {
		if frame.EventType == "error" || gjson.Valid(frame.Data) || strings.TrimSpace(frame.Data) == "[DONE]" {
			return processPayload(frame.Data, frame.EventType)
		}
		// 保留历史兼容：部分上游连续发送独立 JSON data 行而省略事件间空行。
		for _, data := range strings.Split(frame.Data, "\n") {
			if !processPayload(data, frame.EventType) {
				return false
			}
		}
		return true
	}
	flushFrame := func() bool {
		frame, ok := parser.Finish()
		frameBytes = 0
		return !ok && frame.EventType != "error" || processFrame(frame)
	}
	scanner := s.newUpstreamSSEScanner(resp.Body)
	keepReading := true
	for keepReading && scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			keepReading = flushFrame()
			continue
		}
		if _, ok := extractOpenAISSEEventLine(line); ok {
			// 兼容未使用空行隔开的下一事件，禁止把两帧的错误名称和正文混在一起。
			if len(parser.dataLines) > 0 && !flushFrame() {
				keepReading = false
				break
			}
			parser.AddLine(line)
			continue
		}
		if data, ok := extractOpenAISSEDataLine(line); ok {
			if frameBytes+len(data)+1 > maxFrameBytes || len(parser.dataLines) >= 4096 {
				st.Err = errors.New("upstream chat SSE event exceeds size limit")
				keepReading = false
				break
			}
			frameBytes += len(data) + 1
			parser.AddLine(line)
			// 一行完整 JSON 立即转发，兼容无空行流；多行按帧结束解析，避免每一行重复拼接。
			if len(parser.dataLines) == 1 && (gjson.Valid(data) || strings.TrimSpace(data) == "[DONE]") {
				keepReading = flushFrame()
			}
			continue
		}
		// 兼容错误 Content-Type 下的一行裸 JSON 错误，普通 SSE 文本/注释不进入此分支。
		if failure := ccStreamErrorPayload([]byte(strings.TrimSpace(line))); len(failure) > 0 {
			keepReading = flushFrame() && processPayload(line, "")
		}
	}
	if keepReading {
		flushFrame()
	}
	st.ServiceTier = tierObserver.ServiceTier()

	if err := scanner.Err(); err != nil && st.Err == nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			logger.L().Warn(logPrefix+": stream read error",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
		}
		st.Err = err
	}
	if st.Err == nil && (!st.SawDone && !st.SawFinish || !st.SawOutput && !st.SawUsage && !st.SawFinish) {
		st.Err = errors.New("upstream chat stream ended before a completion terminal")
	}
	return st
}

// ccStreamEventErrorPayload 只在明确 event:error 时包装扁平或非 JSON 错误，不扫描正常文本。
func ccStreamEventErrorPayload(payload []byte, eventType string) []byte {
	if failure := ccStreamErrorPayload(payload); len(failure) > 0 {
		return failure
	}
	if eventType != "error" {
		return nil
	}
	if gjson.ValidBytes(payload) && gjson.ParseBytes(payload).IsObject() {
		wrapped, _ := json.Marshal(map[string]json.RawMessage{"error": payload})
		return wrapped
	}
	message := strings.TrimSpace(string(payload))
	if value := gjson.ParseBytes(payload); value.Type == gjson.String {
		message = value.String()
	}
	if message == "" {
		message = "Upstream chat stream returned an error event"
	}
	wrapped, _ := json.Marshal(gin.H{"error": gin.H{"type": "server_error", "message": message}})
	return wrapped
}

// ccStreamErrorPayload 只识别协议错误对象，不把普通文本中的 error 字样误判成错误。
func ccStreamErrorPayload(payload []byte) []byte {
	if !gjson.ValidBytes(payload) {
		return nil
	}
	for _, path := range []string{"error", "response.error"} {
		value := gjson.GetBytes(payload, path)
		if value.Exists() && value.Type != gjson.Null {
			return bytes.Clone(payload)
		}
	}
	if gjson.GetBytes(payload, "type").String() == "error" {
		// 裸 error 事件也有 type/code/message，统一包装便于既有分类器读取。
		wrapped, _ := json.Marshal(map[string]json.RawMessage{"error": payload})
		return wrapped
	}
	return nil
}

// resolveCCStreamFailure 共用现有流错误账号策略和 Ops，只有未输出、未观测用量时允许切号。
// @project-doc docs/architecture/gateway_request_lifecycle.md#account_selection_and_failover
func (s *OpenAIGatewayService) resolveCCStreamFailure(c *gin.Context, resp *http.Response, account *Account, scan ccStreamScanState, model string) (int, string, string, error) {
	// 纯客户端取消不是上游故障；排水已收到的显式上游错误仍须进入 Ops 和分组冷却。
	if clientErr := c.Request.Context().Err(); clientErr != nil && len(scan.FailurePayload) == 0 {
		return 0, "", "", clientErr
	}
	payload := scan.FailurePayload
	message := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(payload)))
	if message == "" {
		message = "Upstream chat stream ended before completion"
	}
	if len(payload) == 0 {
		payload, _ = json.Marshal(gin.H{"error": gin.H{"type": "server_error", "message": message, "status": http.StatusBadGateway}})
	}
	status := openAIStreamFailedEventSemanticStatus(payload, message)
	// 聚合上游也会把真实状态放在顶层；必须先保留显式状态再补标准嵌套字段。
	for _, path := range []string{"response.error.status_code", "response.error.status", "error.status_code", "error.status", "status_code", "status"} {
		if explicit := int(gjson.GetBytes(payload, path).Int()); explicit >= 400 && explicit <= 599 {
			status = explicit
			break
		}
	}
	// 标准 invalid_request 没有数字状态时仍应返回 400；补状态供 Ops 与既有策略一致使用。
	statusPath := "error.status"
	if gjson.GetBytes(payload, "response.error").Exists() {
		statusPath = "response.error.status"
	}
	if normalized, err := sjson.SetBytes(payload, statusPath, status); err == nil {
		payload = normalized
	}
	policyStatus, decision := s.applyOpenAIStreamFailedAccountPolicy(c.Request.Context(), account, model, resp.Header, payload, message)
	if c.Request.Context().Err() == nil && !scan.SawOutput && !scan.SawUsage && !c.Writer.Written() &&
		decision.ShouldFailover(account, policyStatus, openAIStreamFailedEventShouldFailover(payload, message)) {
		// 策略已执行一次；构造错误不能重复暂停账号或累计冷却。
		markOpenAIWSFailureSideEffectsApplied(c, policyStatus, decision.StopScheduling)
		failure := s.newOpenAIStreamFailoverErrorWithModel(c, account, false, resp.Header.Get("x-request-id"), payload, message, model, resp.Header)
		return status, "upstream_error", message, failure
	}
	if scan.SawOutput || scan.SawUsage {
		MarkSmartRoutingAttemptNonReplayable(c.Request.Context(), "upstream_stream_output_or_usage")
	}
	s.recordOpenAIStreamUpstreamError(c, account, false, resp.Header.Get("x-request-id"), "stream_error", payload, message)
	errType := "upstream_error"
	if status == http.StatusTooManyRequests {
		errType = "rate_limit_error"
	} else if status < http.StatusInternalServerError {
		errType = "invalid_request_error"
	}
	if decision.ShouldReturnGenericError() {
		status, errType, message = http.StatusInternalServerError, "upstream_error", "Upstream gateway error"
	} else if ruleStatus, ruleType, ruleMessage, matched := applyOpenAIStreamFailedErrorPassthroughRule(c, account.Platform, payload, message); matched {
		status, errType, message = ruleStatus, ruleType, ruleMessage
	}
	// 使用 handler 已识别的错误前缀，避免已发出的失败终态被再追加一遍。
	return status, errType, message, fmt.Errorf("upstream response failed: stream usage incomplete: %s: %w", message, scan.Err)
}

// logCCStreamMissingDoneSentinel 记录"上游未发 [DONE] 哨兵即结束"的 debug 日志。
func logCCStreamMissingDoneSentinel(logPrefix, requestID string) {
	logger.L().Debug(logPrefix+": upstream stream ended without done sentinel",
		zap.String("request_id", requestID),
	)
}

// readCCUpstreamJSONResponse 读取并解析 CC 非流式 JSON 响应，失败时以调用方
// 端点格式回写错误；成功时顺带提取 usage。
func (s *OpenAIGatewayService) readCCUpstreamJSONResponse(
	c *gin.Context,
	resp *http.Response,
	writeError compatErrorWriter,
) (*apicompat.ChatCompletionsResponse, OpenAIUsage, error) {
	respBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		if !errors.Is(err, ErrUpstreamResponseBodyTooLarge) {
			writeError(c, http.StatusBadGateway, "api_error", "Failed to read upstream response")
		}
		return nil, OpenAIUsage{}, fmt.Errorf("read upstream body: %w", err)
	}

	var ccResp apicompat.ChatCompletionsResponse
	if err := json.Unmarshal(respBody, &ccResp); err != nil {
		writeError(c, http.StatusBadGateway, "api_error", "Failed to parse upstream response")
		return nil, OpenAIUsage{}, fmt.Errorf("parse chat completions response: %w", err)
	}
	observeOpenAIServiceTierInContext(c, respBody, "response.completed")
	// 观察上游 CC JSON 回显的 model / service_tier（计费以回显为准）。
	// CC JSON 无 type 字段，按 untyped payload 观察（上游约束）。
	if observer := upstreamResponseModelObserverFromContext(c); observer != nil {
		observer.ObserveOpenAI(respBody, "")
	}

	usage := OpenAIUsage{}
	if parsed, ok := extractOpenAIUsageFromJSONBytes(respBody); ok {
		usage = parsed
	}
	return &ccResp, usage, nil
}

// writeOpenAIResponsesFallbackError 以 /v1/responses 回退路径的既有错误格式回写
// （裸 error 对象；不调用 MarkResponseCommitted，与原内联写法保持一致）。
func writeOpenAIResponsesFallbackError(c *gin.Context, statusCode int, errType, message string) {
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}
