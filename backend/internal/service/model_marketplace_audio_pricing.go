package service

import (
	"context"
	"math"
	"regexp"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/xai"
)

// TTS/STT 的数字版本名称仍调用不带 model 的同一 Voice 端点；其它后缀不推测身份。
var marketplaceGrokTTSFamily = regexp.MustCompile(`^grok-(?:voice-)?tts(?:-(?:[0-9]+(?:[.-][0-9]+)*|latest))?$`)
var marketplaceGrokSTTFamily = regexp.MustCompile(`^grok-(?:voice-)?stt(?:-(?:[0-9]+(?:[.-][0-9]+)*|latest))?$`)

// marketplaceGrokAudioIdentity 只在 Grok 分组内将明确语音身份映射到真实结算单位。
func marketplaceGrokAudioIdentity(model string) (mode, billingModel, unit string) {
	normalized := strings.ToLower(strings.TrimSpace(xai.StripGrokProviderPrefix(model)))
	switch {
	case normalized == "tts" || marketplaceGrokTTSFamily.MatchString(normalized):
		return "tts", "tts", "million_characters"
	case normalized == "stt" || marketplaceGrokSTTFamily.MatchString(normalized):
		return "stt", "stt", "hour"
	case normalized == "grok-voice-latest" || normalized == "grok-voice-think-fast-1.0" || normalized == "grok-voice-think-fast-2.0":
		// Realtime 的正式请求确实携带模型，逐模型价卡不能合并成一个虚构端点价格。
		return "realtime", strings.TrimSpace(model), "minute"
	default:
		return "", "", ""
	}
}

func marketplaceAudioModalities(mode string) ([]string, []string) {
	switch mode {
	case "tts":
		return []string{"text"}, []string{"audio"}
	case "stt":
		return []string{"audio"}, []string{"text"}
	default:
		return []string{"text", "audio"}, []string{"text", "audio"}
	}
}

// getPublicAudioDisplayPricing 只投影现有 Voice 的真实价格，不改变请求、资金或恢复流程。
// @project-doc docs/interfaces/model_catalog_and_marketplace.md#marketplace_audio_pricing
func (s *ModelMarketplaceService) getPublicAudioDisplayPricing(ctx context.Context, group *Group, mode, billingModel, unit string) ModelDisplayPricing {
	multiplier := group.RateMultiplier
	if !validMarketplaceAudioPrice(multiplier) {
		return unknownDisplayPricing()
	}
	resolver := NewModelPricingResolver(nil, s.billingService)
	if s.gatewayService != nil && s.gatewayService.resolver != nil {
		resolver = s.gatewayService.resolver
	}
	resolved := resolver.Resolve(ctx, PricingInput{Model: billingModel, GroupID: &group.ID, Group: group})
	var cost *CostBreakdown
	if resolved != nil && (resolved.Source == PricingSourceGroup || resolved.Source == PricingSourceChannel) && resolved.Mode == BillingModePerRequest {
		// 在零倍率缩放前校验所选原价，避免历史非法负价经乘零后变成看似免费。
		rawPricing := matchGroupModelPricing(group, billingModel)
		if resolved.Source == PricingSourceChannel {
			rawPricing = resolver.lookupChannelPricingNormalized(ctx, group.ID, billingModel)
		}
		if !marketplaceAudioConfiguredPriceValid(rawPricing, mode) {
			return unknownDisplayPricing()
		}
		// 未配置默认价且没有匹配层级的空价卡不能伪装为免费。
		_, tierFound := resolver.GetRequestTierPriceValue(resolved, mode)
		if !tierFound {
			_, tierFound = resolver.GetRequestTierPriceByContextValue(resolved, 0)
		}
		if !tierFound && (resolved.channelPricing == nil || resolved.channelPricing.PerRequestPrice == nil) {
			return unknownDisplayPricing()
		}
		var err error
		cost, err = s.billingService.CalculateCostUnified(CostInput{Ctx: ctx, Model: billingModel, GroupID: &group.ID, Group: group, UsageUnits: 1, SizeTier: mode, RateMultiplier: multiplier, Resolver: resolver, Resolved: resolved})
		if err != nil {
			return unknownDisplayPricing()
		}
	} else {
		cfg := groupAudioPriceConfigFromAPIKey(&APIKey{Group: group})
		var configured *float64
		switch mode {
		case "tts":
			configured = cfg.TTSPerMChars
		case "stt":
			configured = cfg.STTPerHour
		case "realtime":
			configured = cfg.RealtimePerMin
		}
		if configured != nil && !validMarketplaceAudioPrice(*configured) {
			return unknownDisplayPricing()
		}
		cost = s.billingService.CalculateAudioCost(mode, 1, cfg, multiplier)
	}
	if cost == nil || !validMarketplaceAudioPrice(cost.TotalCost) || !validMarketplaceAudioPrice(cost.ActualCost) {
		return unknownDisplayPricing()
	}
	return ModelDisplayPricing{PricingMode: "audio", PriceStatus: "priced", AudioPrice: &ModelDisplayAudioPrice{Mode: mode, Price: cost.ActualCost, Unit: unit}}
}

func validMarketplaceAudioPrice(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// marketplaceAudioConfiguredPriceValid 与连续单位计价使用相同层级选择，仅拒绝非法或空价格。
func marketplaceAudioConfiguredPriceValid(pricing *ChannelModelPricing, mode string) bool {
	if pricing == nil || pricing.PriceMultiplier != nil && !validMarketplaceAudioPrice(*pricing.PriceMultiplier) {
		return false
	}
	for _, tier := range pricing.Intervals {
		if strings.EqualFold(tier.TierLabel, mode) && tier.PerRequestPrice != nil {
			return validMarketplaceAudioPrice(*tier.PerRequestPrice)
		}
	}
	if tier := FindMatchingInterval(pricing.Intervals, 0); tier != nil && tier.PerRequestPrice != nil {
		return validMarketplaceAudioPrice(*tier.PerRequestPrice)
	}
	return pricing.PerRequestPrice != nil && validMarketplaceAudioPrice(*pricing.PerRequestPrice)
}
