package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

type creativeTextUsageContextKey struct{}

// 捕获器仅由内部画布任务创建，HTTP 头或请求体无法伪造此标记。
type creativeTextUsageCapture struct {
	execution      *CreativeCanvasExecution
	repo           CreativeTextRepository
	mu             sync.Mutex
	responseSource func() (int, []byte)
	requests       atomic.Int32
}

func HasCreativeTextUsageSink(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Value(creativeTextUsageContextKey{}).(*creativeTextUsageCapture)
	return ok
}
func PropagateCreativeTextUsageSink(dst, src context.Context) context.Context {
	if dst == nil {
		dst = context.Background()
	}
	if src == nil {
		return dst
	}
	if value := src.Value(creativeTextUsageContextKey{}); value != nil {
		return context.WithValue(dst, creativeTextUsageContextKey{}, value)
	}
	return dst
}
func BindCreativeTextResponseSource(ctx context.Context, source func() (int, []byte)) {
	if sink, ok := ctx.Value(creativeTextUsageContextKey{}).(*creativeTextUsageCapture); ok {
		sink.mu.Lock()
		sink.responseSource = source
		sink.mu.Unlock()
	}
}

// ClaimCreativeTextUpstreamRequest 在真实文本生成传输边界限制一次发送，内部或跨账号重试也不能重复创建。
func ClaimCreativeTextUpstreamRequest(req *http.Request) error {
	if req == nil || req.URL == nil || req.Method != http.MethodPost {
		return nil
	}
	sink, ok := req.Context().Value(creativeTextUsageContextKey{}).(*creativeTextUsageCapture)
	if !ok {
		return nil
	}
	path := strings.ToLower(req.URL.Path)
	// 只计实际推理端点：令牌刷新、loadCodeAssist、输入 token 计数及账号元数据不占用发送机会。
	if !(strings.HasSuffix(path, "/responses") || strings.HasSuffix(path, "/messages") || strings.HasSuffix(path, "/chat/completions") || strings.HasSuffix(path, ":generatecontent") || strings.HasSuffix(path, ":streamgeneratecontent") || strings.HasSuffix(path, "/agent_chat_generation") || strings.HasSuffix(path, "/invoke") || strings.HasSuffix(path, "/invoke-with-response-stream") || strings.HasSuffix(path, ":rawpredict") || strings.HasSuffix(path, ":streamrawpredict")) {
		return nil
	}
	if sink.requests.Add(1) > 1 {
		return errors.New("creative text generation must not be resubmitted")
	}
	return nil
}

func prepareCreativeTextUsage(ctx context.Context, cmd *UsageBillingCommand, usage *UsageLog) error {
	sink, ok := ctx.Value(creativeTextUsageContextKey{}).(*creativeTextUsageCapture)
	if !ok {
		return nil
	}
	e := sink.execution
	if cmd == nil || usage == nil || cmd.UserID != e.UserID || cmd.ActorUserID != e.UserID || cmd.GroupID == nil || *cmd.GroupID != e.Params.GroupID || cmd.TeamID != nil || cmd.ImageCount != 0 || cmd.LegacyVideoTaskID != 0 {
		return ErrCanvasConflict
	}
	cmd.CreativeTextExecutionID = e.ID
	cmd.RequestID = "creative_text:" + e.ID
	cmd.MediaType = "text"
	cmd.RequestFingerprint = ""
	cmd.Normalize()
	usage.RequestID = cmd.RequestID
	clean := *usage
	clean.User, clean.APIKey, clean.Account, clean.Group, clean.Subscription = nil, nil, nil, nil, nil
	clean.BillingSubscriptions = nil
	clean.VideoBilling = nil
	clean.UserAgent, clean.IPAddress, clean.SessionID = nil, nil, nil
	sink.mu.Lock()
	source := sink.responseSource
	sink.mu.Unlock()
	if source == nil {
		return errors.New("creative text response source unavailable")
	}
	status, body := source()
	text := creativeTextResponseContent(body)
	return sink.repo.SaveTextBillingEvidence(ctx, e.UserID, e.ID, &CreativeTextBillingEvidence{Version: 1, Command: *cmd, Usage: clean}, status, text)
}

// 只读取 Chat Completions 的公开正文，不执行 Markdown/HTML，也不持久化完整上游响应。
func creativeTextResponseContent(body []byte) string {
	var result struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
				Refusal string          `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
	}
	if len(body) > CreativeTextOutputLimit || json.Unmarshal(body, &result) != nil || len(result.Choices) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(result.Choices[0].Message.Content, &text) == nil && strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(result.Choices[0].Message.Content, &parts) == nil {
		var out strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				out.WriteString(p.Text)
			}
		}
		if value := strings.TrimSpace(out.String()); value != "" {
			return value
		}
	}
	return strings.TrimSpace(result.Choices[0].Message.Refusal)
}
