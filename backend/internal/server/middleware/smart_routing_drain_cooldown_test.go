//go:build unit

package middleware

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 排水期间客户端可能先取消；冷却写入必须有独立期限，不能复用已经取消的请求上下文。
type drainCooldownCall struct {
	key, group  int64
	duration    time.Duration
	contextErr  error
	hasDeadline bool
	timeLeft    time.Duration
}

type drainCooldownResolver struct {
	failoverResolver
	calls []drainCooldownCall
}

func (r *drainCooldownResolver) CooldownSmartRoutingGroup(ctx context.Context, key, group int64, duration time.Duration) error {
	deadline, hasDeadline := ctx.Deadline()
	r.calls = append(r.calls, drainCooldownCall{
		key: key, group: group, duration: duration, contextErr: ctx.Err(),
		hasDeadline: hasDeadline, timeLeft: time.Until(deadline),
	})
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.failoverResolver.CooldownSmartRoutingGroup(ctx, key, group, duration)
}

// 使用真实鉴权和智能路由协调器，只替换候选可用性与冷却存储，避免请求真实上游。
func newDrainCooldownRouter(t *testing.T, key *service.APIKey, resolver *drainCooldownResolver, handler gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	resolver.cooling = make(map[[2]int64]time.Time)
	resolver.now = time.Now()
	repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	keyService := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	auth := gin.HandlerFunc(NewAPIKeyAuthMiddleware(keyService, nil, cfg))
	router := gin.New()
	router.Use(WithSmartRoutingResolver(resolver, auth, func(*gin.Context) bool { return true }))
	router.POST("/v1/responses", handler)
	return router
}

func TestSmartRoutingDrainFailureCoolsAfterClientCancellation(t *testing.T) {
	for _, test := range []struct {
		name string
		mark func(*gin.Context)
	}{
		{"stream_error", func(c *gin.Context) {
			service.MarkOpsStreamError(c, "upstream_error", "upstream stopped after partial output", http.StatusServiceUnavailable)
		}},
		{"stream_failure", func(c *gin.Context) {
			service.MarkOpsStreamFailure(c, "upstream_error", "upstream_service_unavailable", "upstream stopped after partial output", http.StatusServiceUnavailable)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := smartRoutingTestKey()
			key.SmartRoutingCooldownSeconds = 60
			resolver := &drainCooldownResolver{}
			var visits []int64
			router := newDrainCooldownRouter(t, key, resolver, func(c *gin.Context) {
				selected, ok := GetAPIKeyFromContext(c)
				require.True(t, ok)
				visits = append(visits, *selected.GroupID)
				if len(visits) > 1 {
					c.JSON(http.StatusOK, gin.H{"output": "next request succeeded"})
					return
				}
				c.Header("Content-Type", "text/event-stream")
				_, err := c.Writer.WriteString("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial-output\"}\n\n")
				require.NoError(t, err)
				c.Writer.Flush()
				ctx, cancel := context.WithCancel(c.Request.Context())
				cancel()
				c.Request = c.Request.WithContext(ctx)
				// 模拟断连后的上游排水：只留下失败事实，不再向已断开的客户端写失败帧。
				service.SetOpsUpstreamError(c, http.StatusServiceUnavailable, "upstream stopped after partial output", "")
				test.mark(c)
			})

			first := callFailoverRouter(router, "/v1/responses", `{"model":"target","stream":true}`)
			require.Equal(t, http.StatusOK, first.Code)
			require.Equal(t, []int64{1}, visits, "已输出且取消的请求禁止换组重放")
			require.Contains(t, first.Body.String(), "partial-output")
			require.NotContains(t, first.Body.String(), "response.failed")
			require.Len(t, resolver.calls, 1, "排水发现的真实上游503仍须冷却失败组")
			call := resolver.calls[0]
			require.NoError(t, call.contextErr, "冷却上下文必须脱离客户端取消信号")
			require.True(t, call.hasDeadline, "冷却上下文必须有期限")
			require.Positive(t, call.timeLeft)
			require.LessOrEqual(t, call.timeLeft, 5*time.Second, "冷却写入不能成为无界后台操作")
			require.Equal(t, key.ID, call.key)
			require.Equal(t, int64(1), call.group)
			require.Equal(t, 60*time.Second, call.duration)

			second := callFailoverRouter(router, "/v1/responses", `{"model":"target","stream":true}`)
			require.Equal(t, http.StatusOK, second.Code, second.Body.String())
			require.Equal(t, []int64{1, 2}, visits, "下一请求必须跳过仍在冷却的组1")
			require.Contains(t, second.Body.String(), "next request succeeded")
			require.Len(t, resolver.calls, 1)
		})
	}
}

func TestSmartRoutingDrainDoesNotCoolCancellationOrRecoveredHistory(t *testing.T) {
	for _, test := range []struct {
		name        string
		cancel      bool
		historyOnly bool
	}{
		{name: "client_cancel_only", cancel: true},
		{name: "recovered_upstream_503", historyOnly: true},
		{name: "recovered_upstream_503_then_client_cancel", cancel: true, historyOnly: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := smartRoutingTestKey()
			key.SmartRoutingCooldownSeconds = 60
			resolver := &drainCooldownResolver{}
			var visits []int64
			router := newDrainCooldownRouter(t, key, resolver, func(c *gin.Context) {
				selected, ok := GetAPIKeyFromContext(c)
				require.True(t, ok)
				visits = append(visits, *selected.GroupID)
				if test.historyOnly {
					// 同组早先账号失败、最终账号成功时，历史上游状态不能被当成本轮流失败。
					service.SetOpsUpstreamError(c, http.StatusServiceUnavailable, "earlier account failed", "")
					c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{AccountID: 91, UpstreamStatusCode: http.StatusServiceUnavailable}})
				}
				c.Header("Content-Type", "text/event-stream")
				_, err := c.Writer.WriteString("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"successful-output\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
				require.NoError(t, err)
				c.Writer.Flush()
				if test.cancel {
					ctx, cancel := context.WithCancel(c.Request.Context())
					cancel()
					c.Request = c.Request.WithContext(ctx)
				}
			})
			for i := 0; i < 2; i++ {
				response := callFailoverRouter(router, "/v1/responses", `{"model":"target","stream":true}`)
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			}
			require.Equal(t, []int64{1, 1}, visits)
			require.Empty(t, resolver.calls, "客户端取消或已恢复历史错误不能冷却成功组")
			require.Empty(t, resolver.cooling)
		})
	}
}
