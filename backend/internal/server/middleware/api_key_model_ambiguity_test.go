package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 复合密钥必须在移除分组前缀之前拒绝重复模型，避免改写后的合法模型隐藏原始越权模型。
func TestCompositeKeyRejectsAmbiguousModelBeforeRewrite(t *testing.T) {
	for _, payload := range []string{
		`{"model":"GPT/allowed","model":"Claude/blocked","messages":[]}`,
		`{"model":"GPT/allowed","\u006dodel":"Claude/blocked","messages":[]}`,
		`{"model":"GPT/allowed","Model":"Claude/blocked","messages":[]}`,
	} {
		t.Run(payload, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(payload))
			c.Request.Header.Set("Content-Type", "application/json")
			svc := service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, nil)
			selected, err := resolveCompositeAPIKeyRequest(c, svc, compositeMiddlewareMultiGroupTestKey())
			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Nil(t, selected)
			_, _, rewritten := GetCompositeModelFromContext(c)
			require.False(t, rewritten)
		})
	}
}
