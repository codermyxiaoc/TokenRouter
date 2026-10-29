package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// CreativeVideoEndpointGrok 只属于创作台，不能作为独立 Video 账号的协议配置。
const CreativeVideoEndpointGrok VideoEndpoint = "grok"

// CreativeVideoEndpointUnified 标记模型绑定了声明式模板协议；画布统一提交规范请求，不对应任何内置端点。
const CreativeVideoEndpointUnified VideoEndpoint = "unified"

// Grok 继续复用原计费口径，只允许能按原值计费的默认参数，不能静默降档或截断时长。
func normalizeCreativeGrokVideoCapabilities(input *CreativeVideoCapabilities) (*CreativeVideoCapabilities, error) {
	out, err := normalizeCreativeVideoCapabilities(CreativeVideoEndpointGrok, input)
	if err != nil {
		return nil, err
	}
	for _, duration := range out.Durations {
		if duration < VideoBillingMinDurationSeconds || duration > VideoBillingMaxDurationSeconds {
			return nil, fmt.Errorf("creative grok video duration must be between 1 and 15 seconds")
		}
	}
	for _, resolution := range out.Resolutions {
		canonical, known := LookupVideoBillingResolution(resolution)
		if !known || canonical != resolution {
			return nil, fmt.Errorf("creative grok video resolution must be 480p, 720p or 1080p")
		}
	}
	return out, nil
}

// Grok 的运行参数范围与现有账本一致；独立 Video 的自定义分辨率和 1–60 秒能力保持不变。
func creativeGrokVideoCanvasCapabilities(input *CreativeVideoCapabilities) *CreativeVideoCapabilities {
	if input == nil {
		return nil
	}
	out := *input
	out.AllowCustomResolution, out.AllowCustomAspectRatio = false, false
	out.Durations = make([]float64, VideoBillingMaxDurationSeconds-VideoBillingMinDurationSeconds+1)
	for i := range out.Durations {
		out.Durations[i] = float64(i + VideoBillingMinDurationSeconds)
	}
	if len(input.Durations) > 0 {
		out.DefaultDuration = input.Durations[0]
		for _, duration := range input.Durations[1:] {
			if duration < out.DefaultDuration {
				out.DefaultDuration = duration
			}
		}
	}
	if len(input.Resolutions) > 0 {
		out.DefaultResolution = input.Resolutions[0]
	}
	if len(input.AspectRatios) > 0 {
		out.DefaultAspectRatio = input.AspectRatios[0]
	}
	out.Resolutions = []string{VideoBillingResolution480P, VideoBillingResolution720P, VideoBillingResolution1080P}
	out.AspectRatios = []string{"1:1", "3:4", "4:3", "16:9", "9:16", "21:9", "adaptive"}
	for _, ratio := range input.AspectRatios {
		if !containsCreativeOperation(out.AspectRatios, ratio) {
			out.AspectRatios = append(out.AspectRatios, ratio)
		}
	}
	return &out
}

