package service

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildUsageVideoBillingDetailsHistoricalPrecisionAndFixedImages(t *testing.T) {
	count, newerCount, price := 7, 99, .15
	quote := &VideoPriceQuote{Mode: BillingModeVideo, Unit: "second", UnitPrice: .425, Resolution: "768p",
		HasReferenceVideo: true, ReferenceImageCount: &count, ImageInputPricing: &VideoImageInputPricing{FreeImages: 5, Price: &price},
		PriceMultiplier: 3, RateMultiplier: 9, Source: "internal-source", Model: "internal-model"}
	log := &UsageLog{ImageInputCost: .3, VideoDurationSeconds: usageVideoTestPtr(3)}
	details := BuildUsageVideoBillingDetails(log, quote, VideoRequestMetadata{DurationSeconds: 2.5, ReferenceImageCount: &newerCount})
	require.NotNil(t, details)
	require.Equal(t, 2.5, details.DurationSeconds, "不能用使用记录向上取整的时长覆盖实际秒数")
	require.Equal(t, .425, details.UnitPrice)
	require.Equal(t, 7, *details.ReferenceImageCount)
	require.Equal(t, 5, *details.ReferenceImageFreeCount)
	require.Equal(t, 2, *details.BillableReferenceImageCount)
	require.Equal(t, .15, *details.ReferenceImageUnitPrice)
	require.Equal(t, .3, *details.ReferenceImageCost, "图片费保持已记录的固定金额，不叠加任何倍率")
	raw, err := json.Marshal(details)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "internal-")
	require.NotContains(t, string(raw), "multiplier")
	// 输出必须复制值，不能让展示修改回写到历史价格对象。
	*details.ReferenceImageCount = 20
	require.Equal(t, 7, *quote.ReferenceImageCount)
}

func TestBuildUsageVideoBillingDetailsUnknownAndExplicitZero(t *testing.T) {
	quote := &VideoPriceQuote{Mode: BillingModeVideoToken, Unit: "million_tokens", UnitPrice: 0, Resolution: "480p"}
	for _, count := range []*int{nil, usageVideoTestPtr(0), usageVideoTestPtr(3)} {
		details := BuildUsageVideoBillingDetails(&UsageLog{}, quote, VideoRequestMetadata{ReferenceImageCount: count, Tokens: usageVideoTestPtr(int64(0))})
		require.NotNil(t, details)
		require.Equal(t, count, details.ReferenceImageCount)
		require.Zero(t, *details.Tokens)
		require.Nil(t, details.ReferenceImageFreeCount)
		require.Nil(t, details.ReferenceImageCost)
	}
	quote.ReferenceImageCount = usageVideoTestPtr(0)
	quote.ImageInputPricing = &VideoImageInputPricing{FreeImages: 0, Price: usageVideoTestPtr(0.0)}
	details := BuildUsageVideoBillingDetails(&UsageLog{}, quote, VideoRequestMetadata{})
	require.NotNil(t, details.ReferenceImageFreeCount)
	require.Zero(t, *details.ReferenceImageFreeCount)
	require.NotNil(t, details.ReferenceImageCost)
	require.Zero(t, *details.ReferenceImageCost)
}

func TestBuildUsageVideoBillingDetailsRejectsInvalidSnapshots(t *testing.T) {
	valid := VideoPriceQuote{Mode: BillingModeVideo, Unit: "second", UnitPrice: .3}
	require.Nil(t, BuildUsageVideoBillingDetails(&UsageLog{}, nil, VideoRequestMetadata{}))
	require.Nil(t, BuildUsageVideoBillingDetails(nil, &valid, VideoRequestMetadata{}))
	for _, value := range []float64{-1, math.NaN(), math.Inf(1)} {
		quote := valid
		quote.UnitPrice = value
		require.Nil(t, BuildUsageVideoBillingDetails(&UsageLog{}, &quote, VideoRequestMetadata{}))
		require.Nil(t, BuildUsageVideoBillingDetails(&UsageLog{}, &valid, VideoRequestMetadata{DurationSeconds: value}))
	}
	quote := valid
	quote.Unit = "request"
	require.Nil(t, BuildUsageVideoBillingDetails(&UsageLog{}, &quote, VideoRequestMetadata{}))
	quote = valid
	quote.ImageInputPricing = &VideoImageInputPricing{FreeImages: 5, Price: usageVideoTestPtr(.15)}
	details := BuildUsageVideoBillingDetails(&UsageLog{ImageInputCost: .3}, &quote, VideoRequestMetadata{ReferenceImageCount: usageVideoTestPtr(7)})
	require.Equal(t, 7, *details.ReferenceImageCount)
	require.Nil(t, details.ReferenceImageCost, "旧价格快照缺少冻结计数时不能用轮询元数据补算图片费")
}

func usageVideoTestPtr[T any](value T) *T { return &value }
