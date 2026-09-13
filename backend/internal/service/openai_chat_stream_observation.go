package service

import (
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// 智能路由的 Chat 桥按 attempt 记录安全终态元数据，避免仅凭 HTTP 200 判断生成成功。
// 不记录模型正文、工具参数、上游错误正文或凭据。
// @project-doc docs/interfaces/openai_upstream.md#chat_stream_failure
func logSmartRoutingChatStreamOutcome(c *gin.Context, account *Account, scan ccStreamScanState, clientDisconnected bool) {
	if c == nil || c.Request == nil || smartRoutingAttemptFromContext(c.Request.Context()) == nil {
		return
	}
	terminal := "missing"
	switch {
	case len(scan.FailurePayload) > 0:
		terminal = "upstream_error"
	case scan.Err != nil:
		terminal = "read_error"
		if c.Request.Context().Err() != nil {
			terminal = "cancelled"
		}
	case scan.SawFinish:
		terminal = "finish_reason"
	case scan.SawDone:
		terminal = "done"
	}
	// 当前流正常完成时，不能把同组之前已恢复账号的错误状态挂到本次终态上。
	upstreamStatus := 0
	if scan.Err != nil && (len(scan.FailurePayload) > 0 || c.Request.Context().Err() == nil) {
		upstreamStatus = c.GetInt(OpsUpstreamStatusCodeKey)
	}
	fields := []zap.Field{
		zap.Int64("group_id", getOpenAIGroupIDFromContext(c)),
		zap.String("stream_terminal", terminal),
		zap.String("finish_reason", scan.FinishReason),
		zap.Bool("saw_output", scan.SawOutput),
		zap.Bool("saw_usage", scan.SawUsage),
		zap.Bool("saw_finish", scan.SawFinish),
		zap.Bool("saw_done", scan.SawDone),
		zap.Bool("client_disconnected", clientDisconnected),
		zap.Bool("request_cancelled", c.Request.Context().Err() != nil),
		zap.Int("upstream_status", upstreamStatus),
	}
	if account != nil {
		fields = append(fields, zap.Int64("account_id", account.ID))
	}
	logger.FromContext(c.Request.Context()).Info("smart routing chat stream finished", fields...)
}
