package dto

import (
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 广场公开实收回退价和固定预扣原价，显式零价不会消失或污染来源快照。
func TestModelMarketplaceVideoFallbackPrepayDTO(t *testing.T) {
	zero, prepay := 0.0, .0000123
	pricing := service.ModelDisplayPricing{PricingMode: "video_token", PriceStatus: "priced", VideoFallbackPrice: &zero,
		VideoTokenPrepay: &service.VideoTokenPrepayConfig{PricePerSecond: &prepay}}
	dto := modelMarketplacePricingFromService(pricing)
	raw, err := json.Marshal(dto)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"video_fallback_price":0`)
	require.Contains(t, string(raw), `"video_token_prepay":{"price_per_second":0.0000123}`)
	*dto.VideoFallbackPrice, *dto.VideoTokenPrepay.PricePerSecond = 1, 2
	require.Zero(t, *pricing.VideoFallbackPrice)
	require.Equal(t, .0000123, *pricing.VideoTokenPrepay.PricePerSecond)
	legacy := modelMarketplacePricingFromService(service.ModelDisplayPricing{})
	raw, err = json.Marshal(legacy)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "video_fallback_price")
	require.NotContains(t, string(raw), "video_token_prepay")
}
