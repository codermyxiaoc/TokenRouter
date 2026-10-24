//go:build unit

package handler

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

// 首账号被限流后，备用账号的本地客户端限制不能继承前次失败并误写图片冷却。
func TestPublicImagesLocalPolicyAfterSafeSwitchDoesNotCoolBackup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accounts := []service.Account{
		{ID: 81001, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 1, Credentials: map[string]any{"api_key": "local-test", "pool_mode": true}},
		{ID: 81002, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 2, Credentials: map[string]any{"access_token": "local-test"}, Extra: map[string]any{"codex_cli_only": true}},
		{ID: 81003, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 3, Credentials: map[string]any{"api_key": "local-test"}},
	}
	upstream := &imageRoutePolicyUpstream{respond: func(id int64) (int, string, error) {
		return http.StatusTooManyRequests, `{"error":{"code":"rate_limit_exceeded","message":"rate limited"}}`, nil
	}}
	cache := &imageRoutePolicyCache{until: map[string]time.Time{}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	gw := service.NewOpenAIGatewayService(openAIImagesFailoverAccountRepo{accounts: accounts}, &openAIWSUsageHandlerUsageLogRepoStub{}, nil, nil, nil, nil, cache, cfg, nil, nil, service.NewBillingService(cfg, nil), nil, billing, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h := NewOpenAIGatewayHandler(gw, service.NewConcurrencyService(&fakeConcurrencyCache{}), billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)

	response := runImageRoutePolicyRequest(h, "gpt-image-2")
	require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
	require.Equal(t, []int64{81001}, upstream.calls, "本地策略拒绝不能发送备用账号或再次换号")
	first, err := cache.GetImageModelCooldown(context.Background(), 81001, "gpt-image-2", "generate")
	require.NoError(t, err)
	require.Positive(t, first, "真实限流账号应进入模型冷却")
	backup, err := cache.GetImageModelCooldown(context.Background(), 81002, "gpt-image-2", "generate")
	require.NoError(t, err)
	require.Zero(t, backup, "未发送请求的本地策略拒绝不能处罚备用账号")
}
