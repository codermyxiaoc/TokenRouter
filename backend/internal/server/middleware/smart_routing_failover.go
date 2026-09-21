package middleware

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const smartRoutingExecutionKey = "smart_routing_execution"

// SmartRoutingRetryGuard 显式复核路由门禁；重试上下文不重入 Gin 中间件链。
type SmartRoutingRetryGuard func(*gin.Context) bool

type smartRoutingCooldownResolver interface {
	GetSmartRoutingCooldown(context.Context, int64, int64) (time.Duration, error)
	CooldownSmartRoutingGroup(context.Context, int64, int64, time.Duration) error
}

// smartRoutingExecution 仅存在于本次 HTTP 请求，禁止共享认证快照保存尝试状态。
type smartRoutingExecution struct {
	base               *gin.Context
	request            *http.Request
	body               []byte
	parent             gin.ResponseWriter
	writer             *smartRoutingAttemptWriter
	attempt            *service.SmartRoutingAttemptState
	visited            map[int64]bool
	keyID, groupID     int64
	cooldown           time.Duration
	replayBlockReason  string
	active             bool
	selectionExhausted bool
}

// runSmartRoutingAuthentication 延迟提交失败响应，成功流仍在首次业务输出时立即放行。
// @project-doc docs/domains/smart_routing_api_keys.md#group_failover
func runSmartRoutingAuthentication(c *gin.Context, resolver service.SmartRoutingGroupResolver, authenticate gin.HandlerFunc, guards []SmartRoutingRetryGuard) {
	state := &smartRoutingExecution{base: c.Copy(), parent: c.Writer, visited: make(map[int64]bool)}
	c.Set(smartRoutingExecutionKey, state)
	terminal := c.Handler()
	// 首轮业务在认证的 Next 内执行，必须提前注册清理，确保 panic 后 Recovery 能写到底层。
	// 异常退出只恢复已启用的智能写入器，不提交未完成的响应缓冲，也不改变普通 Key。
	defer func() {
		if state.active {
			c.Writer = state.parent
		}
	}()
	authenticate(c)
	if !state.active || state.writer == nil {
		return
	}
	current := c
	var history []*service.OpsUpstreamErrorEvent
	for attempts := 0; ; attempts++ {
		w := state.writer
		// 下游已断开时无法写失败帧，仍须接受 service 明确记录的本轮失败终态。
		// 单独的历史上游错误可能已经由组内重试恢复，不能因此冷却成功组。
		_, streamFailed := service.GetOpsStreamError(current)
		failed := (w.failedResponse() || streamFailed) && smartRoutingUpstreamRetryable(current)
		canRetry := terminal != nil && len(guards) > 0 && attempts < 9 &&
			!w.committed && state.attempt.CanReplay() && current.Request.Context().Err() == nil &&
			state.replayBlockReason == "" && failed
		// 同一 Key 的失败组进入冷却；0 只关闭跨请求冷却，不影响当前请求换组。
		if cache, ok := resolver.(smartRoutingCooldownResolver); ok && failed && state.groupID > 0 && state.cooldown > 0 {
			// 已确认的上游故障不能因客户端取消而丢失冷却；独立写入仍受短超时限制。
			cooldownCtx, cancelCooldown := context.WithTimeout(context.WithoutCancel(current.Request.Context()), 250*time.Millisecond)
			err := cache.CooldownSmartRoutingGroup(cooldownCtx, state.keyID, state.groupID, state.cooldown)
			cancelCooldown()
			if err != nil {
				slog.Warn("smart routing cooldown write failed", "key_id", state.keyID, "group_id", state.groupID, "error", err)
			}
		}
		if failed {
			// 仅记录路由决策元数据，避免历史正文或密钥进入排障日志。
			slog.Info("smart routing upstream attempt failed", "request_id", current.Request.Context().Value(ctxkey.RequestID),
				"key_id", state.keyID, "group_id", state.groupID, "upstream_status", smartRoutingUpstreamStatus(current),
				"cooldown_seconds", int(state.cooldown.Seconds()), "can_try_next_group", canRetry, "replay_block_reason", state.replayBlockReason,
				"attempt_block_reason", state.attempt.NonReplayableReason(), "response_committed", w.committed, "request_cancelled", current.Request.Context().Err() != nil)
		}
		if !canRetry {
			finishSmartRoutingAttempt(current, state.attempt, false)
			copySmartRoutingOutcome(c, current, history)
			// 只有最终成功且响应写入正常，才将完整失败历史关联到实际恢复分组。
			// HTTP 200 的失败流、取消请求和写入失败不能被标成已恢复。
			if err := w.commit(); err == nil && w.status >= 200 && w.status < 300 &&
				!w.failedResponse() && !streamFailed && current.Request.Context().Err() == nil &&
				!service.HasOpsClientBusinessLimited(current) && service.GetOpsCyberPolicy(current) == nil {
				if selected, ok := GetAPIKeyFromContext(current); ok && selected != nil {
					service.MarkOpsRecoveredGroup(c, selected.Group)
				}
			}
			return
		}
		state.visited[state.groupID] = true
		if !finishSmartRoutingAttempt(current, state.attempt, true) {
			copySmartRoutingOutcome(c, current, history)
			w.commit()
			return
		}
		lastContext, lastWriter := current, w
		lastGroupID := state.groupID
		lastEvents := smartRoutingUpstreamEvents(current)
		// Copy 保留真实 router 的客户端 IP/参数设置，并使 Next 不再执行旧链。
		// 鉴权成功由它实际写入的 Key 上下文证明，不能用 Copy 的 IsAborted 判定。
		next := state.base.Copy()
		next.Request = state.request.Clone(state.request.Context())
		setRequestBody(next.Request, state.body)
		next.Writer = newSmartRoutingAttemptWriter(state.parent)
		service.ResetGatewayStreamOutputAccounting(next)
		state.writer = next.Writer.(*smartRoutingAttemptWriter)
		state.writer.ctx = next
		state.groupID = 0
		next.Set(smartRoutingResolverContextKey, resolver)
		next.Set(smartRoutingExecutionKey, state)
		authenticate(next)
		selected, authenticated := GetAPIKeyFromContext(next)
		if !authenticated || selected == nil || selected.Group == nil {
			// 无剩余候选不能覆盖最后一次真实上游错误；本地权限/计费错误仍须返回。
			if state.selectionExhausted {
				copySmartRoutingOutcome(c, lastContext, history)
				lastWriter.commit()
				return
			}
			copySmartRoutingOutcome(c, next, append(history, lastEvents...))
			state.writer.commit()
			return
		}
		// 在途编辑为普通/复合 Key 或替换身份时，不能把旧智能请求转发到新模式的默认组。
		if !selected.SmartRouting || selected.ID != state.keyID {
			copySmartRoutingOutcome(c, lastContext, history)
			lastWriter.commit()
			return
		}
		history = append(history, lastEvents...)
		allowed := true
		for _, guard := range guards {
			if !guard(next) {
				allowed = false
				break
			}
		}
		if allowed {
			slog.Info("smart routing group switched", "request_id", next.Request.Context().Value(ctxkey.RequestID),
				"key_id", state.keyID, "from_group_id", lastGroupID, "to_group_id", state.groupID)
			terminal(next)
		}
		current = next
	}
}

