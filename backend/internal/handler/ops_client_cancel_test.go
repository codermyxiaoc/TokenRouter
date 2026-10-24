package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 取消本身不伪造服务错误；已观测到的上游故障或明确失败仍然保留。
func TestOpsErrorLogger_PureClientCancellationDoesNotCreateFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name        string
		cancel      bool
		status      int
		upstream    bool
		streamError bool
		want        int64
	}{
		{name: "pure cancellation", cancel: true, status: statusClientClosedRequest},
		{name: "499 without cancellation", status: statusClientClosedRequest, want: 1},
		{name: "earlier upstream failure", cancel: true, status: statusClientClosedRequest, upstream: true, want: 1},
		{name: "observed stream failure", cancel: true, status: statusClientClosedRequest, streamError: true, want: 1},
		{name: "other failure with cancellation", cancel: true, status: http.StatusBadGateway, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 4)
			ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.POST("/v1/messages", func(c *gin.Context) {
				if tc.cancel {
					cancel()
				}
				if tc.upstream {
					service.SetOpsUpstreamError(c, http.StatusBadGateway, "upstream failure", "")
				}
				if tc.streamError {
					service.MarkOpsStreamError(c, "upstream_error", "upstream failure", http.StatusBadGateway)
				}
				c.Status(tc.status)
			})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx))
			require.Equal(t, tc.status, rec.Code)
			require.Equal(t, tc.want, OpsErrorLogQueueLength())
		})
	}
}
