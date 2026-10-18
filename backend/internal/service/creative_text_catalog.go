package service

import (
	"context"
	"strings"
)

// 文本仅开放已经有正式 Chat Completions 路由的平台，独立视频及 SystemOne 不借道。
func creativeTextPlatformSupported(platform string) bool {
	switch platform {
	case PlatformOpenAI, PlatformGrok, PlatformAnthropic, PlatformGemini, PlatformAntigravity, PlatformQoder, PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax, PlatformOpenCodeGo, PlatformCline, PlatformCommandCode:
		return true
	default:
		return false
	}
}

func creativeDefaultTextCapabilities() *CreativeTextCapabilities {
	return &CreativeTextCapabilities{MaxInputChars: 8000, MaxOutputTokens: 4096, DefaultMaxOutputTokens: 1024, MaxReferenceImages: 0}
}

// 明确的媒体/向量模型不得因为管理员改展示类型而进入文本执行与 Token 结算。
func (s *CreativePublicService) creativeTextModelAllowed(model string) bool {
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" || IsMidjourneyImageGenerationModel(name) {
		return false
	}
	for _, marker := range []string{"image", "imagine", "video", "audio", "speech", "tts", "stt", "voice", "realtime", "embed", "rerank", "systemone", "jev"} {
		if strings.Contains(name, marker) {
			return false
		}
	}
	if s.Pricing != nil && s.Pricing.pricingService != nil {
		_, out := s.Pricing.pricingService.GetModelModalities(model)
		if len(out) > 0 {
			for _, modality := range out {
				if modality == "text" {
					return true
				}
			}
			return false
		}
	}
	return true
}

func (s *CreativePublicService) creativeTextCandidatesForGroup(ctx context.Context, group *Group) ([]CreativeModelCandidate, error) {
	out := []CreativeModelCandidate{}
	if s == nil || s.TextCatalog == nil || s.AccountRepo == nil || group == nil || !group.IsActive() || !creativeTextPlatformSupported(group.Platform) || !group.AllowsClientProtocol(GroupClientProtocolOpenAIChatCompletions) || s.Config != nil && s.Config.RunMode == "simple" {
		return out, nil
	}
	accounts, err := s.AccountRepo.ListSchedulableByGroupIDAndPlatform(ctx, group.ID, group.Platform)
	if err != nil {
		return nil, err
	}
	base := s.TextCatalog.GetAvailableModels(ctx, &group.ID, group.Platform)
	resolved := s.TextCatalog.resolveRequestableModelsWithAccounts(ctx, &group.ID, group.Platform, base, accounts)
	for _, model := range resolved.Models {
		// 未知价与非 Token 合同不能借文本标签免费放行；显式零 Token 价仍然有效。
		if model.PricingAmbiguous || s.PricingResolver == nil {
			continue
		}
		pricingModel := model.PricingModel
		if pricingModel == "" {
			pricingModel = model.ID
		}
		pricing := s.PricingResolver.Resolve(ctx, PricingInput{Model: pricingModel, GroupID: &group.ID, Group: group})
		if pricing == nil || pricing.Mode != BillingModeToken || pricing.BasePricing == nil {
			continue
		}
		if !pricing.HasEffectiveOverridePricing() && !hasAnyDisplayTokenPricing(pricing.BasePricing) {
			// 空 Token 价卡不能把未知模型变成免费；真实目录中的零价和显式零覆盖仍有效。
			if s.Pricing == nil {
				continue
			}
			if _, err := s.Pricing.GetModelPricing(pricingModel); err != nil {
				continue
			}
		}
		routed := model.ID
		if s.TextCatalog.channelService != nil {
			mapping := s.TextCatalog.channelService.ResolveChannelMapping(ctx, group.ID, routed)
			if mapping.Mapped {
				routed = mapping.MappedModel
			}
		}
		if !s.creativeTextModelAllowed(routed) {
			continue
		}
		eligible := false
		mixedMedia := false
		for i := range accounts {
			account := &accounts[i]
			if !account.IsSchedulable() || !account.IsModelSupported(routed) {
				continue
			}
			if (group.Platform == PlatformOpenAI || group.Platform == PlatformGrok) && !account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityTextGeneration) {
				continue
			}
			if !s.creativeTextModelAllowed(resolveAccountUpstreamModel(ctx, account, routed)) {
				mixedMedia = true
				break
			}
			eligible = true
		}
		if eligible && !mixedMedia {
			out = append(out, CreativeModelCandidate{GroupID: group.ID, GroupName: group.Name, Platform: group.Platform, Model: model.ID, MediaType: "text", Operations: []string{CreativeOperationGenerate}})
		}
	}
	return out, nil
}

func (s *CreativePublicService) creativeTextModelsForGroup(ctx context.Context, group *Group, settings []CreativeModelSetting) ([]CreativeModelPublic, error) {
	out := []CreativeModelPublic{}
	configured := map[string]bool{}
	for _, setting := range settings {
		if setting.GroupID == group.ID && setting.MediaType == "text" && containsCreativeOperation(setting.Operations, CreativeOperationGenerate) {
			configured[setting.Model] = true
		}
	}
	if len(configured) == 0 {
		return out, nil
	}
	candidates, err := s.creativeTextCandidatesForGroup(ctx, group)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if configured[candidate.Model] {
			out = append(out, CreativeModelPublic{GroupID: group.ID, GroupName: group.Name, Model: candidate.Model, MediaType: "text", Operations: []string{CreativeOperationGenerate}, TextCapabilities: creativeDefaultTextCapabilities(), OutputCount: 1, MaxOutputCount: 1})
		}
	}
	return out, nil
}
