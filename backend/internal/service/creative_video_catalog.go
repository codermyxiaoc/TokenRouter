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
	// MaxReferenceVideos 是管理员为画布开放的参考视频数；公开目录再与协议上限取较小值，0 表示不开放。
	MaxReferenceVideos int `json:"max_reference_videos,omitempty"`
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

// CreativeVideoReferenceLimit 是协议能表达的画布参考素材数量上限，管理员配置只能在此范围内再收紧。
type CreativeVideoReferenceLimit struct {
	MaxImages int `json:"max_images"`
	MaxVideos int `json:"max_videos"`
}

// 内置协议由代码实现，上限随适配器固定：Seedance、Vidu 可多图，其余一张，画布暂不经内置协议发送参考视频。
// 模板协议（unified）在保存时允许到规范请求上限，实际上限由绑定模板的能力决定，见 creativeVideoProfileReferenceLimit。
// @project-doc docs/interfaces/cloud_canvas.md#video_reference_limits
func creativeBuiltinVideoReferenceLimit(endpoint VideoEndpoint) CreativeVideoReferenceLimit {
	switch endpoint {
	case CreativeVideoEndpointUnified:
		return CreativeVideoReferenceLimit{MaxImages: videoCanonicalMaxMedia, MaxVideos: videoCanonicalMaxMedia}
	case VideoEndpointSeedance, VideoEndpointVidu:
		return CreativeVideoReferenceLimit{MaxImages: 10}
	default:
		return CreativeVideoReferenceLimit{MaxImages: 1}
	}
}

// 画布能发出的素材取决于模板：没有规则读取 images/videos 时为 0；限定角色且不含 reference_image 时，
// 画布只能以首帧发送一张图；其余按 max_images/max_videos，未声明时取规范请求上限。
func creativeVideoProfileReferenceLimit(spec *VideoProtocolProfileSpec) CreativeVideoReferenceLimit {
	if spec == nil {
		return CreativeVideoReferenceLimit{}
	}
	readsImages, readsVideos := false, false
	for _, rule := range spec.Create.Body {
		readsImages = readsImages || rule.From == "images" || strings.HasPrefix(rule.From, "images[")
		readsVideos = readsVideos || rule.From == "videos"
	}
	out := CreativeVideoReferenceLimit{}
	caps := spec.Capabilities
	if readsImages {
		out.MaxImages = videoCanonicalMaxMedia
		if caps != nil && caps.MaxImages > 0 {
			out.MaxImages = caps.MaxImages
		}
		if caps != nil && len(caps.ImageRoles) > 0 && !containsFold(caps.ImageRoles, VideoMediaRoleReferenceImage) {
			out.MaxImages = 0
			if containsFold(caps.ImageRoles, VideoMediaRoleFirstFrame) {
				out.MaxImages = 1
			}
		}
	}
	if readsVideos {
		out.MaxVideos = videoCanonicalMaxMedia
		if caps != nil && caps.MaxVideos > 0 {
			out.MaxVideos = caps.MaxVideos
		}
	}
	return out
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
	// 这里只校验配置本身的取值范围；模板协议的真实上限来自模板，公开目录按较小值生效，保存时不因模板变化而拒绝。
	limit := creativeBuiltinVideoReferenceLimit(endpoint)
	if input.MaxReferenceImages < 0 || input.MaxReferenceImages > limit.MaxImages {
		return nil, fmt.Errorf("creative video max_reference_images must be between 0 and %d for endpoint %s", limit.MaxImages, endpoint)
	}
	if input.MaxReferenceVideos < 0 || input.MaxReferenceVideos > limit.MaxVideos {
		return nil, fmt.Errorf("creative video max_reference_videos must be between 0 and %d for endpoint %s", limit.MaxVideos, endpoint)
	}
	if input.MaxReferenceImages+input.MaxReferenceVideos > videoCanonicalMaxMedia {
		return nil, fmt.Errorf("creative video reference media must not exceed %d in total", videoCanonicalMaxMedia)
	}
	out := &CreativeVideoCapabilities{MaxReferenceImages: input.MaxReferenceImages, MaxReferenceVideos: input.MaxReferenceVideos}
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
func (s *CreativePublicService) creativeVideoCandidatesAndReferenceLimitsForGroup(ctx context.Context, group *Group) ([]CreativeModelCandidate, map[string]map[VideoEndpoint]creativeVideoMediaLimits, error) {
	out := []CreativeModelCandidate{}
	referenceLimits := map[string]map[VideoEndpoint]creativeVideoMediaLimits{}
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
			// 每个模型在账号上只绑定一个协议；内置协议按其端点归类，模板协议归入 unified。
			binding, bound := cfg.Protocol(final)
			if !bound {
				continue
			}
			endpoint := binding.Endpoint
			if binding.IsProfile() {
				endpoint = CreativeVideoEndpointUnified
			}
			if models[model] == nil {
				models[model] = map[VideoEndpoint]bool{}
			}
			models[model][endpoint] = true
		}
	}
	names := make([]string, 0, len(models))
	for model := range models {
		names = append(names, model)
	}
	sort.Strings(names)
	for _, model := range names {
		endpoints := []VideoEndpoint{}
		referenceLimits[model] = map[VideoEndpoint]creativeVideoMediaLimits{}
		protocolLimits := map[VideoEndpoint]CreativeVideoReferenceLimit{}
		for _, endpoint := range []VideoEndpoint{VideoEndpointCompat, VideoEndpointOpenAIVideos, VideoEndpointSeedance, VideoEndpointKling, VideoEndpointWan, VideoEndpointMiniMax, VideoEndpointVidu, CreativeVideoEndpointUnified} {
			if models[model][endpoint] {
				endpoints = append(endpoints, endpoint)
				limits := s.creativeVideoReferenceLimits(ctx, group, model, endpoint, accounts)
				referenceLimits[model][endpoint] = limits
				protocolLimits[endpoint] = limits.Reference
			}
		}
		// 候选直接返回首个可用端点的真实初始值，避免表单占位符被误认为已保存参数；
		// 各端点的协议素材上限一并返回，后台表单据此校验和提示，不在前端写死。
		out = append(out, CreativeModelCandidate{GroupID: group.ID, GroupName: group.Name, Platform: PlatformVideo, Model: model, Operations: []string{CreativeOperationGenerate}, MediaType: "video", VideoEndpoints: endpoints, VideoEndpoint: endpoints[0], VideoCapabilities: defaultCreativeVideoCapabilities(endpoints[0]), VideoReferenceLimits: protocolLimits})
	}
	return out, referenceLimits, nil
}

