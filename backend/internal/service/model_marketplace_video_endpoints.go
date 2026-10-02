package service

import (
	"context"
	"net/http"
	"strings"
)

// ModelMarketplaceVideoEndpoint 描述客户端创建视频的本站入口，不公开上游配置。
type ModelMarketplaceVideoEndpoint struct {
	Method   string
	Path     string
	Protocol string
}

// 仅列规范创建路径；别名及查询、下载入口由 API 文档说明。
var marketplaceVideoCreateEndpoints = []ModelMarketplaceVideoEndpoint{
	{Method: http.MethodPost, Path: "/v1/video/generations", Protocol: "compat"},
	{Method: http.MethodPost, Path: "/v1/videos", Protocol: "openai_videos"},
	{Method: http.MethodPost, Path: "/api/v3/contents/generations/tasks", Protocol: "seedance"},
	{Method: http.MethodPost, Path: "/text-to-video/{model}", Protocol: "kling"},
	{Method: http.MethodPost, Path: "/image-to-video/{model}", Protocol: "kling"},
	{Method: http.MethodPost, Path: "/omni-video/{model}", Protocol: "kling"},
	{Method: http.MethodPost, Path: "/v1/videos/text2video", Protocol: "kling"},
	{Method: http.MethodPost, Path: "/v1/videos/omni-video", Protocol: "kling"},
	{Method: http.MethodPost, Path: "/api/v1/services/aigc/video-generation/video-synthesis", Protocol: "wan"},
	{Method: http.MethodPost, Path: "/v2/video_generation", Protocol: "minimax"},
}

func marketplaceSupportsVideoEndpoints(platform string) bool {
	return platform == PlatformVideo || platform == PlatformGrok || platform == PlatformOpenAI
}

// attachMarketplaceVideoEndpoints 使用本次目录的账号快照和相同模型映射规则，避免把平台能力误认为每个模型都具备。
// @project-doc docs/interfaces/model_catalog_and_marketplace.md#marketplace_video_endpoints
func (s *ModelMarketplaceService) attachMarketplaceVideoEndpoints(ctx context.Context, group *Group, models []ModelMarketplaceModel, accounts []Account) {
	if s == nil || s.gatewayService == nil || group == nil || !marketplaceSupportsVideoEndpoints(group.Platform) || len(models) == 0 {
		return
	}
	if group.Platform != PlatformVideo && !GroupAllowsImageGeneration(group) {
		return
	}
	// 先按账号静态能力过滤，普通 OpenAI 文本账号不增加逐模型解析开销。
	eligibleAccounts := make([]*Account, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if account.Platform != group.Platform || !account.IsSchedulable() || !openAIStickyAccountMatchesGroup(account, &group.ID) {
			continue
		}
		switch group.Platform {
		case PlatformVideo:
			if account.Type != AccountTypeAPIKey {
				continue
			}
		case PlatformOpenAI:
			if !account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilitySeedance) {
				continue
			}
		case PlatformGrok:
			if eligible, _ := account.GrokMediaGenerationEligibility(); !eligible || (account.Type != AccountTypeAPIKey && account.Type != AccountTypeOAuth) {
				continue
			}
		}
		eligibleAccounts = append(eligibleAccounts, account)
	}
	if len(eligibleAccounts) == 0 {
		return
	}
	var channel *Channel
	if s.gatewayService.channelService != nil {
		var err error
		channel, err = s.gatewayService.channelService.GetChannelForGroup(ctx, group.ID)
		if err != nil {
			return
		}
	}
	// resolveTarget 只解析静态协议和安全地址，不进行上游请求、并发占位或生成任务。
	video := NewVideoUpstreamService(&OpenAIGatewayService{cfg: s.cfg})
	for i := range models {
		model := &models[i]
		paths := make(map[string]ModelMarketplaceVideoEndpoint)
		channelModel := model.ID
		if channel != nil && s.gatewayService.channelService != nil {
			if mapped := s.gatewayService.channelService.ResolveChannelMapping(ctx, group.ID, model.ID).MappedModel; strings.TrimSpace(mapped) != "" {
				channelModel = mapped
			}
		}
		for _, account := range eligibleAccounts {
			upstreamModel := account.GetMappedModel(channelModel)
			if group.Platform == PlatformGrok && CanonicalGrokImagineVideoPriceFamily(upstreamModel) == "" {
				continue
			}
			if group.Platform == PlatformOpenAI && !marketplaceModelHasVideoOutput(*model) && !marketplaceKnownSeedanceModel(upstreamModel) {
				continue
			}
			// 单账号解析保留渠道限制（含以上游模型计费）及模型级限流判断。
			if _, ok := s.gatewayService.resolveRequestableModel(ctx, &group.ID, channel, []Account{*account}, model.ID); !ok {
				continue
			}
			switch group.Platform {
			case PlatformVideo:
				if !account.IsModelSupported(channelModel) {
					continue
				}
				for _, endpoint := range marketplaceVideoCreateEndpoints {
					// 模板在展示层保留客户端模型占位符，绝不替换成私有上游模型。
					route, ok := MatchVideoGatewayRoute(endpoint.Method, endpoint.Path)
					if !ok {
						continue
					}
					_, err := video.resolveTarget(account, upstreamModel, VideoTaskSubmitRequest{InboundProtocol: route.Protocol, Native: route.Native, NativePath: route.PathTemplate})
					if err == nil {
						paths[endpoint.Path] = endpoint
					}
				}
			case PlatformGrok:
				for _, path := range []string{"/v1/videos", "/v1/videos/generations"} {
					paths[path] = ModelMarketplaceVideoEndpoint{Method: http.MethodPost, Path: path, Protocol: "grok"}
				}
			case PlatformOpenAI:
				paths[marketplaceVideoCreateEndpoints[2].Path] = marketplaceVideoCreateEndpoints[2]
			}
		}
		// 固定顺序便于客户端浏览，多个账号相同入口只显示一次。
		for _, endpoint := range marketplaceVideoCreateEndpoints {
			if found, ok := paths[endpoint.Path]; ok {
				model.VideoEndpoints = append(model.VideoEndpoints, found)
			}
		}
		if endpoint, ok := paths["/v1/videos/generations"]; ok {
			model.VideoEndpoints = append(model.VideoEndpoints, endpoint)
		}
	}
}

// 旧 Seedance 使用普通 Token 定价；只为显式视频账号识别已知上游型号，任意接入点 ID 不猜测能力。
func marketplaceKnownSeedanceModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "doubao-seedance-") || strings.HasPrefix(model, "seedance-")
}

func marketplaceModelHasVideoOutput(model ModelMarketplaceModel) bool {
	for _, modality := range model.OutputModalities {
		if modality == "video" {
			return true
		}
	}
	return model.Pricing.PricingMode == "video" || model.Pricing.PricingMode == "video_token" || model.Pricing.PricingMode == "video_per_request"
}
