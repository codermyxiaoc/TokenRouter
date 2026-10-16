package handler

import (
	"encoding/json"
	"github.com/TokenFlux/TokenRouter/internal/pkg/gemini"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 原生目录必须按最终账号映射过滤，复合前缀及智能 Key 别名保持可请求。
func TestGeminiAntigravityCompositeCatalog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"composite", "smart", "single"} {
		t.Run(mode, func(t *testing.T) {
			group := &service.Group{ID: 91, Platform: service.PlatformAntigravity, Status: service.StatusActive}
			account := service.Account{ID: 1, Platform: service.PlatformAntigravity, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{
				"model_mapping":   map[string]any{"friendly": "gemini-4-argon-high", "claude-label": "claude-opus-4-6", "gemini-fake": "claude-sonnet-4-6"},
				"model_whitelist": []any{"gemini-4-argon-high", "claude-opus-4-6", "claude-sonnet-4-6"},
			}}
			repo := &smartRoutingModelsRepoStub{gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{91: {account}}}}
			h := newGatewayModelsHandlerForTest(repo)
			key := &service.APIKey{User: &service.User{ID: 1}, IsComposite: mode == "composite", SmartRouting: mode == "smart", Group: group, GroupID: &group.ID, CompositeGroups: []service.APIKeyCompositeGroup{{GroupID: 91, Group: group, Prefix: "ag"}}, ModelMapping: map[string]string{"public": "friendly", "bad": "claude-label"}}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
			c.Set(string(middleware.ContextKeyAPIKey), key)
			h.GeminiV1BetaListModels(c)
			require.Equal(t, 200, w.Code)
			var payload gemini.ModelsListResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
			names := []string{}
			for _, model := range payload.Models {
				names = append(names, model.Name)
			}
			prefix := "models/"
			if mode == "composite" {
				prefix += "ag/"
			}
			require.Contains(t, names, prefix+"friendly")
			require.Contains(t, names, prefix+"public")
			require.NotContains(t, names, prefix+"claude-label")
			require.NotContains(t, names, prefix+"gemini-fake")
			require.NotContains(t, names, prefix+"bad")
			// 普通 Antigravity 模型入口仍同时展示 Claude 与 Gemini，不能复用原生过滤条件。
			w = httptest.NewRecorder()
			c, _ = gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/antigravity/v1/models", nil)
			c.Set(string(middleware.ContextKeyAPIKey), key)
			h.AntigravityModels(c)
			require.Equal(t, 200, w.Code)
			var full gatewayModelsResponseForTest
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &full))
			fullPrefix := ""
			if mode == "composite" {
				fullPrefix = "ag/"
			}
			require.Contains(t, modelIDsForTest(full.Data), fullPrefix+"claude-label")
			require.Contains(t, modelIDsForTest(full.Data), fullPrefix+"friendly")
			// 认证中间件已选择分组并移除前缀，GET 单模型仍以此分组权限进行精确检索。
			for _, model := range []string{"friendly", "gemini-fake", "unknown"} {
				w = httptest.NewRecorder()
				c, _ = gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodGet, "/v1beta/models/"+model, nil)
				c.Params = gin.Params{{Key: "model", Value: model}}
				c.Set(string(middleware.ContextKeyAPIKey), key)
				h.GeminiV1BetaGetModel(c)
				if model == "friendly" {
					require.Equal(t, 200, w.Code)
				} else {
					require.Equal(t, 404, w.Code)
				}
			}
		})
	}
}