// creativeVideoMediaLimits 汇总一个模型端点在全部可调度账号上的画布素材限制。
type creativeVideoMediaLimits struct {
	// ImageBytes 是单张参考图字节上限，0 表示不在前端提示限制。
	ImageBytes int64
	// Reference 是协议能表达的参考图/参考视频数量，多账号取最小值。
	Reference CreativeVideoReferenceLimit
}

// 大小提示与数量上限都沿用渠道映射后再做账号映射；不能只检查公开模型名，否则模型级协议覆盖会误限旧协议。
// 扫描全部候选也覆盖通配白名单和仅支持渠道目标名的账号。单图字节：内置协议只有旧 H3 方案有上限，模板读取
// capabilities.max_reference_bytes，全部候选都声明上限时取最小值，未知或混合方案保守省略前端限制。
// 素材数量：内置协议取代码声明的上限，模板按绑定版本的能力与请求体规则推导，多账号取最小值；
// 模板无法解析时按 0 处理，宁可不开放也不让画布提交注定被拒绝的请求。
func (s *CreativePublicService) creativeVideoReferenceLimits(ctx context.Context, group *Group, model string, endpoint VideoEndpoint, accounts []Account) creativeVideoMediaLimits {
	reference := creativeBuiltinVideoReferenceLimit(endpoint)
	if endpoint != VideoEndpointOpenAIVideos && endpoint != CreativeVideoEndpointUnified {
		return creativeVideoMediaLimits{Reference: reference}
	}
	channelModel := model
	if s.PricingResolver != nil && s.PricingResolver.channelService != nil {
		lookup, err := s.PricingResolver.channelService.lookupGroupChannel(ctx, group.ID)
		if err != nil {
			return creativeVideoMediaLimits{Reference: creativeVideoUnknownReferenceLimit(endpoint, reference)}
		}
		if lookup != nil {
			channelModel = resolveMapping(lookup, group.ID, model).MappedModel
		}
	}
	var limit int64
	bytesKnown, profileSeen := true, false
	profileLimit := CreativeVideoReferenceLimit{MaxImages: videoCanonicalMaxMedia, MaxVideos: videoCanonicalMaxMedia}
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
		binding, bound := cfg.Protocol(finalModel)
		if !bound {
			continue
		}
		var current int64
		switch {
		case endpoint == CreativeVideoEndpointUnified && binding.IsProfile():
			profileSeen = true
			// 模板服务为空时 Resolve 回落到内置预置模板，与提交链路一致。
			profile, resolveErr := s.videoProfiles.Resolve(ctx, binding.ProfileKey, binding.ProfileVersion)
			if resolveErr != nil || profile == nil {
				profileLimit, bytesKnown = CreativeVideoReferenceLimit{}, false
				continue
			}
			accountLimit := creativeVideoProfileReferenceLimit(&profile.Spec)
			profileLimit.MaxImages = min(profileLimit.MaxImages, accountLimit.MaxImages)
			profileLimit.MaxVideos = min(profileLimit.MaxVideos, accountLimit.MaxVideos)
			if profile.Spec.Capabilities == nil || profile.Spec.Capabilities.MaxReferenceBytes <= 0 {
				bytesKnown = false
				continue
			}
			current = profile.Spec.Capabilities.MaxReferenceBytes
		case endpoint == VideoEndpointOpenAIVideos && !binding.IsProfile() && binding.Endpoint == endpoint:
			if binding.Adapter != VideoProtocolAdapterMetasoH3 {
				bytesKnown = false
				continue
			}
			current = CreativeVideoH3ReferenceImageLimit
		default:
			continue
		}
		if limit == 0 || current < limit {
			limit = current
		}
	}
	if !bytesKnown {
		limit = 0
	}
	if endpoint == CreativeVideoEndpointUnified {
		if !profileSeen {
			profileLimit = CreativeVideoReferenceLimit{}
		}
		reference = profileLimit
	}
	return creativeVideoMediaLimits{ImageBytes: limit, Reference: reference}
}