// Grok 视频复用正式目录的请求模型、渠道限制和计费身份，不按展示别名猜测媒体能力。
// 目录扫描只读已知媒体资格，不触发账号探测、并发占位或上游生成。
// @project-doc docs/interfaces/cloud_canvas.md#video_model_settings
func (s *CreativePublicService) creativeGrokVideoCandidatesForGroup(ctx context.Context, group *Group) ([]CreativeModelCandidate, error) {
	out := []CreativeModelCandidate{}
	if s == nil || s.AccountRepo == nil || s.TextCatalog == nil || s.Pricing == nil || group == nil ||
		group.Platform != PlatformGrok || !group.IsActive() || !GroupAllowsImageGeneration(group) {
		return out, nil
	}
	accounts, err := s.AccountRepo.ListSchedulableByGroupIDAndPlatform(ctx, group.ID, PlatformGrok)
	if err != nil {
		return nil, err
	}
	eligible := make([]Account, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if account.Platform != PlatformGrok || (account.Type != AccountTypeAPIKey && account.Type != AccountTypeOAuth) ||
			!account.IsSchedulable() || !openAIStickyAccountMatchesGroup(account, &group.ID) {
			continue
		}
		if allowed, _ := account.GrokMediaGenerationEligibility(); allowed {
			eligible = append(eligible, *account)
		}
	}
	if len(eligible) == 0 {
		return out, nil
	}
	var channel *Channel
	if s.TextCatalog.channelService != nil {
		channel, err = s.TextCatalog.channelService.GetChannelForGroup(ctx, group.ID)
		if err != nil {
			// 渠道读取失败时不能绕过已配置的模型白名单。
			return nil, err
		}
	}
	base := configuredRequestModelsFromAccounts(eligible, PlatformGrok)
	models := s.TextCatalog.resolveRequestableModelsWithAccounts(ctx, &group.ID, PlatformGrok, base, eligible)
	marketplace := &ModelMarketplaceService{gatewayService: &GatewayService{resolver: s.PricingResolver}, billingService: s.Pricing}
	for _, model := range models.Models {
		if model.PricingAmbiguous || strings.ContainsAny(model.ID, "*?") {
			continue
		}
		routed := model.ID
		if channel != nil {
			if mapped := s.TextCatalog.channelService.ResolveChannelMapping(ctx, group.ID, routed).MappedModel; strings.TrimSpace(mapped) != "" {
				routed = mapped
			}
		}
		video, mixed := false, false
		for i := range eligible {
			account := &eligible[i]
			if _, ok := s.TextCatalog.resolveRequestableModel(ctx, &group.ID, channel, []Account{*account}, model.ID); !ok {
				continue
			}
			for _, final := range s.TextCatalog.resolveAccountUpstreamModelsForListing(ctx, account, routed) {
				if CanonicalGrokImagineVideoPriceFamily(final) == "" {
					mixed = true
				} else {
					video = true
				}
			}
		}
		if !video || mixed {
			continue
		}
		pricingModel := model.PricingModel
		if pricingModel == "" {
			pricingModel = model.ID
		}
		// 沿用正式 Grok 秒价/历史按次价及默认价，不借用独立 Video 的价卡合同。
		// 显式零价是有效合同；非法或歧义价格不能作为免费模型放行。
		pricing := marketplace.getPublicVideoDisplayPricing(ctx, group, pricingModel)
		if pricing.PriceStatus != "priced" || len(pricing.VideoPrices) == 0 {
			continue
		}
		out = append(out, CreativeModelCandidate{GroupID: group.ID, GroupName: group.Name, Platform: PlatformGrok,
			Model: model.ID, MediaType: "video", Operations: []string{CreativeOperationGenerate},
			VideoEndpoint: CreativeVideoEndpointGrok, VideoEndpoints: []VideoEndpoint{CreativeVideoEndpointGrok},
			VideoCapabilities: defaultCreativeVideoCapabilities(CreativeVideoEndpointGrok)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out, nil
}

// 运行目录继续要求管理员逐组启用，并保留既有参数派生与单张参考图边界。
func (s *CreativePublicService) creativeGrokVideoModelsForGroup(ctx context.Context, group *Group, settings []CreativeModelSetting) ([]CreativeModelPublic, error) {
	out := []CreativeModelPublic{}
	if group == nil {
		return out, nil
	}
	configured := map[string]CreativeModelSetting{}
	for _, setting := range settings {
		if setting.GroupID == group.ID && setting.MediaType == "video" && setting.VideoEndpoint == CreativeVideoEndpointGrok &&
			containsCreativeOperation(setting.Operations, CreativeOperationGenerate) {
			configured[setting.Model] = setting
		}
	}
	if len(configured) == 0 {
		return out, nil
	}
	candidates, err := s.creativeGrokVideoCandidatesForGroup(ctx, group)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		setting, ok := configured[candidate.Model]
		if !ok {
			continue
		}
		caps, err := normalizeCreativeGrokVideoCapabilities(setting.VideoCapabilities)
		if err != nil {
			continue
		}
		caps = creativeGrokVideoCanvasCapabilities(caps)
		out = append(out, CreativeModelPublic{GroupID: group.ID, GroupName: group.Name, Model: candidate.Model,
			MediaType: "video", UsageNotes: setting.UsageNotes, Operations: []string{CreativeOperationGenerate}, VideoEndpoint: CreativeVideoEndpointGrok,
			VideoCapabilities: caps, AspectRatios: caps.AspectRatios, OutputCount: 1, MaxOutputCount: 1, MaxReferenceImages: caps.MaxReferenceImages})
	}
	return out, nil
}
