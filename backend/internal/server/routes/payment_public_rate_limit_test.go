package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/handler"
	"github.com/TokenFlux/TokenRouter/internal/handler/admin"
	servermiddleware "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newPaymentRoutesTestRouter(redisClient *redis.Client) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	v1 := router.Group("/api/v1")
	noop := func(c *gin.Context) { c.Next() }

	RegisterPaymentRoutes(
		v1,
		&handler.PaymentHandler{},
		&handler.PaymentWebhookHandler{},
		&admin.PaymentHandler{},
		servermiddleware.JWTAuthMiddleware(noop),
		servermiddleware.AdminAuthMiddleware(noop),
		servermiddleware.AuditLogMiddleware(noop),
		nil,
		nil,
		redisClient,
	)
	return router
}

func postPublicOrderVerify(router *gin.Engine, remoteAddr string) *httptest.ResponseRecorder {
	// 空请求只触发参数校验；非 429 状态表示已通过独立限流器。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payment/public/orders/verify", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestPublicOrderVerifyRateLimitedPerIP(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	router := newPaymentRoutesTestRouter(rdb)

	for i := 1; i <= publicOrderVerifyRateLimit; i++ {
		w := postPublicOrderVerify(router, "198.51.100.20:1234")
		require.Equal(t, http.StatusBadRequest, w.Code, "request %d should reach the handler", i)
	}

	w := postPublicOrderVerify(router, "198.51.100.20:1234")
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Contains(t, w.Body.String(), "rate limit exceeded")

	// 不同客户端 IP 的限流计数彼此独立。
	w = postPublicOrderVerify(router, "198.51.100.21:1234")
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPublicOrderVerifyRateLimitFailsOpenWhenRedisUnavailable(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = rdb.Close() })

	router := newPaymentRoutesTestRouter(rdb)
	w := postPublicOrderVerify(router, "203.0.113.30:1234")
	require.Equal(t, http.StatusBadRequest, w.Code, "users mid-payment must not be blocked by a Redis outage")
}
