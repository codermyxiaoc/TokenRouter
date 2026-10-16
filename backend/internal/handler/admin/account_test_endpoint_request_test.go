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

// MiniMax 选项保持扁平，显式关闭水印与未提供必须能区分。
func TestAccountTestRequestBindsMiniMaxOptions(t *testing.T) {
	var request TestAccountRequest
	require.NoError(t, json.Unmarshal([]byte(`{"test_type":"video","video_endpoint":"minimax","reference_image_url":"https://cdn.example/first_frame.png","reference_image_transport":"https_url","aigc_watermark":false}`), &request))
	require.Equal(t, "https://cdn.example/first_frame.png", request.ReferenceImageURL)
	require.Equal(t, "https_url", request.ReferenceImageTransport)
	require.NotNil(t, request.AIGCWatermark)
	require.False(t, *request.AIGCWatermark)
	var omitted TestAccountRequest
	require.NoError(t, json.Unmarshal([]byte(`{"test_type":"video","video_endpoint":"minimax"}`), &omitted))
	require.Nil(t, omitted.AIGCWatermark)
}

// 无效 JSON 和过大素材必须在调用任何上游前失败，因此故意不配置测试服务。
func TestAccountTestRejectsInvalidBodyBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, body := range []string{`{"model_id":`, `{"test_endpoint":123}`, `{"reference_image_url":true}`, `{"reference_image_transport":true}`, `{"reference_image_transport":{}}`, `{"aigc_watermark":"true"}`, `{"aigc_watermark":1}`, `{"audio_data_url":"` + strings.Repeat("a", 32<<20) + `"}`} {
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
