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

type preciseSmartResolver struct {
	evaluate func(int64) service.SmartRoutingGroupAvailability
}

func (r *preciseSmartResolver) UsesSmartRoutingAccountCooldown() bool { return true }
func (r *preciseSmartResolver) CooldownSmartRoutingAccounts(context.Context, int64, []int64, string, string, time.Duration) error {
	return nil
}
func (r *preciseSmartResolver) EvaluateSmartRoutingGroup(_ context.Context, g *service.Group, _, _ string) (service.SmartRoutingGroupAvailability, error) {
	return r.evaluate(g.ID), nil
}

func precisionSmartRouter(key *service.APIKey, resolver service.SmartRoutingGroupResolver, handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	keys := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	router := gin.New()
	router.Use(WithSmartRoutingResolver(resolver, gin.HandlerFunc(NewAPIKeyAuthMiddleware(keys, nil, cfg)), func(*gin.Context) bool { return true }))
	router.POST("/v1/responses", handler)
	return router
}

func TestSmartRoutingPrecisionRechecksExpiredObservationOnce(t *testing.T) {
	key := smartRoutingTestKey()
	key.SmartRoutingCooldownSeconds = 10
	calls := 0
	resolver := &preciseSmartResolver{evaluate: func(id int64) service.SmartRoutingGroupAvailability {
		if id == 1 {
			calls++
			if calls == 1 {
				return service.SmartRoutingGroupAvailability{HasModel: true, Cooling: true, RetryAfter: time.Minute, RetryAt: time.Now().Add(-time.Millisecond)}
			}
			return service.SmartRoutingGroupAvailability{HasModel: true, Schedulable: true}
		}
		return service.SmartRoutingGroupAvailability{}
	}}
	visits := 0
	router := precisionSmartRouter(key, resolver, func(c *gin.Context) { visits++; c.JSON(200, gin.H{"ok": true}) })
	response := callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Equal(t, 2, calls)
	require.Equal(t, 1, visits)
	require.Empty(t, response.Header().Get("Retry-After"))
}

func TestSmartRoutingPrecisionRechecksAfterCandidateScanCrossesExpiry(t *testing.T) {
	key := smartRoutingTestKey()
	key.SmartRoutingCooldownSeconds = 10
	calls := 0
	var until time.Time
	resolver := &preciseSmartResolver{evaluate: func(id int64) service.SmartRoutingGroupAvailability {
		if id == 1 {
			calls++
			if calls > 1 {
				return service.SmartRoutingGroupAvailability{HasModel: true, Schedulable: true}
			}
			until = time.Now().Add(100 * time.Millisecond)
			return service.SmartRoutingGroupAvailability{HasModel: true, Cooling: true, RetryAfter: 100 * time.Millisecond, RetryAt: until}
		}
		// 可靠地跨越第一个候选的到期点，不假设系统 Sleep 或调度精度。
		if remaining := time.Until(until); remaining > 0 {
			<-time.After(remaining + time.Millisecond)
		}
		return service.SmartRoutingGroupAvailability{HasModel: true, Cooling: true, RetryAfter: time.Minute, RetryAt: time.Now().Add(time.Minute)}
	}}
	router := precisionSmartRouter(key, resolver, func(c *gin.Context) {
		selected, _ := GetAPIKeyFromContext(c)
		c.JSON(200, gin.H{"group": selected.Group.ID})
	})
	response := callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Equal(t, 2, calls)
}

func TestSmartRoutingPrecisionNeverLoopsOnPermanentlyStaleResolver(t *testing.T) {
	key := smartRoutingTestKey()
	key.SmartRoutingCooldownSeconds = 10
	calls := 0
	resolver := &preciseSmartResolver{evaluate: func(int64) service.SmartRoutingGroupAvailability {
		calls++
		return service.SmartRoutingGroupAvailability{HasModel: true, Cooling: true, RetryAfter: time.Minute, RetryAt: time.Now().Add(-time.Second)}
	}}
	router := precisionSmartRouter(key, resolver, func(c *gin.Context) { t.Error("stale candidates must not be sent upstream"); c.Status(200) })
	response := callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
	require.Equal(t, 503, response.Code)
	require.NotContains(t, response.Body.String(), "SMART_ROUTING_ALL_GROUPS_COOLING")
	require.LessOrEqual(t, calls, 2*len(key.CompositeGroups))
	require.Empty(t, response.Header().Get("Retry-After"))
}

// 图片别名在真实分派时才识别也必须保持同组预算，不能借智能路由换组重新生成。
func TestSmartRoutingImagePolicyInstalledDuringForwardPreventsGroupReplay(t *testing.T) {
	key := smartRoutingTestKey()
	key.SmartRoutingCooldownSeconds = 10
	resolver := &smartRoutingAccountResolver{accounts: map[int64][]int64{1: {11}, 2: {22}, 3: {33}}}
	visits := 0
	router := precisionSmartRouter(key, resolver, func(c *gin.Context) {
		visits++
		ctx, _ := service.WithImageAttemptPolicy(c.Request.Context(), time.Minute)
		c.Request = c.Request.WithContext(ctx)
		t.Cleanup(func() { service.CancelImageAttemptPolicy(ctx) })
		selected, _ := GetAPIKeyFromContext(c)
		c.Set("ops_account_id", int64(11))
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{AccountID: 11, GroupID: selected.Group.ID, Kind: "http_error", UpstreamStatusCode: 502}})
		upstreamFailure(c, http.StatusBadGateway, "image-failed")
	})
	response := callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
	require.Equal(t, 502, response.Code)
	require.Equal(t, 1, visits)
	require.Equal(t, 10*time.Second, resolver.cooling["1/target//v1/responses"][11])
}
