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
	require.JSONEq(t, `{"pricing_mode":"video","price_status":"priced","video_prices":[{"resolution":"480p","price":0,"unit":"second"},{"resolution":"720p","price":1.5,"unit":"request"}]}`, string(body))
}
