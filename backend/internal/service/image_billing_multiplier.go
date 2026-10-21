package service

// resolveImageRateMultiplier 返回分组媒体默认倍率，余额消费和公开报价沿用此口径。
func resolveImageRateMultiplier(apiKey *APIKey, effectiveGroupMultiplier float64) float64 {
	if apiKey != nil && apiKey.Group != nil && apiKey.Group.ImageRateIndependent {
		if apiKey.Group.ImageRateMultiplier < 0 {
			return 0
		}
		return apiKey.Group.ImageRateMultiplier
	}
	return effectiveGroupMultiplier
}

// resolveImageSubscriptionRateMultiplier 只在显式开启优先级且套餐配置有效覆盖时替换图片独立倍率。
// @project-doc docs/domains/routing_and_billing.md#media_subscription_rate_priority
func resolveImageSubscriptionRateMultiplier(apiKey *APIKey, subscription *UserSubscription, effectiveGroupMultiplier float64) float64 {
	multiplier := resolveImageRateMultiplier(apiKey, effectiveGroupMultiplier)
	if apiKey != nil && apiKey.Group != nil && apiKey.Group.ImageRateIndependent && apiKey.Group.ImageSubscriptionRatePriority {
		return resolveExplicitMediaSubscriptionRate(apiKey, subscription, multiplier)
	}
	return multiplier
}

// resolveVideoSubscriptionRateMultiplier 与图片使用同一资金优先级，未设置套餐倍率时仍回退视频独立倍率。
func resolveVideoSubscriptionRateMultiplier(apiKey *APIKey, subscription *UserSubscription, effectiveGroupMultiplier float64) float64 {
	multiplier := resolveVideoRateMultiplier(apiKey, effectiveGroupMultiplier)
	if apiKey != nil && apiKey.Group != nil && apiKey.Group.VideoRateIndependent && apiKey.Group.VideoSubscriptionRatePriority {
		return resolveExplicitMediaSubscriptionRate(apiKey, subscription, multiplier)
	}
	return multiplier
}

// resolveExplicitMediaSubscriptionRate 不把普通分组倍率或其它分组的套餐配置当作显式覆盖。
func resolveExplicitMediaSubscriptionRate(apiKey *APIKey, subscription *UserSubscription, fallback float64) float64 {
	if subscription == nil || APIKeyEffectiveBillingMode(apiKey) == APIKeyBillingModeBalance {
		return fallback
	}
	groupID := apiKey.Group.ID
	if apiKey.GroupID != nil {
		groupID = *apiKey.GroupID
	}
	if multiplier, ok := subscriptionPlanGroupRateMultiplier(subscription.Plan, groupID); ok {
		return multiplier
	}
	return fallback
}

// resolveVideoRateMultiplier 按分组配置选择视频独立倍率或共享有效倍率。
func resolveVideoRateMultiplier(apiKey *APIKey, effectiveGroupMultiplier float64) float64 {
	if apiKey != nil && apiKey.Group != nil && apiKey.Group.VideoRateIndependent {
		if apiKey.Group.VideoRateMultiplier < 0 {
			return 0
		}
		return apiKey.Group.VideoRateMultiplier
	}
	return effectiveGroupMultiplier
}
