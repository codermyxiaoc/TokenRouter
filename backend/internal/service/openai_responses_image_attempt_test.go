//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 工具模型保持实际发送值，参考图和局部重绘使用各自的健康键。
func TestResponsesImageAttemptScope(t *testing.T) {
	for _, tc := range []struct{ body, model, operation string }{
		{`{"model":"gpt-5.2","tools":[{"type":"image_generation","model":"gpt-image-2.5"}],"input":"draw"}`, "gpt-image-2.5", "generate"},
		{`{"tools":[{"type":"image_generation"}],"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/source.png"}]}]}`, "gpt-image-2", "edit"},
		{`{"tools":[{"type":"image_generation","input_image_mask":{"image_url":"https://example.test/mask.png"}}]}`, "gpt-image-2", "inpaint"},
	} {
		model, operation := openAIResponsesImageAttemptScope([]byte(tc.body), "gpt-5.2")
		require.Equal(t, tc.model, model)
		require.Equal(t, tc.operation, operation)
	}
}

// 管理员指定账号测试没有用户执行 Key，不能隐式开启客户端自动换号策略。
func TestResponsesImageAttemptAdminWithoutKeyUnchanged(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx := prepareOpenAIResponsesImageAttempt(context.Background(), c, &Account{ID: 1, Platform: PlatformOpenAI}, []byte(`{"tools":[{"type":"image_generation"}]}`), "gpt-5.2")
	require.False(t, ImageAttemptHasPolicy(ctx))
}