// prepareSmartRoutingExecution 在 Key 重定向和分组上下文写入之前保留客户端请求。
func prepareSmartRoutingExecution(c *gin.Context, key *service.APIKey) *smartRoutingExecution {
	value, _ := c.Get(smartRoutingExecutionKey)
	state, _ := value.(*smartRoutingExecution)
	if state == nil || !smartRoutingReplayEndpoint(c) {
		return nil
	}
	body, err := readAndRestoreRequestBody(c.Request)
	if err != nil {
		return nil
	}
	if !state.active {
		state.active = true
		state.keyID = key.ID
		// 不可重放只禁止本次换组，不能同时关闭失败组的冷却观测。
		state.replayBlockReason = smartRoutingReplayBlockReason(body)
		// 各轮重新检查资格，但同一客户端请求的全局 RPM 只能递增一次。
		requestCtx := service.WithSmartRoutingRPMAdmission(state.base.Request.Context())
		state.request = c.Request.Clone(requestCtx)
		c.Request = c.Request.WithContext(requestCtx)
		state.body = bytes.Clone(body)
		state.writer = newSmartRoutingAttemptWriter(state.parent)
		state.writer.ctx = c
		c.Writer = state.writer
		service.ResetGatewayStreamOutputAccounting(c)
	}
	state.selectionExhausted = false
	state.cooldown = time.Duration(key.SmartRoutingCooldownSeconds) * time.Second
	ctx, attempt := service.WithSmartRoutingAttempt(c.Request.Context())
	state.attempt = attempt
	c.Request = c.Request.WithContext(ctx)
	return state
}

func finishSmartRoutingAttempt(c *gin.Context, state *service.SmartRoutingAttemptState, replay bool) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 2*time.Second)
	defer cancel()
	if err := service.FinishSmartRoutingAttempt(ctx, state, replay); err != nil {
		slog.Warn("smart routing session claim cleanup failed", "error", err)
		return false
	}
	return true
}

