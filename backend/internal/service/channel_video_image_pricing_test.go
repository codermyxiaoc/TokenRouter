//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// 配置与价格指针都必须复制，分组价卡 JSON 也必须保留显式零值。
func TestChannelVideoImageInputPricingCloneAndJSON(t *testing.T) {
	price := 0.0
	original := ChannelModelPricing{VideoImageInputPricing: &VideoImageInputPricing{FreeImages: 0, Price: &price}}
	cloned := original.Clone()
	cloned.VideoImageInputPricing.FreeImages = 3
	*cloned.VideoImageInputPricing.Price = .25
	require.Zero(t, original.VideoImageInputPricing.FreeImages)
	require.Zero(t, *original.VideoImageInputPricing.Price)

	encoded, err := json.Marshal([]ChannelModelPricing{original})
	require.NoError(t, err)
	var decoded []ChannelModelPricing
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, original.VideoImageInputPricing, decoded[0].VideoImageInputPricing)
	require.Contains(t, string(encoded), `"video_image_input_pricing":{"free_images":0,"price":0}`)

	var old ChannelModelPricing
	require.NoError(t, json.Unmarshal([]byte(`{"models":["legacy"]}`), &old))
	require.Nil(t, old.Clone().VideoImageInputPricing)
	encoded, err = json.Marshal(old)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "video_image_input_pricing")
}
