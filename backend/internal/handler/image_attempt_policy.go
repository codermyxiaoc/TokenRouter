package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// 图片故障的当前请求恢复与后续健康冷却分离，未知受理绝不因未预占资金而绕过保护。
func (h *OpenAIGatewayHandler) retryImageOnDifferentAccount(c *gin.Context, ctx context.Context, account *service.Account, finalModel, operation string, err error, writerBefore int) bool {
	decision, ok := service.ImageAttemptDecisionForError(err)
	if !ok {
		decision, ok = service.ImageAttemptLastDecision(ctx)
	}
	if !ok {
		decision = service.ImageAttemptDecision{Outcome: service.ImageAttemptNotSent, Reason: "image_local_validation_failed"}
	}
	if decision.AttemptFault {
		h.gatewayService.ReportOpenAIAccountScheduleResult(account, finalModel, false, nil, err)
		// 缓存包装自己拥有短预算；客户端离开不能撤销已经确认的健康信息。按故障类型与连续次数分级冷却。
		_ = h.gatewayService.RecordImageModelFailure(context.WithoutCancel(ctx), account.ID, finalModel, operation, decision)
	}
	if imageAttemptDeadlineExceeded(c, ctx) || failoverClientGone(c) {
		return false
	}
	if service.ImageAttemptCanSwitch(decision) && service.ImageAttemptSendCount(ctx) < service.ImageAttemptMaxSends &&
		ctx.Err() == nil && !service.IsResponseCommitted(c) && service.OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c) == writerBefore {
		return true
	}
	if service.IsResponseCommitted(c) || service.OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c) != writerBefore {
		return false
	}
	if decision.Outcome == service.ImageAttemptUnknown || decision.Outcome == service.ImageAttemptAccepted || decision.Outcome == service.ImageAttemptResultReady {
		h.errorResponse(c, http.StatusBadGateway, "image_result_unconfirmed", "Image generation result is unconfirmed; do not automatically resubmit")
		return false
	}
	var failover *service.UpstreamFailoverError
	if errors.As(err, &failover) {
		h.handleFailoverExhausted(c, failover, false)
		return false
	}
	h.ensureOpenAIForwardErrorResponse(c, false, err)
	return false
}

// 总预算耗尽属于网关超时，不能误报为客户端取消，也不能重发仍可能执行中的生成。
func imageAttemptDeadlineExceeded(c *gin.Context, ctx context.Context) bool {
	if !service.ImageAttemptHasPolicy(ctx) || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return false
	}
	if !service.IsResponseCommitted(c) && service.OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c) < 0 {
		last, _ := service.ImageAttemptLastDecision(ctx)
		hasSent := service.ImageAttemptSendCount(ctx) > 0 && last.Outcome != service.ImageAttemptNotSent && last.Outcome != service.ImageAttemptRejected
		message := "Image generation deadline exceeded"
		if hasSent {
			message = "Image generation deadline exceeded; result is unconfirmed, do not automatically resubmit"
		}
		service.StopOpenAIImagesJSONKeepaliveCommitted(c)
		c.JSON(http.StatusGatewayTimeout, gin.H{"error": gin.H{"type": "image_execution_timeout", "message": message}})
	}
	return true
}
