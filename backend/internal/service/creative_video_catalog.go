package service

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
)

// CreativeVideoCapabilities 保存模型默认参数与参考图限制，公开目录另附画布可编辑范围；不是上游能力保证。
type CreativeVideoCapabilities struct {
	Durations   []float64 `json:"durations"`
	Resolutions []string  `json:"resolutions"`
	// 以下字段由公开目录派生，不改变已保存的模型默认参数或任务价卡。
	AllowCustomResolution  bool     `json:"allow_custom_resolution,omitempty"`
	AllowCustomAspectRatio bool     `json:"allow_custom_aspect_ratio,omitempty"`
	DefaultDuration        float64  `json:"default_duration,omitempty"`
	DefaultResolution      string   `json:"default_resolution,omitempty"`
	DefaultAspectRatio     string   `json:"default_aspect_ratio,omitempty"`
	AspectRatios           []string `json:"aspect_ratios"`
	MaxReferenceImages     int      `json:"max_reference_images"`
	// 仅运行目录在所有可用候选均受限时公开；上传存储上限与供应商官方限制不由此字段定义。
	MaxReferenceImageBytes int64 `json:"max_reference_image_bytes,omitempty"`
}

// 管理端候选与保存共用端点默认值，不根据请求别名猜测最终模型能力。
// @project-doc docs/interfaces/cloud_canvas.md#video_model_settings
func defaultCreativeVideoCapabilities(endpoint VideoEndpoint) *CreativeVideoCapabilities {
	duration, resolution := 5.0, "720p"
	if endpoint == VideoEndpointOpenAIVideos {
		duration = 4
	}
	if endpoint == VideoEndpointMiniMax {
		duration, resolution = 6, "768p"
	}
	return &CreativeVideoCapabilities{Durations: []float64{duration}, Resolutions: []string{resolution}, AspectRatios: []string{"16:9", "9:16"}, MaxReferenceImages: 1}
}

func normalizeCreativeVideoCapabilities(endpoint VideoEndpoint, input *CreativeVideoCapabilities) (*CreativeVideoCapabilities, error) {
	defaults := defaultCreativeVideoCapabilities(endpoint)
	if input == nil {
		input = defaults
	} else {
		// 旧表单只编辑一个字段时会发送其它空集合，按未配置处理；保留显式零参考图和所有已填值。
		// 使用副本补齐，避免修改调用方缓存的设置或正在编辑的对象。
		filled := *input
		if len(filled.Durations) == 0 {
			filled.Durations = defaults.Durations
		}
		if len(filled.Resolutions) == 0 {
			filled.Resolutions = defaults.Resolutions
		}
		if len(filled.AspectRatios) == 0 {
			filled.AspectRatios = defaults.AspectRatios
		}
		input = &filled
	}
	if len(input.Durations) > 60 {
		return nil, fmt.Errorf("creative video durations must contain at most 60 values")
	}
	if len(input.Resolutions) > 20 {
		return nil, fmt.Errorf("creative video resolutions must contain at most 20 values")
	}
	if len(input.AspectRatios) > 10 {
		return nil, fmt.Errorf("creative video aspect_ratios must contain at most 10 values")
	}
	if input.MaxReferenceImages < 0 || input.MaxReferenceImages > 10 {
		return nil, fmt.Errorf("creative video max_reference_images must be between 0 and 10")
	}
	if endpoint != VideoEndpointSeedance && endpoint != VideoEndpointVidu && input.MaxReferenceImages > 1 {
		return nil, fmt.Errorf("creative video endpoint supports at most one reference image")
	}
	out := &CreativeVideoCapabilities{MaxReferenceImages: input.MaxReferenceImages}
	seenDuration := map[float64]bool{}
	for _, duration := range input.Durations {
		if duration <= 0 || duration > 60 || math.IsNaN(duration) || math.IsInf(duration, 0) || math.Trunc(duration) != duration || seenDuration[duration] {
			return nil, fmt.Errorf("creative video duration is invalid")
		}
		seenDuration[duration] = true
		out.Durations = append(out.Durations, duration)
	}
	for _, raw := range input.Resolutions {
		resolution, err := NormalizeVideoPriceResolution(raw)
		if err != nil || containsCreativeOperation(out.Resolutions, resolution) {
			return nil, fmt.Errorf("creative video resolution is invalid")
		}
		out.Resolutions = append(out.Resolutions, resolution)
	}
	allowedRatios := map[string]bool{"1:1": true, "16:9": true, "9:16": true, "4:3": true, "3:4": true, "21:9": true, "9:21": true, "3:2": true, "2:3": true, "adaptive": true}
	for _, raw := range input.AspectRatios {
		ratio := strings.TrimSpace(raw)
		if !allowedRatios[ratio] || containsCreativeOperation(out.AspectRatios, ratio) {
			return nil, fmt.Errorf("creative video ratio is invalid")
		}
		out.AspectRatios = append(out.AspectRatios, ratio)
	}
	return out, nil
}

