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

// imageSmartRouter 与 precisionSmartRouter 相同，但挂在生图端点上。
func imageSmartRouter(key *service.APIKey, resolver service.SmartRoutingGroupResolver, handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	keys := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	router := gin.New()
	router.Use(WithSmartRoutingResolver(resolver, gin.HandlerFunc(NewAPIKeyAuthMiddleware(keys, nil, cfg)), func(*gin.Context) bool { return true }))
	router.POST("/v1/images/generations", handler)
	return router
}

// realImageHandler 模拟真实图片入口：进入时安装执行策略并 defer cancel（这正是误判客户端取消的来源）。
func realImageHandler(body func(*gin.Context, int)) gin.HandlerFunc {
	visits := 0
	return func(c *gin.Context) {
		visits++
		ctx, cancel := service.WithImageAttemptPolicy(c.Request.Context(), service.DefaultPublicImageAttemptTimeout)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		body(c, visits)
	}
}

func rejectWith429(t *testing.T, c *gin.Context, accountID int64) {
	ctx := c.Request.Context()
	require.NoError(t, service.BeginImageAttempt(ctx, accountID, "img", "generate"))
	service.RecordImageAttemptDecision(ctx, service.ClassifyImageHTTPFailure(http.StatusTooManyRequests, nil))
	c.Set("ops_account_id", accountID)
	c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{AccountID: accountID, Kind: "http_error", UpstreamStatusCode: 429}})
	upstreamFailure(c, http.StatusTooManyRequests, "rate limited")
}

// 生图端点只在“本组未发送”或“确定未生成”时换组；结果未知或缺少图片策略时绝不重放。
func TestSmartRoutingImageEndpointGroupFallback(t *testing.T) {
	for _, tc := range []struct {
		name       string
		group1     func(*testing.T, *gin.Context)
		wantVisits int
		wantStatus int
	}{
		{
			name: "全组冷却未发送_换组成功",
			group1: func(_ *testing.T, c *gin.Context) {
				service.MarkImageAttemptGroupUnsent(c.Request.Context())
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "image_models_cooling_down"}})
			},
			wantVisits: 2, wantStatus: http.StatusOK,
		},
		{
			name:       "确定拒绝可换号_换组成功",
			group1:     func(t *testing.T, c *gin.Context) { rejectWith429(t, c, 11) },
			wantVisits: 2, wantStatus: http.StatusOK,
		},
		{
			name: "已发送结果未知_不换组",
			group1: func(t *testing.T, c *gin.Context) {
				ctx := c.Request.Context()
				require.NoError(t, service.BeginImageAttempt(ctx, 11, "img", "generate"))
				service.RecordImageAttemptDecision(ctx, service.ClassifyImageHTTPFailure(http.StatusBadGateway, nil))
				c.Set("ops_account_id", int64(11))
				c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{AccountID: 11, Kind: "http_error", UpstreamStatusCode: 502}})
				upstreamFailure(c, http.StatusBadGateway, "unknown")
			},
			wantVisits: 1, wantStatus: http.StatusBadGateway,
		},
		{
			name: "已冻结资金_不换组",
			group1: func(t *testing.T, c *gin.Context) {
				// 预占成功后按请求 ID 冻结，换组会与已冻结合同冲突。
				service.MarkSmartRoutingAttemptNonReplayable(c.Request.Context(), "image_billing_reserved")
				rejectWith429(t, c, 11)
			},
			wantVisits: 1, wantStatus: http.StatusTooManyRequests,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := smartRoutingTestKey()
			resolver := &smartRoutingAccountResolver{accounts: map[int64][]int64{1: {11}, 2: {22}, 3: {33}}}
			visits := 0
			router := imageSmartRouter(key, resolver, realImageHandler(func(c *gin.Context, n int) {
				visits = n
				if n == 1 {
					tc.group1(t, c)
					return
				}
				c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"url": "https://example.test/a.png"}}})
			}))
			response := callFailoverRouter(router, "/v1/images/generations", `{"model":"target","prompt":"x"}`)
			require.Equal(t, tc.wantStatus, response.Code, response.Body.String())
			require.Equal(t, tc.wantVisits, visits)
		})
	}
}

