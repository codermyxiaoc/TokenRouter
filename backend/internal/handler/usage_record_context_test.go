//go:build unit

package handler

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 排队结算即终止重放资格，不能等异步任务执行后才保护原请求。
func TestWrapUsageRecordTaskPreventsSmartRoutingReplay(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx, state := service.WithSmartRoutingAttempt(context.Background())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
	ran := false
	task := wrapUsageRecordTaskContext(c, func(context.Context) { ran = true })
	require.False(t, state.CanReplay())
	require.False(t, ran)
	task(context.Background())
	require.True(t, ran)
}
