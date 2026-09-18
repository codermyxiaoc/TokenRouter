package service

import (
	"context"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// 已确认的显式流失败须落入 Ops；正常连接与断连排水均由流函数退出时调用一次。
// 仅补充失败归属和流失败标记，不重新执行账号健康副作用或用量结算。
// @project-doc docs/operations/ops_monitoring_and_alerting.md#ops_signal_pipeline
func (s *OpenAIGatewayService) recordOpenAIStreamDrainFailure(c *gin.Context, account *Account, passthrough bool, requestID string, payload []byte, message string) {
	if len(payload) == 0 {
		return
	}
	// 专门的策略拒绝已有独立标记与结算语义，不能覆盖成服务器故障。
	if hit, _, _ := detectOpenAICyberPolicy(payload); hit {
		return
	}
	message = s.recordOpenAIStreamUpstreamError(c, account, passthrough, requestID, "stream_error", payload, message)
	status := openAIStreamFailureStatus(payload, message)
	MarkOpsStreamFailure(c, "upstream_error", "", message, status)
}

// 仅记录关联上下文、写入阶段和错误类别，避免完整网络错误暴露内部地址或凭据。
func logOpenAIStreamClientDisconnect(ctx context.Context, account *Account, stage string, err error) {
	fields := []zap.Field{
		zap.String("stage", stage),
		zap.String("error_type", fmt.Sprintf("%T", err)),
		zap.Bool("request_cancelled", ctx.Err() != nil),
	}
	if account != nil {
		fields = append(fields, zap.Int64("account_id", account.ID))
	}
	logger.FromContext(ctx).Info("openai downstream disconnected; draining upstream", fields...)
}