// 画布开放连续时长和自定义分辨率；比例使用预设，保留原配置默认值及参考图上限。
// 仅扩展运行时目录，不回写管理员设置；正式协议校验和按实际参数报价仍由视频任务服务负责。
func creativeVideoCanvasCapabilities(input *CreativeVideoCapabilities) *CreativeVideoCapabilities {
	if input == nil {
		return nil
	}
	out := *input
	out.AllowCustomResolution, out.AllowCustomAspectRatio = true, false
	out.Durations = make([]float64, 60)
	for i := range out.Durations {
		out.Durations[i] = float64(i + 1)
	}
	if len(input.Durations) > 0 {
		out.DefaultDuration = input.Durations[0]
		// 旧前端先排序再取最小时长，扩展为 1–60 秒后仍维持原先的默认选择。
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
	out.Resolutions = []string{"480p", "720p", "1080p"}
	for _, value := range input.Resolutions {
		if !containsCreativeOperation(out.Resolutions, value) {
			out.Resolutions = append(out.Resolutions, value)
		}
	}
	out.AspectRatios = []string{"1:1", "3:4", "4:3", "16:9", "9:16", "21:9", "adaptive"}
	for _, value := range input.AspectRatios {
		if !containsCreativeOperation(out.AspectRatios, value) {
			out.AspectRatios = append(out.AspectRatios, value)
		}
	}
	return &out
}

// 候选只从真实可调度账号的显式模型及端点产生，不把通配白名单扩展成任意模型。
func (s *CreativePublicService) creativeVideoCandidatesForGroup(ctx context.Context, group *Group) ([]CreativeModelCandidate, error) {
	candidates, _, err := s.creativeVideoCandidatesAndReferenceLimitsForGroup(ctx, group)
	return candidates, err
}

// 同一次候选扫描收集发送限制，避免目录与实际公开模型使用不同账号快照。
// 混合协议不声明统一限制，提交仍按实际选中候选在预扣前校验。
func (s *CreativePublicService) creativeVideoCandidatesAndReferenceLimitsForGroup(ctx context.Context, group *Group) ([]CreativeModelCandidate, map[string]map[VideoEndpoint]int64, error) {
	out := []CreativeModelCandidate{}
	referenceLimits := map[string]map[VideoEndpoint]int64{}
	if group == nil || group.Platform != PlatformVideo || !group.IsActive() || s.AccountRepo == nil {
		return out, referenceLimits, nil
	}
	accounts, err := s.AccountRepo.ListSchedulableByGroupIDAndPlatform(ctx, group.ID, PlatformVideo)
	if err != nil {
		return nil, nil, err
	}
	models := map[string]map[VideoEndpoint]bool{}
	for i := range accounts {
		a := &accounts[i]
		if a.Platform != PlatformVideo || !a.IsSchedulable() {
			continue
		}
		cfg, configErr := a.VideoConfiguration()
		if configErr != nil {
			continue
		}
		// 视频账号的模型空间沿用账号保存时校验过的请求模型集合，既包含
		// model_mapping/model_bindings，也包含仅配置 model_whitelist 的账号；
		// 不能套用文本平台的固定白名单，否则有效视频模型会被错误隐藏。
		configuredModels := a.GetConfiguredRequestModels()
		for _, model := range configuredModels {
			if model == "" || strings.ContainsAny(model, "*?") {
				continue
			}
			final := a.GetMappedModel(model)
			if final == "" {
				final = model
			}
			for _, endpoint := range cfg.Endpoints {
				if _, selectErr := cfg.SelectEndpoint(final, string(endpoint), true); selectErr != nil {
					continue
				}
				if endpoint == VideoEndpointKling && !validKlingVideoPath(cfg.ModelPaths[final]) {
					continue
				}
				if endpoint == VideoEndpointVidu && !validViduVideoPath(cfg.ViduModelPaths[final]) {
					continue
				}
				if models[model] == nil {
					models[model] = map[VideoEndpoint]bool{}
				}
				models[model][endpoint] = true
			}
		}
	}
	names := make([]string, 0, len(models))
	for model := range models {
		names = append(names, model)
	}
	sort.Strings(names)
	for _, model := range names {
		endpoints := []VideoEndpoint{}
		referenceLimits[model] = map[VideoEndpoint]int64{}
		for _, endpoint := range []VideoEndpoint{VideoEndpointCompat, VideoEndpointOpenAIVideos, VideoEndpointSeedance, VideoEndpointKling, VideoEndpointWan, VideoEndpointMiniMax, VideoEndpointVidu} {
			if models[model][endpoint] {
				endpoints = append(endpoints, endpoint)
				referenceLimits[model][endpoint] = s.creativeVideoReferenceImageLimit(ctx, group, model, endpoint, accounts)
			}
		}
		// 候选直接返回首个可用端点的真实初始值，避免表单占位符被误认为已保存参数。
		out = append(out, CreativeModelCandidate{GroupID: group.ID, GroupName: group.Name, Platform: PlatformVideo, Model: model, Operations: []string{CreativeOperationGenerate}, MediaType: "video", VideoEndpoints: endpoints, VideoEndpoint: endpoints[0], VideoCapabilities: defaultCreativeVideoCapabilities(endpoints[0])})
	}
	return out, referenceLimits, nil
}

// 大小提示沿用渠道映射后再做账号映射；不能只检查公开模型名，否则模型级协议覆盖会误限旧协议。
// 扫描全部候选也覆盖通配白名单和仅支持渠道目标名的账号，未知或混合方案保守省略前端限制。
func (s *CreativePublicService) creativeVideoReferenceImageLimit(ctx context.Context, group *Group, model string, endpoint VideoEndpoint, accounts []Account) int64 {
	if endpoint != VideoEndpointOpenAIVideos {
		return 0
	}
	channelModel := model
	if s.PricingResolver != nil && s.PricingResolver.channelService != nil {
		lookup, err := s.PricingResolver.channelService.lookupGroupChannel(ctx, group.ID)
		if err != nil {
			return 0
		}
		if lookup != nil {
			channelModel = resolveMapping(lookup, group.ID, model).MappedModel
		}
	}
	found := false
	for i := range accounts {
		account := &accounts[i]
		if account.Platform != PlatformVideo || account.Type != AccountTypeAPIKey || !account.IsSchedulable() || !openAIStickyAccountMatchesGroup(account, &group.ID) || !account.IsModelSupported(channelModel) {
			continue
		}
		cfg, err := account.VideoConfiguration()
		if err != nil {
			continue
		}
		finalModel := account.GetMappedModel(channelModel)
		if _, err := cfg.SelectEndpoint(finalModel, string(endpoint), true); err != nil {
			continue
		}
		if adapter, _ := cfg.ProtocolAdapter(finalModel, endpoint); adapter != VideoProtocolAdapterMetasoH3 {
			return 0
		}
		found = true
	}
	if found {
		return CreativeVideoH3ReferenceImageLimit
	}
	return 0
}

func (s *CreativePublicService) creativeVideoModelsForGroup(ctx context.Context, group *Group, settings []CreativeModelSetting) ([]CreativeModelPublic, error) {
	candidates, referenceLimits, err := s.creativeVideoCandidatesAndReferenceLimitsForGroup(ctx, group)
	if err != nil {
		return nil, err
	}
	out := []CreativeModelPublic{}
	for _, candidate := range candidates {
		for _, setting := range settings {
			if setting.MediaType != "video" || setting.GroupID != group.ID || setting.Model != candidate.Model || !containsVideoEndpoint(candidate.VideoEndpoints, setting.VideoEndpoint) {
				continue
			}
			caps, err := normalizeCreativeVideoCapabilities(setting.VideoEndpoint, setting.VideoCapabilities)
			if err != nil {
				continue
			}
			caps = creativeVideoCanvasCapabilities(caps)
			caps.MaxReferenceImageBytes = referenceLimits[candidate.Model][setting.VideoEndpoint]
			out = append(out, CreativeModelPublic{GroupID: group.ID, GroupName: group.Name, Model: setting.Model, MediaType: "video", Operations: []string{CreativeOperationGenerate}, VideoEndpoint: setting.VideoEndpoint, VideoCapabilities: caps, AspectRatios: caps.AspectRatios, OutputCount: 1, MaxOutputCount: 1, MaxReferenceImages: caps.MaxReferenceImages})
		}
	}
	return out, nil
}

func containsVideoEndpoint(values []VideoEndpoint, target VideoEndpoint) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