func smartRoutingReplayEndpoint(c *gin.Context) bool {
	if c.Request.Method != http.MethodPost {
		return false
	}
	path := strings.TrimSuffix(c.Request.URL.Path, "/")
	if isGeminiNativeModelEndpoint(path) {
		return strings.HasSuffix(path, ":generateContent") || strings.HasSuffix(path, ":streamGenerateContent") || strings.HasSuffix(path, ":countTokens")
	}
	for _, prefix := range []string{"/antigravity", "/backend-api/codex", "/v1"} {
		path = strings.TrimPrefix(path, prefix)
	}
	switch path {
	case "/messages", "/messages/count_tokens", "/responses", "/responses/compact", "/responses/input_tokens", "/chat/completions", "/embeddings", "/systemone":
		return true
	}
	return false
}

// smartRoutingReplayBlockReason 只识别请求协议字段，不递归误判用户参数或工具 schema。
func smartRoutingReplayBlockReason(body []byte) string {
	if gjson.GetBytes(body, "previous_response_id").String() != "" {
		return "previous_response_id"
	}
	// 普通 reasoning 密文随原始历史保留；不透明压缩项可能是唯一上下文，仍须限制跨组。
	for _, item := range gjson.GetBytes(body, "input").Array() {
		kind := item.Get("type").String()
		if (kind == "compaction" || kind == "compaction_summary") && item.Get("encrypted_content").String() != "" {
			return "opaque_compaction_history"
		}
	}
	if gjson.GetBytes(body, "background").Bool() {
		return "background_task"
	}
	if service.IsExplicitImageGenerationIntent("/responses", "", body) {
		return "image_generation"
	}
	// 服务端执行工具可能已经产生外部副作用；没有输出不代表可以再次执行。
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		kind := tool.Get("type").String()
		switch kind {
		case "mcp", "shell", "code_execution", "code_interpreter", "computer", "computer_use_preview":
			return "server_tool_" + kind
		}
	}
	return ""
}

// continuationRequiresSameCandidate 禁止借冷却跳过可能持有远端上下文的候选。
func (s *smartRoutingExecution) continuationRequiresSameCandidate() bool {
	return s.replayBlockReason == "previous_response_id" || s.replayBlockReason == "opaque_compaction_history"
}

func smartRoutingUpstreamRetryable(c *gin.Context) bool {
	if service.HasOpsClientBusinessLimited(c) || service.GetOpsCyberPolicy(c) != nil {
		return false
	}
	// 内容策略等请求级终态不能借用之前组内重试留下的上游状态换组。
	if streamErr, ok := service.GetOpsStreamError(c); ok && streamErr.RequestScoped {
		return false
	}
	event := smartRoutingLastUpstreamEvent(c)
	// 最新的无状态网络失败优先于旧账号的 HTTP 状态，取消不能借历史 5xx 触发冷却。
	if smartRoutingTransportFailure(event) {
		return c.Request.Context().Err() == nil
	}
	status := smartRoutingUpstreamStatus(c)
	if status == http.StatusTooManyRequests || status >= 500 && status <= 599 {
		return true
	}
	// 一些本地参数校验也设置上游状态字段；新增的 4xx 必须有真实上游事件证明。
	// 只查看本轮最后一条事件，避免旧故障使后续本地错误被错误重放或冷却。
	if event == nil {
		return false
	}
	if status >= 400 && status <= 499 && event.UpstreamStatusCode == status {
		return event.Stage != string(service.GatewayFailureStageAccountAuth)
	}
	return false
}

func smartRoutingUpstreamStatus(c *gin.Context) int {
	if smartRoutingTransportFailure(smartRoutingLastUpstreamEvent(c)) {
		return 0
	}
	status := c.GetInt(service.OpsUpstreamStatusCodeKey)
	if status == 0 {
		if event := smartRoutingLastUpstreamEvent(c); event != nil {
			status = event.UpstreamStatusCode
		}
	}
	return status
}

// smartRoutingTransportFailure 不将凭据、调度等本地阶段的无状态事件误当成上游网络失败。
func smartRoutingTransportFailure(event *service.OpsUpstreamErrorEvent) bool {
	return event != nil && event.UpstreamStatusCode == 0 && event.Stage != string(service.GatewayFailureStageAccountAuth) &&
		(event.Kind == "request_error" || event.Kind == "signature_retry_tools_request_error")
}

func smartRoutingLastUpstreamEvent(c *gin.Context) *service.OpsUpstreamErrorEvent {
	events := smartRoutingUpstreamEvents(c)
	for i := len(events) - 1; i >= 0; i-- {
		if events[i] != nil {
			return events[i]
		}
	}
	return nil
}