// 渠道映射查询失败时无法确定最终模板，模板协议不开放素材，内置协议仍按代码声明。
func creativeVideoUnknownReferenceLimit(endpoint VideoEndpoint, builtin CreativeVideoReferenceLimit) CreativeVideoReferenceLimit {
	if endpoint == CreativeVideoEndpointUnified {
		return CreativeVideoReferenceLimit{}
	}
	return builtin
}

func (s *CreativePublicService) creativeVideoModelsForGroup(ctx context.Context, group *Group, settings []CreativeModelSetting) ([]CreativeModelPublic, error) {
	candidates, referenceLimits, err := s.creativeVideoCandidatesAndReferenceLimitsForGroup(ctx, group)
	if err != nil {
		return nil, err
	}
	out := []CreativeModelPublic{}
	for _, candidate := range candidates {
		// 旧厂商方案改绑为模板后候选端点变为 unified，而已保存的画布模型设置仍记在原端点（如 openai_videos、seedance）。
		// 画布统一提交规范请求，端点只用于目录归类，因此该模型没有 unified 设置时沿用其原端点设置，避免模型从创作台消失。
		legacyFallback := false
		if containsVideoEndpoint(candidate.VideoEndpoints, CreativeVideoEndpointUnified) {
			legacyFallback = true
			for _, setting := range settings {
				if setting.MediaType == "video" && setting.GroupID == group.ID && setting.Model == candidate.Model && containsVideoEndpoint(candidate.VideoEndpoints, setting.VideoEndpoint) {
					legacyFallback = false
					break
				}
			}
		}
		for _, setting := range settings {
			if setting.MediaType != "video" || setting.GroupID != group.ID || setting.Model != candidate.Model || setting.VideoEndpoint == CreativeVideoEndpointGrok {
				continue
			}
			limitEndpoint := setting.VideoEndpoint
			if !containsVideoEndpoint(candidate.VideoEndpoints, setting.VideoEndpoint) {
				if !legacyFallback {
					continue
				}
				limitEndpoint = CreativeVideoEndpointUnified
			}
			caps, err := normalizeCreativeVideoCapabilities(setting.VideoEndpoint, setting.VideoCapabilities)
			if err != nil {
				continue
			}
			caps = creativeVideoCanvasCapabilities(caps)
			// 生效数量取管理员配置与协议上限的较小值，模板收紧后已有配置自动随之收紧，不需要重新保存。
			limits := referenceLimits[candidate.Model][limitEndpoint]
			caps.MaxReferenceImageBytes = limits.ImageBytes
			caps.MaxReferenceImages = min(caps.MaxReferenceImages, limits.Reference.MaxImages)
			caps.MaxReferenceVideos = min(caps.MaxReferenceVideos, limits.Reference.MaxVideos)
			out = append(out, CreativeModelPublic{GroupID: group.ID, GroupName: group.Name, Model: setting.Model, MediaType: "video", UsageNotes: setting.UsageNotes, Operations: []string{CreativeOperationGenerate}, VideoEndpoint: setting.VideoEndpoint, VideoCapabilities: caps, AspectRatios: caps.AspectRatios, OutputCount: 1, MaxOutputCount: 1, MaxReferenceImages: caps.MaxReferenceImages})
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
