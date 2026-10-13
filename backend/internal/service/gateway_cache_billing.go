package service

import (
	"context"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// recordUsageBillingModel 与用量入口保持相同的计费来源，不引入跨模型模糊回退。
func (s *GatewayService) recordUsageBillingModel(ctx context.Context, key *APIKey, result *ForwardResult, fields ChannelUsageFields) string {
	model := forwardResultBillingModel(result.Model, result.UpstreamModel)
	switch fields.BillingModelSource {
	case BillingModelSourceUpstream:
		if result.UpstreamModel != "" {
			model = result.UpstreamModel
		}
	case BillingModelSourceRequested:
		if fields.OriginalModel != "" {
			model = fields.OriginalModel
		}
	case BillingModelSourceChannelMapped:
		if fields.ChannelMappedModel != "" {
			model = fields.ChannelMappedModel
		}
	}
	return model
}

// cacheStoragePrice 按明确计费模型及实际模型寻找官方存储单价，无法报价时由缓存入口拒绝写入。
func (s *GatewayService) cacheStoragePrice(result *ForwardResult, model string) float64 {
	if s == nil || s.billingService == nil || result == nil {
		return 0
	}
	for _, name := range []string{model, result.UpstreamModel, result.Model} {
		if strings.TrimSpace(name) == "" {
			continue
		}
		if price := s.billingService.CacheStoragePricePerTokenHour(name); price > 0 {
			return price
		}
	}
	return 0
}
func (s *GatewayService) cacheStorageCost(result *ForwardResult, model string) float64 {
	if result == nil || result.CacheStorageTokenHours <= 0 {
		return 0
	}
	return result.CacheStorageTokenHours * s.cacheStoragePrice(result, model)
}
func isCacheStorageOnlyResult(result *ForwardResult) bool {
	if result == nil {
		return false
	}
	u := result.Usage
	return result.CacheStorageTokenHours > 0 && u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheCreationInputTokens == 0 && u.CacheReadInputTokens == 0 && u.ImageOutputTokens == 0 && result.ImageCount == 0 && result.AudioUsage == nil && result.SearchCount == 0
}
func addCacheStorageToAccountStatsCost(log *UsageLog, storageCost float64) {
	if log != nil && log.AccountStatsCost != nil {
		cost := *log.AccountStatsCost + storageCost
		log.AccountStatsCost = &cost
	}
}

// CacheStoragePriced 检查本次缓存是否具有可信的存储报价。
func (s *GatewayService) CacheStoragePriced(ctx context.Context, input *RecordUsageInput) bool {
	if input == nil || input.Result == nil {
		return false
	}
	return s.cacheStoragePrice(input.Result, s.recordUsageBillingModel(ctx, input.APIKey, input.Result, input.ChannelUsageFields)) > 0
}

// QuoteUsageCostBreakdown 使用本站订阅和余额定价规则计算预占报价，不记录使用或执行资金变动。
func (s *GatewayService) QuoteUsageCostBreakdown(ctx context.Context, input *RecordUsageInput) *CostBreakdown {
	if input == nil || input.APIKey == nil || input.User == nil || input.Account == nil || input.Result == nil {
		return nil
	}
	key, user := input.APIKey, input.User
	multiplier := 1.0
	if s.cfg != nil {
		multiplier = s.cfg.Default.RateMultiplier
	}
	subscription := resolveUsageSubscriptionForAPIKey(ctx, key, input.Subscription, s.userSubRepo, usageSubscriptionResolverFrom(s.usageBillingRepo), user.ID, key.GroupID)
	if key.GroupID != nil && key.Group != nil && subscription == nil {
		multiplier = s.getUserGroupRateMultiplier(ctx, user.ID, *key.GroupID, key.Group.RateMultiplier)
	} else {
		multiplier = resolveUsageRateMultiplier(ctx, user.ID, key.GroupID, key.Group, multiplier, subscription, nil)
	}
	at := input.PricingAt
	if at.IsZero() {
		at = timezone.Now()
		if s.usageBillingNow != nil {
			at = s.usageBillingNow()
		}
	}
	multiplier, imageMultiplier := computePeakAwareMultipliers(key, multiplier, at)
	model := s.recordUsageBillingModel(ctx, key, input.Result, input.ChannelUsageFields)
	requested := input.OriginalModel
	if requested == "" {
		requested = input.Result.Model
	}
	return s.calculateRecordUsageCost(ctx, input.Result, key, input.Account, model, requested, input.BillingModelSource, input.ChannelMappedModel, multiplier, imageMultiplier, &recordUsageOpts{PricingAt: at, RequirePricing: true})
}

// QuoteUsageCost 是只读估价入口，实际捕获仍由资金合同和数据库锁决定分配。
func (s *GatewayService) QuoteUsageCost(ctx context.Context, input *RecordUsageInput) float64 {
	cost := s.QuoteUsageCostBreakdown(ctx, input)
	if cost == nil {
		return 0
	}
	return cost.ActualCost
}