func smartRoutingUpstreamEvents(c *gin.Context) []*service.OpsUpstreamErrorEvent {
	value, _ := c.Get(service.OpsUpstreamErrorsKey)
	events, _ := value.([]*service.OpsUpstreamErrorEvent)
	return events
}

func copySmartRoutingOutcome(dst, src *gin.Context, history []*service.OpsUpstreamErrorEvent) {
	events := append(append([]*service.OpsUpstreamErrorEvent(nil), history...), smartRoutingUpstreamEvents(src)...)
	if dst != src {
		dst.Keys = src.Copy().Keys
		dst.Request = src.Request
		dst.Params = append(gin.Params(nil), src.Params...)
		dst.Errors = src.Errors
	}
	if len(events) > 0 {
		dst.Set(service.OpsUpstreamErrorsKey, events)
	}
}

const smartRoutingPendingLimit = 256 * 1024

// smartRoutingAttemptWriter 只缓冲有限错误和 SSE 前导帧，不缓存完整成功响应。
type smartRoutingAttemptWriter struct {
	gin.ResponseWriter
	ctx        *gin.Context
	header     http.Header
	status     int
	size       int
	committed  bool
	failure    bool
	pending    bytes.Buffer
	ssePartial []byte
	sseDiscard bool
	writeErr   error
}

func newSmartRoutingAttemptWriter(parent gin.ResponseWriter) *smartRoutingAttemptWriter {
	return &smartRoutingAttemptWriter{ResponseWriter: parent, header: parent.Header().Clone(), status: http.StatusOK, size: -1}
}

func (w *smartRoutingAttemptWriter) Header() http.Header { return w.header }
func (w *smartRoutingAttemptWriter) Status() int         { return w.status }
func (w *smartRoutingAttemptWriter) Size() int           { return w.size }
func (w *smartRoutingAttemptWriter) Written() bool       { return w.size >= 0 }
func (w *smartRoutingAttemptWriter) WriteHeader(code int) {
	if !w.Written() && code > 0 {
		w.status = code
	}
}
func (w *smartRoutingAttemptWriter) WriteHeaderNow() {
	if w.size < 0 {
		w.size = 0
	}
}
func (w *smartRoutingAttemptWriter) WriteString(body string) (int, error) {
	return w.Write([]byte(body))
}
func (w *smartRoutingAttemptWriter) Write(body []byte) (int, error) {
	w.WriteHeaderNow()
	w.size += len(body)
	isSSE := strings.Contains(w.header.Get("Content-Type"), "text/event-stream")
	business := false
	if isSSE {
		// 流已提交后仍观测真实错误终态，仅用于冷却，绝不重新缓冲或跨组拼接。
		business = w.observeSSE(body)
	}
	if w.committed {
		return w.writeCommitted(body)
	}
	if len(body) > smartRoutingPendingLimit-w.pending.Len() {
		// 超过上限直接放行，避免一次大 Write 先复制完整响应再检查容量。
		if err := w.commit(); err != nil {
			return 0, err
		}
		return w.writeCommitted(body)
	}
	_, _ = w.pending.Write(body)
	if w.status >= 400 {
		return len(body), nil
	}
	if !isSSE {
		// Responses 成功体可包含 error:null，不能借历史故障把它当成新的失败。
		errorValue := gjson.GetBytes(w.pending.Bytes(), "error")
		if w.ctx != nil && errorValue.Exists() && errorValue.Type != gjson.Null && smartRoutingUpstreamRetryable(w.ctx) {
			w.failure = true
			return len(body), nil
		}
		return len(body), w.commit()
	}
	if business {
		return len(body), w.commit()
	}
	return len(body), nil
}

func (w *smartRoutingAttemptWriter) failedResponse() bool { return w.status >= 400 || w.failure }

// writeCommitted 保留流放行后的写入错误，避免客户端断流仍被记录为恢复成功。
func (w *smartRoutingAttemptWriter) writeCommitted(body []byte) (int, error) {
	n, err := w.ResponseWriter.Write(body)
	if err == nil && n < len(body) {
		err = io.ErrShortWrite
	}
	if err != nil && w.writeErr == nil {
		w.writeErr = err
	}
	return n, err
}

