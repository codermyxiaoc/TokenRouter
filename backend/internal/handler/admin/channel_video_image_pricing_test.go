//go:build unit

package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// 管理端零值配置可完整往返，DTO 转换不能让响应或输入对象共享价格指针。
func TestChannelVideoImageInputPricingDTO(t *testing.T) {
	var request channelModelPricingRequest
	require.NoError(t, json.Unmarshal([]byte(`{"models":["model"],"platform":"video","billing_mode":"video","video_image_input_pricing":{"free_images":0,"price":0}}`), &request))
	entries := pricingRequestToService([]channelModelPricingRequest{request})
	require.Len(t, entries, 1)
	response := pricingToResponse(&entries[0])
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"video_image_input_pricing":{"free_images":0,"price":0}`)

	response.VideoImageInputPricing.FreeImages = 2
	*response.VideoImageInputPricing.Price = .5
	*request.VideoImageInputPricing.Price = .25
	require.Zero(t, entries[0].VideoImageInputPricing.FreeImages)
	require.Zero(t, *entries[0].VideoImageInputPricing.Price)

	var legacy channelModelPricingRequest
	require.NoError(t, json.Unmarshal([]byte(`{"models":["legacy"]}`), &legacy))
	legacyEntries := pricingRequestToService([]channelModelPricingRequest{legacy})
	require.Nil(t, legacyEntries[0].VideoImageInputPricing)
	encoded, err = json.Marshal(pricingToResponse(&legacyEntries[0]))
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "video_image_input_pricing")
}
