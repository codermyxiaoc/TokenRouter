package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 视频测试目录必须由账号真实配置派生，不能回落聊天模型或泄漏凭据。
func TestAccountHandlerGetAvailableModels_VideoTestCatalog(t *testing.T) {
	tests := []struct {
		name        string
		credentials map[string]any
		want        map[string][]string
	}{
		{
			name:        "empty catalog stays empty",
			credentials: map[string]any{"video_endpoints": []string{"compat"}},
			want:        map[string][]string{},
		},
		{
			name: "binding limits each model",
			credentials: map[string]any{
				"video_endpoints":      []string{"compat", "seedance", "minimax"},
				"video_model_bindings": map[string]any{"seed-model": []string{"seedance", "compat"}, "h3-model": "minimax"},
			},
			want: map[string][]string{"seed-model": {"compat", "seedance"}, "h3-model": {"minimax"}},
		},
		{
			name: "aliases use final model binding and whitelist",
			credentials: map[string]any{
				"video_endpoints":      []string{"compat", "seedance"},
				"model_mapping":        map[string]any{"public-name": "private-name", "forbidden-name": "blocked"},
				"model_whitelist":      []string{"private-name"},
				"video_model_bindings": map[string]any{"private-name": "seedance", "blocked": "compat"},
			},
			want: map[string][]string{"public-name": {"seedance"}, "private-name": {"seedance"}},
		},
		{
			name: "unbound whitelist model inherits explicitly enabled endpoints",
			credentials: map[string]any{
				"video_endpoints": []string{"minimax", "openai_videos"},
				"model_whitelist": []string{"h3"},
			},
			want: map[string][]string{"h3": {"minimax", "openai_videos"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.credentials["api_key"] = "fixture-secret-not-for-response"
			tt.credentials["base_url"] = "https://video.example"
			account := service.Account{ID: 42, Platform: service.PlatformVideo, Type: service.AccountTypeAPIKey, Credentials: tt.credentials}
			router := setupAvailableModelsRouter(&availableModelsAdminService{stubAdminService: newStubAdminService(), account: account})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/42/models", nil))
			require.Equal(t, http.StatusOK, rec.Code)
			require.NotContains(t, rec.Body.String(), "fixture-secret-not-for-response")
			var response struct {
				Data []struct {
					ID        string   `json:"id"`
					Endpoints []string `json:"video_endpoints"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			require.NotNil(t, response.Data, "空目录也必须返回数组")
			got := make(map[string][]string, len(response.Data))
			for _, model := range response.Data {
				got[model.ID] = model.Endpoints
			}
			require.Equal(t, tt.want, got)
		})
	}
}
