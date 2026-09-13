package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type smartRoutingModelsRepoStub struct{ gatewayModelsAccountRepoStub }

func (r *smartRoutingModelsRepoStub) ListAllWithFilters(_ context.Context, platform, accountType, status, search string, groupID int64, privacyMode string) ([]service.Account, error) {
	// 目录查询必须固定分组，并保留错误/暂停账号的配置供真实服务分别判断资格。
	if groupID <= 0 || accountType != "" || status != "" || search != "" || privacyMode != "" {
		panic("智能目录查询必须固定分组且不预先过滤调度状态")
	}
	accounts := make([]service.Account, 0, len(r.byGroup[groupID]))
	for _, account := range r.byGroup[groupID] {
		if platform == "" || account.Platform == platform {
			accounts = append(accounts, account)
		}
	}
	return accounts, nil
}

// TestSmartRoutingModelsAggregateWithoutPrefixes 验证候选顺序、模型去重、别名及已撤销分组隔离。
func TestSmartRoutingModelsAggregateWithoutPrefixes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &smartRoutingModelsRepoStub{gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{}}}
	key := &service.APIKey{SmartRouting: true, User: &service.User{ID: 1, DisabledPublicGroups: []int64{3}}, ModelMapping: map[string]string{"alias": "shared", "missing-alias": "unknown"}}
	for index, platform := range []string{service.PlatformOpenAI, service.PlatformAnthropic, service.PlatformGemini} {
		id := int64(index + 1)
		extra := []string{"openai-model", "claude-model", "hidden-model"}[index]
		group := &service.Group{ID: id, Platform: platform, Status: service.StatusActive}
		key.CompositeGroups = append(key.CompositeGroups, service.APIKeyCompositeGroup{GroupID: id, Prefix: "internal", Group: group})
		repo.byGroup[id] = []service.Account{{ID: id, Platform: platform, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
			Credentials: map[string]any{"model_mapping": map[string]any{"shared": "shared", extra: extra}}}}
	}
	h := newGatewayModelsHandlerForTest(repo)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware.ContextKeyAPIKey), key)
	h.Models(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	ids := modelIDsForTest(response.Data)
	require.ElementsMatch(t, []string{"shared", "openai-model", "claude-model", "alias"}, ids)
	require.NotContains(t, ids, "internal/shared")
	require.NotContains(t, ids, "missing-alias")
	require.NotContains(t, ids, "hidden-model")
	// 第一组的全部模型和别名必须先于下一候选独有模型。
	require.Equal(t, "claude-model", ids[len(ids)-1])
}
