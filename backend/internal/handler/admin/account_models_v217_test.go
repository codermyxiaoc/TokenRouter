package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 账号测试目录必须区分订阅退役、API Key 可用及显式替代映射。
func TestAccountModelsRetirementCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, typ, target string
		keep              bool
	}{
		{"OAuth退役", service.AccountTypeOAuth, "gpt-5.5", false},
		{"Setup退役", service.AccountTypeSetupToken, "gpt-5.5", false},
		{"APIKey保留", service.AccountTypeAPIKey, "gpt-5.5", true},
		{"替代映射保留", service.AccountTypeOAuth, "gpt-6.1-sol", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := service.Account{ID: 42, Platform: service.PlatformOpenAI, Type: tc.typ, Credentials: map[string]any{
				"model_mapping": map[string]any{"public-alias": tc.target}, "model_whitelist": []any{tc.target},
			}}
			svc := &availableModelsAdminService{stubAdminService: newStubAdminService(), account: account}
			rec := httptest.NewRecorder()
			setupAvailableModelsRouter(svc).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/42/models", nil))
			require.Equal(t, http.StatusOK, rec.Code)
			var body struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			found := false
			for _, model := range body.Data {
				if model.ID == "public-alias" {
					found = true
				}
			}
			require.Equal(t, tc.keep, found)
			if !tc.keep {
				require.NotContains(t, rec.Body.String(), `"data":null`)
			}
		})
	}
}

// 能力元数据只能修饰已配置模型，不能重新开放目录中的其它模型或泄漏上游正文。
func TestAccountModelsAntigravityMetadataPreservesWhitelist(t *testing.T) {
	account := service.Account{ID: 42, Platform: service.PlatformAntigravity, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"model_mapping": map[string]any{"client-model": "gemini-4-argon-high"}, "model_whitelist": []any{"gemini-4-argon-high"}},
		Extra: map[string]any{"upstream_model_metadata": map[string]any{"source": "upstream", "models": map[string]any{
			"gemini-4-argon-high": map[string]any{"display_name": "Gemini Argon", "reasoning": true, "context_window": 1000000, "input_modalities": []any{"text", "image"}},
			"private-other-model": map[string]any{"display_name": "not allowed"},
		}}},
	}
	rec := httptest.NewRecorder()
	setupAvailableModelsRouter(&availableModelsAdminService{stubAdminService: newStubAdminService(), account: account}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/42/models", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"id":"client-model"`)
	require.Contains(t, rec.Body.String(), `"context_window":1000000`)
	require.NotContains(t, rec.Body.String(), "private-other-model")
}
