package dto

import (
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestModelMarketplaceVideoPricingDTOIncludesZeroAndUnit(t *testing.T) {
	pricing := modelMarketplacePricingFromService(service.ModelDisplayPricing{
		PricingMode: "video", PriceStatus: "priced",
		VideoPrices: []service.ModelDisplayVideoPrice{
			{Resolution: "480p", Price: 0, Unit: "second"},
			{Resolution: "720p", Price: 1.5, Unit: "request"},
		},
	})
	body, err := json.Marshal(pricing)
	require.NoError(t, err)
	require.JSONEq(t, `{"pricing_mode":"video","price_status":"priced","video_prices":[{"resolution":"480p","price":0,"unit":"second","video_token_prepay":null,"video_image_input_pricing":null},{"resolution":"720p","price":1.5,"unit":"request","video_token_prepay":null,"video_image_input_pricing":null}]}`, string(body))
}

// 每档关闭配置显式序列化 null，零配置和回退自有单位不能被省略或共享指针。
func TestModelMarketplaceVideoPerTierContractsDTO(t *testing.T) {
	zero, hasReference := 0.0, false
	row := service.ModelDisplayVideoPrice{Resolution: "720p", HasReferenceVideo: &hasReference, Unit: "million_tokens",
		VideoTokenPrepay:       &service.VideoTokenPrepayConfig{PricePerSecond: &zero},
		VideoImageInputPricing: &service.VideoImageInputPricing{FreeImages: 5, Price: &zero}}
	fallback := row
	fallback.Resolution, fallback.HasReferenceVideo = "", nil
	source := service.ModelDisplayPricing{PricingMode: "video", PriceStatus: "priced",
		VideoPrices: []service.ModelDisplayVideoPrice{{Resolution: "480p", Unit: "second"}, row}, VideoFallbackPricing: &fallback}
	dto := modelMarketplacePricingFromService(source)
	body, err := json.Marshal(dto)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	rows := decoded["video_prices"].([]any)
	first := rows[0].(map[string]any)
	require.Contains(t, first, "video_token_prepay")
	require.Nil(t, first["video_token_prepay"])
	require.Contains(t, first, "video_image_input_pricing")
	require.Nil(t, first["video_image_input_pricing"])
	second := rows[1].(map[string]any)
	require.Equal(t, false, second["has_reference_video"])
	require.Equal(t, 0.0, second["price"])
	decodedFallback := decoded["video_fallback_pricing"].(map[string]any)
	require.Equal(t, "million_tokens", decodedFallback["unit"])
	require.Equal(t, 0.0, decodedFallback["price"])
	require.Equal(t, "", decodedFallback["resolution"])
	require.NotContains(t, decodedFallback, "has_reference_video")
	require.NotContains(t, decoded, "video_fallback_price")
	*dto.VideoPrices[1].VideoTokenPrepay.PricePerSecond = 1
	*dto.VideoPrices[1].VideoImageInputPricing.Price = 2
	*dto.VideoPrices[1].HasReferenceVideo = true
	require.Zero(t, *source.VideoPrices[1].VideoTokenPrepay.PricePerSecond)
	require.Zero(t, *source.VideoPrices[1].VideoImageInputPricing.Price)
	require.False(t, *source.VideoPrices[1].HasReferenceVideo)
	require.Zero(t, *dto.VideoFallbackPricing.VideoTokenPrepay.PricePerSecond)
	require.Zero(t, *dto.VideoFallbackPricing.VideoImageInputPricing.Price)
	*dto.VideoFallbackPricing.VideoTokenPrepay.PricePerSecond = 3
	require.Zero(t, *source.VideoFallbackPricing.VideoTokenPrepay.PricePerSecond)
}
