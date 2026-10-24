package handler

import (
	"context"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// 图片总预算耗尽不等同于客户端主动离开；只有尚未发送响应时才能补写 504。
func finishImageAttemptRequest(c *gin.Context, onDeadline func()) {
	if c == nil || c.Request == nil {
		return
	}
	ctx := c.Request.Context()
	if service.ImageAttemptHasPolicy(ctx) && errors.Is(ctx.Err(), context.DeadlineExceeded) && !c.Writer.Written() {
		onDeadline()
	}
	service.CancelImageAttemptPolicy(ctx)
}
