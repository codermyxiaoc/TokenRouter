package handler

import (
	"context"
	"log/slog"
	"time"

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
	dst = service.PropagateCreativeTextUsageSink(dst, src)
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
		base = service.PropagateCreativeTextUsageSink(base, requestCtx)
		task(base)
	}
}

// 画布文本先保存响应和账务证据再结算，不能交给可能丢弃任务的普通异步记录池。
// 仅服务端创建的 sink 启用此路径，普通 HTTP 请求保持原调度方式。
func runCreativeTextUsageRecordTask(c *gin.Context, task service.UsageRecordTask) (handled bool) {
	if task == nil || c == nil || c.Request == nil || !service.HasCreativeTextUsageSink(c.Request.Context()) {
		return false
	}
	handled = true
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 10*time.Second)
	defer cancel()
	defer func() {
		if recover() != nil {
			// 缺少成功回执时画布保持待核对，不能再次调用模型；日志不写入原始异常载荷。
			slog.Error("creative text usage record task panicked")
		}
	}()
	wrapUsageRecordTaskContext(c, task)(ctx)
	return handled
}
