package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 管理端测试目录与独立白名单、别名一致，不能因平台默认目录把自定义模型隐藏。
func TestAdditionalProviderAvailableModelsRespectConfiguredNames(t *testing.T) {
	for _, platform := range []string{service.PlatformAntigravity, service.PlatformTypeSafe, service.PlatformCline, service.PlatformCommandCode} {
		t.Run(platform, func(t *testing.T) {
			svc := &availableModelsAdminService{stubAdminService: newStubAdminService(), account: service.Account{
				ID: 42, Platform: platform, Type: service.AccountTypeAPIKey, Status: service.StatusActive,
				Credentials: map[string]any{"model_mapping": map[string]any{"custom-alias": "upstream-model"}, "model_whitelist": []any{"custom-alias", "explicit-only"}},
			}}
			w := httptest.NewRecorder()
			setupAvailableModelsRouter(svc).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/42/models", nil))
			require.Equal(t, 200, w.Code)
			var response struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
			var ids []string
			for _, model := range response.Data {
				ids = append(ids, model.ID)
			}
			require.Contains(t, ids, "custom-alias")
			require.Contains(t, ids, "explicit-only")
			require.NotContains(t, ids, "upstream-model")
			if platform != service.PlatformAntigravity {
				require.ElementsMatch(t, []string{"custom-alias", "explicit-only"}, ids)
			}
		})
	}
}
