package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// 新单价与旧双行配置都按分辨率收费；参考视频仅为输入信息，不能改变费用。
func TestVideoResolutionPriceIgnoresReferenceInputAcrossModes(t *testing.T) {
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoToken, BillingModeVideoPerRequest} {
		for _, price := range []float64{0, 2.5} {
			for _, shape := range []string{"new", "legacy_true_only", "legacy_pair", "legacy_pair_reversed"} {
				t.Run(string(mode)+"/"+shape+"/"+map[bool]string{true: "free", false: "paid"}[price == 0], func(t *testing.T) {
					rows := []VideoPriceTier{{Resolution: "768p", Price: &price}}
					switch shape {
					case "legacy_true_only":
						rows[0].HasReferenceVideo = true
					case "legacy_pair":
						rows = append(rows, VideoPriceTier{Resolution: "768P", HasReferenceVideo: true, Price: &price})
					case "legacy_pair_reversed":
						rows = append([]VideoPriceTier{{Resolution: "768P", HasReferenceVideo: true, Price: &price}}, rows...)
					}
					multiplier, image, prepay, imageCount := 2., .15, .3, 7
					card := ChannelModelPricing{Platform: PlatformVideo, Models: []string{"video-model"}, BillingMode: mode, VideoPrices: rows,
						PriceMultiplier: &multiplier, VideoImageInputPricing: &VideoImageInputPricing{FreeImages: 5, Price: &image}}
					if mode == BillingModeVideoToken {
						card.VideoTokenPrepay = &VideoTokenPrepayConfig{PricePerSecond: &prepay}
					}
					require.NoError(t, validatePricingEntries([]ChannelModelPricing{card}))
					group := &Group{ID: 12, Platform: PlatformVideo, RateMultiplier: 9, VideoRateIndependent: true, VideoRateMultiplier: .5, ModelPricing: []ChannelModelPricing{card}}
					resolver := videoPricingTestResolver(group, nil)
					units, unit := 8., "second"
					if mode == BillingModeVideoToken {
						units, unit = .25, "million_tokens"
					} else if mode == BillingModeVideoPerRequest {
						units, unit = 1, "request"
					}
					for _, reference := range []bool{false, true} {
						quote, err := resolver.QuoteVideo(context.Background(), VideoPriceInput{Group: group, Model: "video-model", Resolution: "768P", HasReferenceVideo: reference, ReferenceImageCount: &imageCount, RateMultiplier: 9})
						require.NoError(t, err)
						require.Equal(t, unit, quote.Unit)
						require.Equal(t, price*2, quote.UnitPrice)
						tokens := int64(250_000)
						cost, err := quote.Calculate(8, &tokens)
						require.NoError(t, err)
						require.InDelta(t, units*price*2, cost.OutputCost, 1e-12)
						require.InDelta(t, .3, cost.ImageInputCost, 1e-12)
						require.InDelta(t, units*price+.3, cost.ActualCost, 1e-12)
						if mode == BillingModeVideoToken {
							require.Equal(t, .3, *quote.TokenPrepay.PricePerSecond)
						}
					}
					display := resolver.VideoDisplayPricing(context.Background(), group, "video-model")
					require.Len(t, display.VideoPrices, 1)
					require.Nil(t, display.VideoPrices[0].HasReferenceVideo)
					require.Equal(t, price, display.VideoPrices[0].Price)
					require.Equal(t, unit, display.VideoPrices[0].Unit)
				})
			}
		}
	}
}

// 新 API 配置不携带参考条件，历史任务 JSON 仍按自己的冻结单价结算。
func TestVideoResolutionPriceNewJSONAndLegacySnapshot(t *testing.T) {
	var tier VideoPriceTier
	require.NoError(t, json.Unmarshal([]byte(`{"resolution":"720p","price":0}`), &tier))
	raw, err := json.Marshal(tier)
	require.NoError(t, err)
	require.JSONEq(t, `{"resolution":"720p","price":0}`, string(raw))
	var historical VideoPriceQuote
	require.NoError(t, json.Unmarshal([]byte(`{"version":1,"mode":"video_token","source":"channel","model":"video-model","resolution":"720p","has_reference_video":true,"unit":"million_tokens","unit_price":46,"rate_multiplier":0.5}`), &historical))
	tokens := int64(1_000_000)
	cost, err := historical.Calculate(8, &tokens)
	require.NoError(t, err)
	require.Equal(t, 23., cost.ActualCost)
}

// 原按秒层级支持自定义分辨率，展示必须与收费相同，不能只枚举三个默认档位。
func TestVideoResolutionLegacySecondTierDisplay(t *testing.T) {
	price := .425
	group := &Group{ID: 12, Platform: PlatformVideo, RateMultiplier: 2, ModelPricing: []ChannelModelPricing{{Platform: PlatformVideo, Models: []string{"video-model"}, BillingMode: BillingModeVideo,
		Intervals: []PricingInterval{{TierLabel: "768p", PerRequestPrice: &price}}}}}
	resolver := videoPricingTestResolver(group, nil)
	display := resolver.VideoDisplayPricing(context.Background(), group, "video-model")
	require.Len(t, display.VideoPrices, 1)
	require.Equal(t, "768p", display.VideoPrices[0].Resolution)
	require.Equal(t, "second", display.VideoPrices[0].Unit)
	require.InDelta(t, .85, display.VideoPrices[0].Price, 1e-12)
}

// 含参考视频的新任务只按分辨率价预扣和结算，重复恢复不会重复扣款。
func TestVideoResolutionPriceLifecycleAndUsageTier(t *testing.T) {
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoToken, BillingModeVideoPerRequest} {
		t.Run(string(mode), func(t *testing.T) {
			s, key, upstream, billing, logs := newVideoLifecycleFixture(mode)
			upstream.selection.Metadata.HasReferenceVideo = true
			created, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model"}`), IdempotencyKey: "one"})
			require.NoError(t, err)
			tokens := int64(250_000)
			upstream.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Body: []byte(`{"status":"completed"}`), Metadata: VideoRequestMetadata{Resolution: "720p", DurationSeconds: 8, Tokens: &tokens}}
			task, err := s.repo.Get(context.Background(), created.LocalTaskID)
			require.NoError(t, err)
			// 受理后即使改价，也必须依原快照处理完成事件。
			changedPrice := 99.
			key.Group.ModelPricing[0].VideoPrices[0].Price = &changedPrice
			s.advance(context.Background(), task)
			require.Equal(t, "settled", task.BillingStatus)
			want := map[BillingMode]float64{BillingModeVideo: 16, BillingModeVideoToken: .5, BillingModeVideoPerRequest: 2}[mode]
			require.Equal(t, want, task.BillingResult.ActualAmountUSD)
			require.NotNil(t, logs.logs["video_capture:"+task.ID].BillingTier)
			require.Equal(t, "720p", *logs.logs["video_capture:"+task.ID].BillingTier)
			s.advance(context.Background(), task)
			require.Equal(t, 1, billing.captures)
			require.Equal(t, 1, upstream.submitted)
			require.Len(t, logs.logs, 1)
		})
	}
}
