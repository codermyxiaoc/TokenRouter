package service

import (
	"context"
	"strings"
	"unicode/utf8"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

const maxModelDescriptionLength = 2000

// @project-doc docs/interfaces/model_catalog_and_marketplace.md#marketplace_video_model_details
// validateModelDetails 只校验公开说明配置，不能把说明误当作有效价格或新增可请求模型。
func validateModelDetails(pricing []ChannelModelPricing) error {
	for i, p := range pricing {
		if len(p.ModelDetails) == 0 {
			continue
		}
		if p.Platform != PlatformVideo {
			return infraerrors.BadRequest("MODEL_DETAILS_UNSUPPORTED_PLATFORM", "model details require the video platform")
		}
		models := make(map[string]string, len(p.Models))
		for _, model := range p.Models {
			key := normalizeChannelPricingModelName(model)
			if key == "" {
				return infraerrors.BadRequest("INVALID_MODEL_DETAILS", "model details require non-empty models")
			}
			if _, exists := models[key]; exists {
				return infraerrors.BadRequest("INVALID_MODEL_DETAILS", "model details contain duplicate models")
			}
			models[key] = model
		}
		seen := make(map[string]struct{}, len(p.ModelDetails))
		canonical := make(map[string]ModelDetails, len(p.ModelDetails))
		for model, detail := range p.ModelDetails {
			key := normalizeChannelPricingModelName(model)
			if _, exists := models[key]; !exists || key == "" {
				return infraerrors.BadRequest("INVALID_MODEL_DETAILS", "model details keys must belong to the pricing entry models")
			}
			if _, exists := seen[key]; exists {
				return infraerrors.BadRequest("INVALID_MODEL_DETAILS", "model details contain duplicate model keys")
			}
			seen[key] = struct{}{}
			canonical[models[key]] = detail
			if !utf8.ValidString(detail.Description) || utf8.RuneCountInString(detail.Description) > maxModelDescriptionLength {
				return infraerrors.BadRequest("INVALID_MODEL_DETAILS", "model description must not exceed 2000 characters")
			}
		}
		// 键名保存为models中原值，避免编辑器因大小写或空白差异丢弃合法说明。
		pricing[i].ModelDetails = canonical
	}
	return nil
}

// matchModelDetails 保持价卡精确优先、通配其次的规则，不跨平台寻找同名模型。
func matchModelDetails(pricing []ChannelModelPricing, model string) (ModelDetails, bool) {
	entries := make([]ChannelModelPricing, 0, len(pricing))
	for _, entry := range pricing {
		if entry.Platform == PlatformVideo || entry.Platform == "" {
			entries = append(entries, entry)
		}
	}
	card := matchGroupModelPricingEntry(&Group{ModelPricing: entries}, model, true)
	if card == nil {
		return ModelDetails{}, false
	}
	normalized := normalizeChannelPricingModelName(model)
	var selected string
	for _, pattern := range card.Models {
		key := normalizeChannelPricingModelName(pattern)
		if key == normalized {
			selected = key
			break
		}
		if selected == "" && strings.HasSuffix(key, "*") && strings.HasPrefix(normalized, strings.TrimSuffix(key, "*")) {
			selected = key
		}
	}
	for key, detail := range card.ModelDetails {
		if normalizeChannelPricingModelName(key) == selected {
			return detail, true
		}
	}
	return ModelDetails{}, false
}

// marketplaceModelDescription 在当前分组内投影说明；明确关闭阻止渠道回退，公开文本不暴露配置键。
func (s *ModelMarketplaceService) marketplaceModelDescription(ctx context.Context, group *Group, model marketplaceModelDef) string {
	if group == nil || group.Platform != PlatformVideo {
		return ""
	}
	candidates := []string{model.ID}
	if !model.PricingAmbiguous && strings.TrimSpace(model.PricingModel) != "" && model.PricingModel != model.ID {
		candidates = append(candidates, model.PricingModel)
	}
	var channelService *ChannelService
	if s.gatewayService != nil {
		channelService = s.gatewayService.channelService
		if channelService == nil && s.gatewayService.resolver != nil {
			channelService = s.gatewayService.resolver.channelService
		}
	}
	if channelService != nil && !model.PricingAmbiguous {
		mapping := channelService.ResolveChannelMapping(ctx, group.ID, model.ID)
		if mapping.Mapped && strings.TrimSpace(mapping.MappedModel) != "" {
			candidates = append(candidates, mapping.MappedModel)
		}
	}
	// 只使用目录的明确身份候选和已经解析的映射，不从通用模糊模型价推测说明。
	for _, candidate := range candidates {
		if detail, exists := matchModelDetails(group.ModelPricing, candidate); exists {
			return publicModelDescription(detail)
		}
	}
	if channelService == nil {
		return ""
	}
	channel, err := channelService.GetChannelForGroup(ctx, group.ID)
	if err != nil || channel == nil {
		return ""
	}
	for _, candidate := range candidates {
		for _, identity := range append([]string{candidate}, buildModelLookupCandidates(candidate)...) {
			// 渠道必须显式声明Video，不能让其他平台的同名配置向视频模型泄漏。
			videoEntries := make([]ChannelModelPricing, 0, len(channel.ModelPricing))
			for _, entry := range channel.ModelPricing {
				if entry.Platform == PlatformVideo {
					videoEntries = append(videoEntries, entry)
				}
			}
			if detail, exists := matchModelDetails(videoEntries, identity); exists {
				return publicModelDescription(detail)
			}
		}
	}
	return ""
}

// publicModelDescription 二次限制公开投影，兼容旧数据中的禁用、空白或越界说明。
func publicModelDescription(detail ModelDetails) string {
	if !detail.Enabled || !utf8.ValidString(detail.Description) || utf8.RuneCountInString(detail.Description) > maxModelDescriptionLength {
		return ""
	}
	return strings.TrimSpace(detail.Description)
}

// IsModelDetailsOnly 只识别新增的纯说明行；任何收费字段（包括显式零价）都必须继续走原计费校验。
func (p ChannelModelPricing) IsModelDetailsOnly() bool {
	return p.Platform == PlatformVideo && len(p.ModelDetails) > 0 &&
		(p.BillingMode == "" || p.BillingMode == BillingModeToken || isVideoMatrixBillingMode(p.BillingMode)) &&
		p.PriceMultiplier == nil && p.FastModeMultiplier == nil && p.FastMultiplier == nil && p.FlexMultiplier == nil &&
		p.MaxReasoningEffortMultiplier == nil && len(p.ReasoningEffortMultipliers) == 0 &&
		p.InputPrice == nil && p.OutputPrice == nil && p.CacheWritePrice == nil && p.CacheWrite1hPrice == nil && p.CacheReadPrice == nil &&
		p.ImageInputPrice == nil && p.ImageOutputPrice == nil && p.PerRequestPrice == nil &&
		len(p.Intervals) == 0 && p.TimePricing == nil && len(p.VideoPrices) == 0 && p.VideoFallbackPrice == nil &&
		p.VideoTokenPrepay == nil && p.VideoImageInputPricing == nil
}
