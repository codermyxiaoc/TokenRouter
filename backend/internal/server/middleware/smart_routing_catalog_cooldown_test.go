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

// catalogCooldownAccountRepo 模拟仓储写入，但保留真实错误策略、目录解析和选组链路。
type catalogCooldownAccountRepo struct {
	service.AccountRepository
	accounts map[int64][]service.Account
}

func (r *catalogCooldownAccountRepo) ListAllWithFilters(_ context.Context, platform, _, status, _ string, groupID int64, _ string) ([]service.Account, error) {
	if groupID <= 0 || status != "" {
		panic("目录查询必须固定分组且不能过滤账号错误状态")
	}
	var result []service.Account
	for _, account := range r.accounts[groupID] {
		if platform == "" || account.Platform == platform {
			result = append(result, account)
		}
	}
	return result, nil
}

func (r *catalogCooldownAccountRepo) SetError(_ context.Context, id int64, message string) error {
	for groupID := range r.accounts {
		for index := range r.accounts[groupID] {
			account := &r.accounts[groupID][index]
			if account.ID == id {
				account.Status, account.Schedulable, account.ErrorMessage = service.StatusError, false, message
			}
		}
	}
	return nil
}

type catalogCooldownCache struct {
	service.GatewayCache
	*failoverResolver
}

func TestSmartRoutingRealCatalogPausedAccountsRemainCoolingWithoutInventingModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key := smartRoutingTestKey()
	key.SmartRoutingCooldownSeconds = 60
	accounts := &catalogCooldownAccountRepo{accounts: make(map[int64][]service.Account)}
	for _, binding := range key.CompositeGroups {
		accounts.accounts[binding.GroupID] = []service.Account{{
			ID: binding.GroupID + 10, Platform: binding.Group.Platform,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
			Credentials: map[string]any{
				"model_mapping":              map[string]any{"target": "target"},
				"custom_error_codes_enabled": true,
				"custom_error_codes":         []any{float64(429), float64(502), float64(524)},
			},
		}}
	}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	clock := &failoverResolver{now: time.Now(), cooling: make(map[[2]int64]time.Time)}
	cache := &catalogCooldownCache{failoverResolver: clock}
	policy := service.NewRateLimitService(accounts, nil, cfg, nil, nil)
	// 使用生产构造器和真实 SmartRoutingService，避免 stub HasModel 掩盖配置查询条件。
	gateway := service.NewGatewayService(
		accounts, nil, nil, nil, nil, nil, nil, cache, cfg,
		nil, nil, nil, policy, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	resolver := service.NewSmartRoutingService(gateway, nil)
	keys := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}
	keyService := service.NewAPIKeyService(keys, nil, nil, nil, nil, nil, cfg)
	auth := gin.HandlerFunc(NewAPIKeyAuthMiddleware(keyService, nil, cfg))
	router := gin.New()
	router.Use(WithSmartRoutingResolver(resolver, auth, func(*gin.Context) bool { return true }))
	var visits []int64
	router.POST("/v1/responses", func(c *gin.Context) {
		selected, _ := GetAPIKeyFromContext(c)
		groupID := *selected.GroupID
		visits = append(visits, groupID)
		account := accounts.accounts[groupID][0]
		status := map[int64]int{1: 429, 2: 502, 3: 524}[groupID]
		decision := policy.ApplyUpstreamError(c.Request.Context(), &account, status, http.Header{}, []byte(`{"error":{"message":"paused upstream"}}`), "target")
		require.True(t, decision.StopScheduling)
		upstreamFailure(c, status, "last-upstream-failure")
	})
	response := callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
	require.Equal(t, 524, response.Code, response.Body.String())
	require.Equal(t, []int64{1, 2, 3}, visits)
	visits = nil
	response = callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
	require.Equal(t, 503, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "SMART_ROUTING_ALL_GROUPS_COOLING")
	require.Equal(t, "60", response.Header().Get("Retry-After"))
	require.Empty(t, visits)
	// 同一组正在冷却也不能将无目录的探针误判成服务不可用。
	response = callFailoverRouter(router, "/v1/responses", `{"model":"unknown-probe"}`)
	require.Equal(t, 404, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "SMART_ROUTING_MODEL_NOT_FOUND")
	require.Empty(t, visits)
	clock.now = clock.now.Add(61 * time.Second)
	response = callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
	require.Equal(t, 503, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "SMART_ROUTING_NO_AVAILABLE_ACCOUNTS")
	require.Empty(t, visits, "冷却到期不会绕过账号的既有永久暂停")
}
