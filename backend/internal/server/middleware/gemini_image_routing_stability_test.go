//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 复合 Key 同时绑定开/关两组时，门禁必须继承请求前缀选中的组，而不是旁边允许生图的组。
func TestGeminiStability_CompositeSelectionRetainsImagePermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, native := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			name := "disabled"
			if enabled {
				name = "enabled"
			}
			t.Run(name+map[bool]string{true: " native", false: " chat"}[native], func(t *testing.T) {
				denied := &service.Group{ID: 11, Platform: service.PlatformGemini, Status: service.StatusActive, Hydrated: true}
				allowed := &service.Group{ID: 12, Platform: service.PlatformAntigravity, Status: service.StatusActive, Hydrated: true, AllowImageGeneration: true}
				key := &service.APIKey{ID: 1, UserID: 2, IsComposite: true, User: &service.User{ID: 2, Status: service.StatusActive}, CompositeGroups: []service.APIKeyCompositeGroup{
					{GroupID: 11, Prefix: "off", NormalizedPrefix: "off", Group: denied}, {GroupID: 12, Prefix: "on", NormalizedPrefix: "on", Group: allowed},
				}}
				prefix := "off"
				want := int64(11)
				if enabled {
					prefix = "on"
					want = 12
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+prefix+`/studio-alias","messages":[]}`))
				if native {
					c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/"+prefix+"/studio-alias:generateContent", strings.NewReader(`{}`))
					c.Params = gin.Params{{Key: "modelAction", Value: "/" + prefix + "/studio-alias:generateContent"}}
				}
				selected, err := resolveCompositeAPIKeyRequest(c, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, nil), key)
				require.NoError(t, err)
				require.Equal(t, want, *selected.GroupID)
				require.Equal(t, !enabled, service.GeminiImageGenerationDenied(selected.Group, nil, "studio-alias", "gemini-3-pro-image"))
				require.Nil(t, key.Group)
				require.False(t, denied.AllowImageGeneration)
				require.True(t, allowed.AllowImageGeneration)
			})
		}
	}
}

// 智能选组投影不得把后备组的媒体权限复制到本轮，也不得回写原始共享 Key。
func TestGeminiStability_SmartSelectionRetainsImagePermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, selectedID := range []int64{1, 2} {
		key := smartRoutingTestKey()
		key.CompositeGroups = key.CompositeGroups[:2]
		for i := range key.CompositeGroups {
			key.CompositeGroups[i].Group.Platform = service.PlatformGemini
			key.CompositeGroups[i].Group.AllowImageGeneration = i == 1
		}
		key.ModelMapping = map[string]string{"studio-alias": "gemini-3-pro-image"}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"studio-alias","messages":[]}`))
		c.Set(smartRoutingResolverContextKey, smartRoutingResolverStub{check: func(_ context.Context, g *service.Group, model, endpoint string) (service.SmartRoutingGroupAvailability, error) {
			require.Equal(t, "gemini-3-pro-image", model)
			return service.SmartRoutingGroupAvailability{HasModel: true, Schedulable: g.ID == selectedID}, nil
		}})
		selected, err := resolveSmartRoutingAPIKeyRequest(c, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, nil), key)
		require.NoError(t, err)
		require.Equal(t, selectedID, *selected.GroupID)
		require.Equal(t, selectedID == 1, service.GeminiImageGenerationDenied(selected.Group, nil, "gemini-3-pro-image"))
		require.Nil(t, key.Group)
		require.Nil(t, key.GroupID)
	}
}
