package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/gemini"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// geminiDeclaredModelsUpstream 记录模型接口是否透传到上游，上游返回一个账号未声明的模型。
type geminiDeclaredModelsUpstream struct {
	service.HTTPUpstream
	calls int
}

func (u *geminiDeclaredModelsUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	u.calls++
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"models":[{"name":"models/gemini-upstream-only"}]}`))}, nil
}

func newGeminiDeclaredModelsHandler(t *testing.T, groupID int64, accounts []service.Account) (*GatewayHandler, *geminiDeclaredModelsUpstream) {
	t.Helper()
	repo := &geminiAllowlistAccountRepoStub{gatewayModelsAccountRepoStub: gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{groupID: accounts}}}
	upstream := &geminiDeclaredModelsUpstream{}
	h := newGatewayModelsHandlerForTest(repo)
	h.geminiCompatService = service.NewGeminiMessagesCompatService(repo, nil, nil, nil, nil, nil, upstream, nil, &config.Config{})
	return h, upstream
}

func geminiDeclaredModelsContext(groupID int64, path, model string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	if model != "" {
		c.Params = gin.Params{{Key: "model", Value: model}}
	}
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformGemini}})
	return c, rec
}

// 分组内 Gemini 账号都声明了模型映射时，/v1beta/models 列表与详情按与 /v1/models 同源的可请求模型应答，不透传单个账号的上游列表。
func TestGeminiV1BetaModelsUseDeclaredMappings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(47)
	h, upstream := newGeminiDeclaredModelsHandler(t, groupID, []service.Account{
		{ID: 1, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey, Schedulable: true, Status: service.StatusActive, Credentials: map[string]any{"api_key": "a", "model_mapping": map[string]any{"gemini-4-argon": "gemini-4-argon"}}},
		{ID: 2, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey, Schedulable: true, Status: service.StatusActive, Credentials: map[string]any{"api_key": "b", "model_mapping": map[string]any{"gemini-2.5-flash": "gemini-2.5-flash"}}},
	})

	c, rec := geminiDeclaredModelsContext(groupID, "/v1beta/models", "")
	h.GeminiV1BetaListModels(c)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list gemini.ModelsListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	names := []string{}
	for _, model := range list.Models {
		names = append(names, model.Name)
	}
	require.ElementsMatch(t, []string{"models/gemini-2.5-flash", "models/gemini-4-argon"}, names)

	for model, status := range map[string]int{"gemini-4-argon": http.StatusOK, "gemini-2.5-flash": http.StatusOK, "gemini-upstream-only": http.StatusNotFound} {
		c, rec := geminiDeclaredModelsContext(groupID, "/v1beta/models/"+model, model)
		h.GeminiV1BetaGetModel(c)
		require.Equal(t, status, rec.Code, model+": "+rec.Body.String())
	}
	require.Zero(t, upstream.calls, "账号都声明映射时不透传上游列表")
}

// 有账号未配置映射（可接受任意模型）时，模型接口仍由选中账号的上游应答。
func TestGeminiV1BetaModelsUnmappedAccountKeepsUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(48)
	h, upstream := newGeminiDeclaredModelsHandler(t, groupID, []service.Account{
		{ID: 1, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey, Schedulable: true, Status: service.StatusActive, Credentials: map[string]any{"api_key": "a", "model_mapping": map[string]any{"gemini-4-argon": "gemini-4-argon"}}},
		{ID: 2, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey, Schedulable: true, Status: service.StatusActive, Credentials: map[string]any{"api_key": "b"}},
	})
	c, rec := geminiDeclaredModelsContext(groupID, "/v1beta/models", "")
	h.GeminiV1BetaListModels(c)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "gemini-upstream-only")
	require.Equal(t, 1, upstream.calls)
}
