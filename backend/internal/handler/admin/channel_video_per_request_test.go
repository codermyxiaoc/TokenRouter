//go:build unit

package admin

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 实际 HTTP JSON 绑定必须接受新模式，避免表单提交被枚举校验提前拦截。
func TestChannelVideoPerRequestHTTPBindingAndDTO(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/", strings.NewReader(`{"models":["video-model"],"platform":"video","billing_mode":"video_per_request","video_fallback_price":0,"video_image_input_pricing":{"free_images":5,"price":0.15}}`))
	c.Request.Header.Set("Content-Type", "application/json")
	var request channelModelPricingRequest
	require.NoError(t, c.ShouldBindJSON(&request))
	entries := pricingRequestToService([]channelModelPricingRequest{request})
	require.Equal(t, service.BillingModeVideoPerRequest, entries[0].BillingMode)
	raw, err := json.Marshal(pricingToResponse(&entries[0]))
	require.NoError(t, err)
	require.Contains(t, string(raw), `"billing_mode":"video_per_request"`)
	require.Contains(t, string(raw), `"video_fallback_price":0`)
	require.Contains(t, string(raw), `"video_image_input_pricing":{"free_images":5,"price":0.15}`)
}
