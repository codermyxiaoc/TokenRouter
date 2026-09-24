package service

import (
	"math"
	"strings"
)

// cloneReasoningEffortMultipliers 保证解析或复制价卡时不会污染缓存中的共享映射。
func cloneReasoningEffortMultipliers(source map[string]float64) map[string]float64 {
	if source == nil {
		return nil
	}
	result := make(map[string]float64, len(source))
	for effort, multiplier := range source {
		result[effort] = multiplier
	}
	return result
}

// reasoningEffortBillingMultiplier 优先应用显式档位；未配置时完全沿用旧 Max 与模型默认，避免升级改价。
func reasoningEffortBillingMultiplier(model, effort string, pricing *ModelPricing) float64 {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort != "none" {
		effort = NormalizeMaxReasoningEffort(effort)
	}
	if pricing != nil {
		if value, ok := pricing.ReasoningEffortMultipliers[effort]; ok && value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
			return value
		}
	}
	return maxReasoningEffortBillingMultiplier(model, effort, pricing)
}

// displayReasoningEffortMultipliers 把生效的旧 Max 回退并入展示快照，避免公开价格遗漏加价。
func displayReasoningEffortMultipliers(pricing *ModelPricing) map[string]float64 {
	if pricing == nil {
		return nil
	}
	result := cloneReasoningEffortMultipliers(pricing.ReasoningEffortMultipliers)
	if pricing.MaxReasoningEffortMultiplier != nil {
		if result == nil {
			result = make(map[string]float64)
		}
		if _, configured := result["max"]; !configured {
			result["max"] = *pricing.MaxReasoningEffortMultiplier
		}
	}
	return result
}
