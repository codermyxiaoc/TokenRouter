package handler

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

func usageRecordContextFromGin(c *gin.Context) context.Context {
	dst := context.Background()
	if c == nil || c.Request == nil {
		return dst
	}
	src := c.Request.Context()
	for _, key := range []any{
		ctxkey.RequestID,
		ctxkey.ClientRequestID,
		ctxkey.ClientModel,
	} {
		if value := src.Value(key); value != nil {
			dst = context.WithValue(dst, key, value)
		}
	}
	dst = service.PropagateAPIKeyModelRedirectTrace(dst, src)
	return dst
}

func wrapUsageRecordTaskContext(c *gin.Context, task func(context.Context)) func(context.Context) {
	if task == nil {
		return nil
	}
	// 已准备结算（含中断流的部分用量）时，跨组重放可能重复执行且碰撞同一账单幂等键。
	if c != nil && c.Request != nil {
		service.MarkSmartRoutingAttemptNonReplayable(c.Request.Context(), "usage_record_scheduled")
	}
	requestCtx := usageRecordContextFromGin(c)
	return func(workerCtx context.Context) {
		base := workerCtx
		if base == nil {
			base = context.Background()
		}
		for _, key := range []any{
			ctxkey.RequestID,
			ctxkey.ClientRequestID,
			ctxkey.ClientModel,
		} {
			if value := requestCtx.Value(key); value != nil {
				base = context.WithValue(base, key, value)
			}
		}
		base = service.PropagateAPIKeyModelRedirectTrace(base, requestCtx)
		task(base)
	}
}
