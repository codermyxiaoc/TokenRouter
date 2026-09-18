package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 元数据入口只跳过消费资源预检，不能跳过 Key、余额和指定订阅校验。
func TestModelRetrieveEndpointClassification(t *testing.T) {
	for _, path := range []string{"/v1/models/vendor/model", "/models/gpt-5", "/antigravity/models/gemini-2.5-flash", "/antigravity/v1/models/group/vendor/model"} {
		t.Run(path, func(t *testing.T) {
			require.True(t, isGatewayModelRetrieveEndpoint(http.MethodGet, path))
			require.True(t, isCompositeKeyNoModelEndpoint(http.MethodGet, path))
			require.True(t, isAPIKeyNonConsumingRequest(http.MethodGet, path))
			require.False(t, isCompositeKeyBillingBypassEndpoint(http.MethodGet, path))
			require.False(t, isAPIKeyNonConsumingRequest(http.MethodPost, path))
		})
	}
	for _, path := range []string{"/unknown/models", "/api/v1/models", "/foo/v1/models/gpt-5", "/v1/models-private/gpt-5", "/models-private/models", "/v1/responses", "/v1beta/models/gemini:generateContent", "/antigravity/v1beta/models/gemini:streamGenerateContent"} {
		require.False(t, isGatewayModelRetrieveEndpoint(http.MethodGet, path), path)
		require.False(t, isAPIKeyNonConsumingRequest(http.MethodGet, path), path)
	}
}

// 复合 Key 检索必须保留完整前缀，交给最终可见列表匹配，不能先选择一个分组或重写 ID。
func TestModelRetrieveCompositeKeyKeepsCatalogScope(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models/GPT/vendor/model", nil)
	c.Params = gin.Params{{Key: "model", Value: "/GPT/vendor/model"}}
	selected, err := resolveCompositeAPIKeyRequest(c, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, nil), compositeMiddlewareTestKey())
	require.NoError(t, err)
	require.Nil(t, selected.GroupID)
	require.Equal(t, "/GPT/vendor/model", c.Param("model"))
	_, marked := c.Get(compositeKeyNoGroupContextKey)
	require.True(t, marked)
}
