//go:build unit

package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// 管理端显式零价可完整往返，请求、响应、内部价卡的价格指针相互隔离。
func TestChannelVideoFallbackPrepayDTO(t *testing.T) {
	var request channelModelPricingRequest
	require.NoError(t, json.Unmarshal([]byte(`{"models":["model"],"platform":"video","billing_mode":"video_token","video_fallback_price":0,"video_token_prepay":{"price_per_second":0}}`), &request))
	entries := pricingRequestToService([]channelModelPricingRequest{request})
	response := pricingToResponse(&entries[0])
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"video_fallback_price":0`)
	require.Contains(t, string(encoded), `"video_token_prepay":{"price_per_second":0}`)
	*request.VideoFallbackPrice, *response.VideoFallbackPrice = 1, 2
	*request.VideoTokenPrepay.PricePerSecond, *response.VideoTokenPrepay.PricePerSecond = 1, 2
	require.Zero(t, *entries[0].VideoFallbackPrice)
	require.Zero(t, *entries[0].VideoTokenPrepay.PricePerSecond)
	var legacy channelModelPricingRequest
	require.NoError(t, json.Unmarshal([]byte(`{"models":["legacy"]}`), &legacy))
	old := pricingRequestToService([]channelModelPricingRequest{legacy})
	require.Nil(t, old[0].VideoFallbackPrice)
	require.Nil(t, old[0].VideoTokenPrepay)
}
