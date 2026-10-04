package service

import "strings"

// usageBillingAllocationRates 保存两种资金来源各自的倍率，避免展示用订阅价格污染余额结算。
type usageBillingAllocationRates struct {
	SubscriptionRateMultiplier      float64
	SubscriptionRateMultiplierScale float64
	BalanceRateMultiplier           float64
	DisablePlanGroupRateMultiplier  bool
}

// resolveUsageBillingAllocationRates 按最终计费模式解析资金倍率。
// mediaMode 标记实际媒体类型，令牌计费仍沿用令牌及高峰规则；图片/视频按次或按秒不叠加高峰。
func resolveUsageBillingAllocationRates(apiKey *APIKey, billingMode string, mediaMode BillingMode, subscriptionBase, balanceBase, peakScale float64) usageBillingAllocationRates {
	if peakScale < 0 {
		peakScale = 1
	}
	rates := usageBillingAllocationRates{
		SubscriptionRateMultiplier:      subscriptionBase * peakScale,
		SubscriptionRateMultiplierScale: peakScale,
		BalanceRateMultiplier:           balanceBase * peakScale,
		// 免费高峰不能被仓储的缺省缩放回退重新变成套餐收费。
		DisablePlanGroupRateMultiplier: peakScale == 0,
	}
	if strings.TrimSpace(billingMode) == string(BillingModeToken) {
		return rates
	}
	switch mediaMode {
	case BillingModeImage:
		rates.SubscriptionRateMultiplier = resolveImageRateMultiplier(apiKey, subscriptionBase)
		rates.BalanceRateMultiplier = resolveImageRateMultiplier(apiKey, balanceBase)
		rates.SubscriptionRateMultiplierScale = 1
		rates.DisablePlanGroupRateMultiplier = apiKey != nil && apiKey.Group != nil && apiKey.Group.ImageRateIndependent
	case BillingModeVideo:
		rates.SubscriptionRateMultiplier = resolveVideoRateMultiplier(apiKey, subscriptionBase)
		rates.BalanceRateMultiplier = resolveVideoRateMultiplier(apiKey, balanceBase)
		rates.SubscriptionRateMultiplierScale = 1
		rates.DisablePlanGroupRateMultiplier = apiKey != nil && apiKey.Group != nil && apiKey.Group.VideoRateIndependent
	case BillingModePerRequest:
		// 独立搜索和 OpenAI 音频使用无高峰的普通资金倍率，不套用图片独立倍率。
		rates.SubscriptionRateMultiplier = subscriptionBase
		rates.BalanceRateMultiplier = balanceBase
		rates.SubscriptionRateMultiplierScale = 1
		rates.DisablePlanGroupRateMultiplier = false
	}
	return rates
}
