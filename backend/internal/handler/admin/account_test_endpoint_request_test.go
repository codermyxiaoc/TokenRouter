package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 请求选项保持扁平 JSON，避免新增媒体参数后旧前端字段被静默忽略。
func TestAccountTestRequestBindsEndpointAndMediaOptions(t *testing.T) {
	var request TestAccountRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model_id":"test-model","test_type":"stt","test_endpoint":"auto","image_data_url":"data:image/png;base64,fixture","audio_data_url":"data:audio/wav;base64,fixture","test_mode":"image"}`), &request))
	require.Equal(t, "test-model", request.ModelID)
	require.Equal(t, "stt", request.TestType)
	require.Equal(t, "image", request.TestMode)
	require.Equal(t, "auto", request.TestEndpoint)
	require.Equal(t, "data:image/png;base64,fixture", request.ImageDataURL)
	require.Equal(t, "data:audio/wav;base64,fixture", request.AudioDataURL)
}

// 无效 JSON 和过大素材必须在调用任何上游前失败，因此故意不配置测试服务。
func TestAccountTestRejectsInvalidBodyBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, body := range []string{`{"model_id":`, `{"test_endpoint":123}`, `{"audio_data_url":"` + strings.Repeat("a", 32<<20) + `"}`} {
		handler := &AccountHandler{}
		router := gin.New()
		router.POST("/accounts/:id/test", handler.Test)
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/accounts/1/test", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		require.Contains(t, recorder.Body.String(), "Invalid account test request")
	}
}