// 跨组共享同一执行策略：总发送次数不超过两次、同一账号不重复发送、首次截止时间不随换组刷新。
func TestSmartRoutingImageFallbackSharesBudgetAndDeadline(t *testing.T) {
	key := smartRoutingTestKey()
	resolver := &smartRoutingAccountResolver{accounts: map[int64][]int64{1: {11}, 2: {22}, 3: {33}}}
	var deadlines []time.Time
	var sendsSeen []int
	var thirdErr error
	visits := 0
	router := imageSmartRouter(key, resolver, realImageHandler(func(c *gin.Context, n int) {
		visits = n
		ctx := c.Request.Context()
		deadline, _ := ctx.Deadline()
		deadlines = append(deadlines, deadline)
		sendsSeen = append(sendsSeen, service.ImageAttemptSendCount(ctx))
		switch n {
		case 1:
			rejectWith429(t, c, 11)
		case 2:
			// 第二组再遇明确拒绝；两次发送预算已用尽。
			require.ErrorContains(t, service.BeginImageAttempt(ctx, 11, "img", "generate"), "budget", "同一账号不能在另一组再次发送")
			rejectWith429(t, c, 22)
		default:
			thirdErr = service.BeginImageAttempt(ctx, 33, "img", "generate")
			c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"url": "https://example.test/a.png"}}})
		}
	}))
	response := callFailoverRouter(router, "/v1/images/generations", `{"model":"target","prompt":"x"}`)
	require.Equal(t, []int{0, 1}, sendsSeen[:2], "第二组看到第一组已用掉的发送次数")
	require.Equal(t, deadlines[0], deadlines[1], "换组不能刷新首次截止时间")
	if visits >= 3 {
		require.Error(t, thirdErr, "两次发送后不能再生成")
	} else {
		require.Equal(t, http.StatusTooManyRequests, response.Code, "预算耗尽后停止换组")
	}
}

// 生图端点缺少执行策略时按结果未知处理，即使是可换组的 5xx 也不重放。
func TestSmartRoutingImageEndpointWithoutPolicyNeverReplays(t *testing.T) {
	key := smartRoutingTestKey()
	resolver := &smartRoutingAccountResolver{accounts: map[int64][]int64{1: {11}, 2: {22}, 3: {33}}}
	visits := 0
	router := imageSmartRouter(key, resolver, func(c *gin.Context) {
		visits++
		c.Set("ops_account_id", int64(11))
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{AccountID: 11, Kind: "http_error", UpstreamStatusCode: 503}})
		upstreamFailure(c, http.StatusServiceUnavailable, "busy")
	})
	response := callFailoverRouter(router, "/v1/images/generations", `{"model":"target","prompt":"x"}`)
	// 协调器已为生图端点安装共享策略，但该处理器从未发送也未声明“本组未发送”，仍缺少未生成的正向证据。
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Equal(t, 1, visits)
}

// R6：显式 /images 正文天然带生图模型，不能因正文生图意图被一律阻断换组。
func TestSmartRoutingImageEndpointBodyIntentDoesNotBlockFallback(t *testing.T) {
	key := smartRoutingTestKey()
	resolver := &smartRoutingAccountResolver{accounts: map[int64][]int64{1: {11}, 2: {22}, 3: {33}}}
	visits := 0
	router := imageSmartRouter(key, resolver, realImageHandler(func(c *gin.Context, n int) {
		visits = n
		if n == 1 {
			rejectWith429(t, c, 11)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"url": "https://example.test/a.png"}}})
	}))
	response := callFailoverRouter(router, "/v1/images/generations", `{"model":"gpt-image-2","prompt":"x"}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, 2, visits)
}

// R7：晚识别图片入口在处理器内建策略并在收尾取消（finishImageAttemptRequest 行为），
// 这不是客户端取消，确定拒绝后仍应换组。
func TestSmartRoutingLateImagePolicyCleanupDoesNotBlockFallback(t *testing.T) {
	key := smartRoutingTestKey()
	resolver := &smartRoutingAccountResolver{accounts: map[int64][]int64{1: {11}, 2: {22}, 3: {33}}}
	visits := 0
	router := precisionSmartRouter(key, resolver, func(c *gin.Context) {
		visits++
		ctx, cancel := service.WithImageAttemptPolicy(c.Request.Context(), time.Minute)
		c.Request = c.Request.WithContext(ctx)
		defer service.CancelImageAttemptPolicy(c.Request.Context())
		defer cancel()
		if visits == 1 {
			rejectWith429(t, c, 11)
			return
		}
		c.JSON(http.StatusOK, gin.H{"output": "ok"})
	})
	response := callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, 2, visits)
}
