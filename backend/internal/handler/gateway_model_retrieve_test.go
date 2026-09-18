package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 单模型返回列表中的同一个最终对象，不能跳过自定义目录或 Key 别名可见性。
func TestGatewayModelRetrieveUsesVisibleCatalog(t *testing.T) {
	groupID := int64(42)
	h := newGatewayModelsHandlerForTest(&gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{
		groupID: {{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{
			"model_mapping": map[string]any{"vendor/model": "vendor/model", "hidden-model": "hidden-model"},
		}}},
	}})
	key := &service.APIKey{Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI,
		ModelsListConfig: service.GroupModelsListConfig{Enabled: true, Models: []string{"vendor/model"}}},
		ModelMapping: map[string]string{"review": "vendor/model", "missing-alias": "missing"}}
	request := func(model *string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		c.Set(string(middleware.ContextKeyAPIKey), key)
		if model != nil {
			c.Params = gin.Params{{Key: "model", Value: "/" + *model}}
		}
		h.Models(c)
		return recorder
	}
	list := request(nil)
	require.Equal(t, http.StatusOK, list.Code)
	var catalog struct {
		Data []json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &catalog))
	require.Len(t, catalog.Data, 2)
	for _, item := range catalog.Data {
		var model struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.Unmarshal(item, &model))
		retrieved := request(&model.ID)
		require.Equal(t, http.StatusOK, retrieved.Code)
		require.JSONEq(t, string(item), retrieved.Body.String())
	}
	for _, hidden := range []string{"hidden-model", "missing-alias", "missing", "", "vendor%2Fmodel", "vendor/../model"} {
		retrieved := request(&hidden)
		require.Equal(t, http.StatusNotFound, retrieved.Code, hidden)
		require.Contains(t, retrieved.Body.String(), `"code":"model_not_found"`)
	}
}

// 指定订阅只开放套餐内分组，复合前缀不能让检索进入套餐外目录。
func TestGatewayModelRetrieveRespectsPreferredSubscription(t *testing.T) {
	h := newGatewayModelsHandlerForTest(&gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{
		52: {{ID: 1, Platform: service.PlatformOpenAI, Credentials: map[string]any{"model_mapping": map[string]any{"vendor/model": "vendor/model"}}}},
		53: {{ID: 2, Platform: service.PlatformOpenAI, Credentials: map[string]any{"model_mapping": map[string]any{"blocked-model": "blocked-model"}}}},
	}})
	key := &service.APIKey{IsComposite: true, BillingMode: service.APIKeyBillingModeSubscription,
		User: &service.User{Status: service.StatusActive, AllowedGroups: []int64{52, 53}},
		CompositeGroups: []service.APIKeyCompositeGroup{
			{GroupID: 52, Prefix: "Allowed", Group: &service.Group{ID: 52, Platform: service.PlatformOpenAI, Status: service.StatusActive, IsExclusive: true}},
			{GroupID: 53, Prefix: "Blocked", Group: &service.Group{ID: 53, Platform: service.PlatformOpenAI, Status: service.StatusActive, IsExclusive: true}},
		}}
	for _, test := range []struct {
		model  string
		status int
	}{
		{"Allowed/vendor/model", http.StatusOK}, {"Blocked/blocked-model", http.StatusNotFound},
		{"vendor/model", http.StatusNotFound}, {"allowed/vendor/model", http.StatusNotFound},
	} {
		t.Run(test.model, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/models/"+test.model, nil)
			c.Params = gin.Params{{Key: "model", Value: "/" + test.model}}
			c.Set(string(middleware.ContextKeyAPIKey), key)
			c.Set(string(middleware.ContextKeyAPIKeyBilling), &middleware.APIKeyBillingContext{
				Mode: service.APIKeyBillingModeSubscription, Source: "subscription", Available: true,
				Subscription: &service.UserSubscription{Plan: &service.SubscriptionPlan{GroupIDs: []int64{52}}},
			})
			h.Models(c)
			require.Equal(t, test.status, recorder.Code, recorder.Body.String())
		})
	}
}
