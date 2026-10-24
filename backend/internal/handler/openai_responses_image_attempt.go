package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// Responses 图片生成不能进入文本池的原账号退避或多账号循环；拒绝证据与资金输出独立判断。
func (h *OpenAIGatewayHandler) retryResponsesImageOnDifferentAccount(c *gin.Context, account *service.Account, err error, writerBefore int, streamStarted bool) bool {
	ctx := c.Request.Context()
	decision, known := service.ImageAttemptDecisionForError(err)
	if !known {
		decision, known = service.ImageAttemptLastDecision(ctx)
	}
	if decision.AttemptFault {
		h.gatewayService.ReportOpenAIAccountScheduleResult(account, "", false, nil, err)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		// 图片预算结束不是客户端主动离开；即使已有心跳，也必须给出明确的超时终态。
		if !openAIForwardErrorAlreadyCommunicated(c, writerBefore, err) {
			h.handleStreamingAwareError(c, http.StatusGatewayTimeout, "image_execution_timeout", "Image generation deadline exceeded; result is unconfirmed, do not automatically resubmit", streamStarted || c.Writer.Written())
		}
		return false
	}
	if ctx.Err() != nil {
		failoverClientGone(c)
		return false
	}
	if known && service.ImageAttemptCanSwitch(decision) && service.ImageAttemptSendCount(ctx) < service.ImageAttemptMaxSends &&
		!service.IsResponseCommitted(c) && service.OpenAICompactKeepaliveAdjustedWrittenSize(c) == writerBefore {
		return true
	}
	if openAIForwardErrorAlreadyCommunicated(c, writerBefore, err) {
		return false
	}
	if decision.Outcome == service.ImageAttemptUnknown || decision.Outcome == service.ImageAttemptAccepted || decision.Outcome == service.ImageAttemptResultReady {
		h.handleStreamingAwareError(c, http.StatusBadGateway, "image_result_unconfirmed", "Image generation result is unconfirmed; do not automatically resubmit", streamStarted || c.Writer.Written())
		return false
	}
	var failover *service.UpstreamFailoverError
	if errors.As(err, &failover) {
		h.handleFailoverExhausted(c, failover, streamStarted || c.Writer.Written())
		return false
	}
	h.ensureOpenAIForwardErrorResponse(c, streamStarted, err)
	return false
}