// observeSSE 以有限内存扫描完整帧，保留跨 Write 的分隔符；超长业务帧放行后继续观察后续终态。
func (w *smartRoutingAttemptWriter) observeSSE(body []byte) (business bool) {
	for len(body) > 0 {
		space := smartRoutingPendingLimit - len(w.ssePartial)
		if space == 0 {
			business, w.sseDiscard = true, true
			// 保留分隔符的最长不完整后缀，避免超长帧边界刚好跨 Write 时失步。
			w.ssePartial = append(w.ssePartial[:0], w.ssePartial[len(w.ssePartial)-3:]...)
			space = smartRoutingPendingLimit - len(w.ssePartial)
		}
		take := min(space, len(body))
		w.ssePartial = append(w.ssePartial, body[:take]...)
		body = body[take:]
		consumed := 0
		for {
			unread := w.ssePartial[consumed:]
			end, delimiter := bytes.Index(unread, []byte("\n\n")), 2
			if crlf := bytes.Index(unread, []byte("\r\n\r\n")); crlf >= 0 && (end < 0 || crlf < end) {
				end, delimiter = crlf, 4
			}
			if end < 0 {
				break
			}
			if w.sseDiscard {
				w.sseDiscard = false
			} else {
				prelude, failure := smartRoutingSSEPrelude(unread[:end])
				business = business || !prelude
				w.failure = w.failure || failure
			}
			consumed += end + delimiter
		}
		if consumed > 0 {
			// 一批帧只搬移一次尾部，避免大量小帧造成平方级复制。
			w.ssePartial = append(w.ssePartial[:0], w.ssePartial[consumed:]...)
		}
	}
	return business
}

func smartRoutingSSEPrelude(frame []byte) (prelude, failure bool) {
	event := ""
	var data []byte
	for _, line := range bytes.Split(frame, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("event:")) {
			event = strings.TrimSpace(string(line[6:]))
		} else if bytes.HasPrefix(line, []byte("data:")) {
			data = append(data, bytes.TrimSpace(line[5:])...)
			data = append(data, '\n')
		}
	}
	if len(bytes.TrimSpace(data)) == 0 && event == "" {
		return true, false
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		// 错误之后的结束哨兵不提交已缓冲失败；正常空流由请求收尾统一提交。
		return true, false
	}
	payload := gjson.ParseBytes(data)
	if event == "" {
		event = payload.Get("type").String()
	}
	switch event {
	case "ping", "response.created", "response.in_progress":
		return true, false
	case "error", "response.failed":
		return true, true
	case "message_start":
		// message_start 只在内容为空时是前导，避免供应商将业务内容内嵌时被重放。
		content := payload.Get("message.content")
		return content.IsArray() && len(content.Array()) == 0, false
	case "content_block_start":
		block := payload.Get("content_block")
		kind := block.Get("type").String()
		return kind == "text" && block.Get("text").String() == "" || kind == "thinking" && block.Get("thinking").String() == "" && block.Get("signature").String() == "", false
	}
	if value := payload.Get("error"); value.Exists() && value.Type != gjson.Null {
		return true, true
	}
	if smartRoutingChatRolePrelude(payload) {
		return true, false
	}
	return false, false
}

// Chat 首帧只有 assistant 角色或空文本时尚无业务输出，工具调用和结束原因始终按输出处理。
func smartRoutingChatRolePrelude(payload gjson.Result) bool {
	choices := payload.Get("choices")
	if !choices.IsArray() || len(choices.Array()) == 0 {
		return false
	}
	for _, choice := range choices.Array() {
		if reason := choice.Get("finish_reason"); reason.Exists() && reason.Type != gjson.Null {
			return false
		}
		delta := choice.Get("delta")
		if !delta.IsObject() {
			return false
		}
		empty := true
		delta.ForEach(func(key, value gjson.Result) bool {
			switch key.String() {
			case "role":
				empty = value.String() == "assistant" || value.String() == ""
			case "content", "reasoning_content", "refusal":
				empty = value.Type == gjson.Null || value.Type == gjson.String && value.String() == ""
			default:
				empty = false
			}
			return empty
		})
		if !empty {
			return false
		}
	}
	return true
}

func (w *smartRoutingAttemptWriter) Flush() {
	w.WriteHeaderNow()
	if w.committed {
		w.ResponseWriter.Flush()
	}
}

func (w *smartRoutingAttemptWriter) commit() error {
	if w.committed {
		return w.writeErr
	}
	w.committed = true
	dst := w.ResponseWriter.Header()
	for key := range dst {
		delete(dst, key)
	}
	for key, values := range w.header {
		dst[key] = append([]string(nil), values...)
	}
	w.ResponseWriter.WriteHeader(w.status)
	if w.pending.Len() > 0 {
		_, w.writeErr = io.Copy(w.ResponseWriter, &w.pending)
	}
	return w.writeErr
}
