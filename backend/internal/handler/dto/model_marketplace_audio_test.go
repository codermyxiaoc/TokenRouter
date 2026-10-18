package dto

import (
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestModelMarketplaceAudioPricePreservesZeroAndSnapshotIsolation(t *testing.T) {
	for _, tc := range []struct{ mode, unit string }{{"tts", "million_characters"}, {"stt", "hour"}, {"realtime", "minute"}} {
		source := service.ModelDisplayPricing{PricingMode: "audio", PriceStatus: "priced", AudioPrice: &service.ModelDisplayAudioPrice{Mode: tc.mode, Price: 0, Unit: tc.unit}}
		dto := modelMarketplacePricingFromService(source)
		body, err := json.Marshal(dto)
		require.NoError(t, err)
		require.JSONEq(t, `{"pricing_mode":"audio","price_status":"priced","audio_price":{"mode":"`+tc.mode+`","price":0,"unit":"`+tc.unit+`"}}`, string(body))
		dto.AudioPrice.Price = 99
		require.Zero(t, source.AudioPrice.Price)
	}
}

func TestModelMarketplaceAudioPriceAbsentForUnknownAndOtherMedia(t *testing.T) {
	for _, mode := range []string{"token", "image", "video", "unknown"} {
		body, err := json.Marshal(modelMarketplacePricingFromService(service.ModelDisplayPricing{PricingMode: mode, PriceStatus: "unpriced"}))
		require.NoError(t, err)
		require.NotContains(t, string(body), "audio_price")
	}
}
