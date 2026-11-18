package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 模型广场公开 12 小时缓存写入价：已乘分组倍率；未单独定价时为 nil（不展示），显式 0 保留为 0。
func TestCacheWrite12hDisplayPricing(t *testing.T) {
	price := func(v float64) *float64 { return &v }

	t.Run("default card with fast tier", func(t *testing.T) {
		pricing := &ModelPricing{
			InputPricePerToken:            1e-6,
			OutputPricePerToken:           4e-6,
			CacheCreationPricePerToken:    1.25e-6,
			CacheCreation12hPricePerToken: price(2.5e-6),
			SupportsServiceTier:           true,
		}
		display := buildTokenDisplayPricing(pricing, 2)
		require.NotNil(t, display.CacheWrite12hPricePerToken)
		require.InDelta(t, 5e-6, *display.CacheWrite12hPricePerToken, 1e-15)
		// Fast 档按与结算相同的 2 倍换算。
		require.NotNil(t, display.FastCacheWrite12hPricePerToken)
		require.InDelta(t, 10e-6, *display.FastCacheWrite12hPricePerToken, 1e-15)
	})

	t.Run("priority price ratio matches settlement", func(t *testing.T) {
		pricing := &ModelPricing{
			InputPricePerToken:                 1e-6,
			InputPricePerTokenPriority:         2.5e-6,
			CacheCreationPricePerToken:         1e-6,
			CacheCreationPricePerTokenPriority: 2.5e-6,
			CacheCreation12hPricePerToken:      price(2e-6),
		}
		display := buildTokenDisplayPricing(pricing, 1)
		require.NotNil(t, display.FastCacheWrite12hPricePerToken)
		require.InDelta(t, 5e-6, *display.FastCacheWrite12hPricePerToken, 1e-15)
		// 展示价与结算价一致：12h 写入 1 token 的 priority 成本。
		svc := &BillingService{}
		cost := svc.computeTokenBreakdown(pricing, UsageTokens{CacheCreationTokens: 1, CacheCreation12hTokens: 1}, 1, OpenAIFastTierPriority, false)
		require.InDelta(t, *display.FastCacheWrite12hPricePerToken, cost.CacheCreationCost, 1e-15)
	})

	t.Run("explicit zero and unset", func(t *testing.T) {
		zero := buildTokenDisplayPricing(&ModelPricing{InputPricePerToken: 1e-6, CacheCreation12hPricePerToken: price(0)}, 1)
		require.NotNil(t, zero.CacheWrite12hPricePerToken)
		require.Zero(t, *zero.CacheWrite12hPricePerToken)
		unset := buildTokenDisplayPricing(&ModelPricing{InputPricePerToken: 1e-6}, 1)
		require.Nil(t, unset.CacheWrite12hPricePerToken)
		require.Nil(t, unset.FastCacheWrite12hPricePerToken)
	})

	t.Run("long context intervals", func(t *testing.T) {
		pricing := &ModelPricing{
			InputPricePerToken:            1e-6,
			OutputPricePerToken:           4e-6,
			CacheCreation12hPricePerToken: price(2.5e-6),
			LongContextInputThreshold:     272000,
			LongContextInputMultiplier:    2,
			LongContextOutputMultiplier:   1.5,
		}
		display := buildTokenDisplayPricing(pricing, 1)
		require.Len(t, display.ContextIntervals, 2)
		require.InDelta(t, 2.5e-6, *display.ContextIntervals[0].CacheWrite12hPricePerToken, 1e-15)
		// 长上下文区间与结算一致，12h 写入属于输入侧，按输入倍率放大。
		require.InDelta(t, 5e-6, *display.ContextIntervals[1].CacheWrite12hPricePerToken, 1e-15)
	})

	t.Run("channel price multiplier scales 12h price", func(t *testing.T) {
		scaled := multiplyModelPricing(&ModelPricing{InputPricePerToken: 1e-6, CacheCreation12hPricePerToken: price(2e-6)}, 1.5)
		require.InDelta(t, 3e-6, *scaled.CacheCreation12hPricePerToken, 1e-15)
		// 缩放不得修改原价卡指针。
		original := price(2e-6)
		_ = multiplyModelPricing(&ModelPricing{CacheCreation12hPricePerToken: original}, 3)
		require.InDelta(t, 2e-6, *original, 1e-15)
	})
}
