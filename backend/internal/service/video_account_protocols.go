package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
)

// VideoModelProtocol 把一个上游模型绑定到唯一的协议：内置协议写作“端点.方案”，声明式模板写作“profile:键名[@版本]”。
// 统一入口 /v1/videos 只按这里的绑定选择上游协议，客户端入口不再决定端点。
// @project-doc docs/interfaces/video_upstream.md#video_accounts
type VideoModelProtocol struct {
	Protocol string `json:"protocol"`
	// Path 仅 Kling/Vidu 内置协议使用，表示模型操作路径。
	Path string `json:"path,omitempty"`
	// BaseURL 覆盖账号共享 base_url；模板协议同样适用。
	BaseURL string `json:"base_url,omitempty"`
}

// videoModelProtocolWildcard 绑定账号其余模型的默认协议，是唯一允许的通配符。
const videoModelProtocolWildcard = "*"

// videoResolvedProtocol 是解析后的绑定：内置协议给出端点与方案，模板协议给出键名与可选版本。
type videoResolvedProtocol struct {
	Endpoint       VideoEndpoint
	Adapter        string
	ProfileKey     string
	ProfileVersion int
	Path           string
	BaseURL        string
}

// IsProfile 表示绑定使用声明式模板协议。
func (p videoResolvedProtocol) IsProfile() bool { return p.ProfileKey != "" }

// parseVideoModelProtocol 校验并解析单条绑定，不推断厂商，也不允许跨端点方案。
func parseVideoModelProtocol(binding VideoModelProtocol) (videoResolvedProtocol, error) {
	out := videoResolvedProtocol{Path: strings.TrimSpace(binding.Path), BaseURL: strings.TrimSpace(binding.BaseURL)}
	protocol := strings.TrimSpace(binding.Protocol)
	if key, version, ok := ParseVideoProfileReference(protocol); ok {
		if out.Path != "" {
			return out, fmt.Errorf("profile protocols define their own paths")
		}
		out.ProfileKey, out.ProfileVersion = key, version
		return out, nil
	}
	endpoint, adapter, found := strings.Cut(protocol, ".")
	out.Endpoint, out.Adapter = VideoEndpoint(endpoint), adapter
	if !found || !validVideoEndpoint(out.Endpoint) || !ValidVideoProtocolAdapter(out.Endpoint, adapter) {
		return out, fmt.Errorf("video protocol %q must be endpoint.adapter or profile:key", protocol)
	}
	switch out.Endpoint {
	case VideoEndpointKling:
		if !validKlingVideoPath(out.Path) {
			return out, fmt.Errorf("kling protocols require a supported path template")
		}
		if adapter == VideoProtocolAdapterKling && out.Path != "/text-to-video/{model}" && out.Path != "/image-to-video/{model}" {
			return out, fmt.Errorf("kling_v1 requires /text-to-video/{model} or /image-to-video/{model}")
		}
		if adapter == VideoProtocolAdapterKemei && out.Path != "/text-to-video/{model}" && out.Path != "/image-to-video/{model}" && out.Path != "/omni-video/{model}" {
			return out, fmt.Errorf("kemei_v1 requires a native Kling model operation path")
		}
	case VideoEndpointVidu:
		if !validViduVideoPath(out.Path) {
			return out, fmt.Errorf("vidu protocols require a supported operation path")
		}
	default:
		if out.Path != "" {
			return out, fmt.Errorf("path is only used by kling and vidu protocols")
		}
	}
	return out, nil
}

// Protocol 返回模型的绑定：精确模型优先，其次通配默认值。
func (cfg VideoAccountConfiguration) Protocol(model string) (videoResolvedProtocol, bool) {
	binding, ok := cfg.ModelProtocols[model]
	if !ok {
		binding, ok = cfg.ModelProtocols[videoModelProtocolWildcard]
	}
	if !ok {
		return videoResolvedProtocol{}, false
	}
	resolved, err := parseVideoModelProtocol(binding)
	return resolved, err == nil
}

// DeclaredModels 返回显式绑定的模型（不含通配符），按名称排序。
func (cfg VideoAccountConfiguration) DeclaredModels() []string {
	models := make([]string, 0, len(cfg.ModelProtocols))
	for model := range cfg.ModelProtocols {
		if model != videoModelProtocolWildcard {
			models = append(models, model)
		}
	}
	sort.Strings(models)
	return models
}

// validateModelProtocols 拒绝含糊或无效的绑定：空模型、额外通配符、未知协议或非法地址。
func (cfg VideoAccountConfiguration) validateModelProtocols(sharedBaseURL string) error {
	if len(cfg.ModelProtocols) == 0 {
		return fmt.Errorf("video_model_protocols must bind at least one model")
	}
	for model, binding := range cfg.ModelProtocols {
		if model == "" || model != strings.TrimSpace(model) || model != videoModelProtocolWildcard && strings.ContainsAny(model, "*?") {
			return fmt.Errorf("video_model_protocols requires exact models or the * default")
		}
		resolved, err := parseVideoModelProtocol(binding)
		if err != nil {
			return fmt.Errorf("model %q: %w", model, err)
		}
		if !resolved.IsProfile() && videoDeprecatedProtocolAdapters[resolved.Adapter] {
			// 旧厂商方案只保留给在途任务恢复与结算，新绑定必须使用官方协议或模板。
			return fmt.Errorf("model %q: protocol %q has been replaced by preset templates; bind an official protocol or a profile instead", model, binding.Protocol)
		}
		base := resolved.BaseURL
		if base == "" {
			base = strings.TrimSpace(sharedBaseURL)
		}
		u, parseErr := url.Parse(base)
		if parseErr != nil || u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("model %q requires an HTTP(S) base URL without credentials, query or fragment", model)
		}
	}
	return nil
}

// convertLegacyVideoConfiguration 把旧的端点集合、模型绑定与方案配置换算为新的模型协议绑定。
// 多端点模型按旧 /v1/videos 的优先级选取 openai_videos，其次 compat，再其次配置顺序中的第一个原生端点。
func convertLegacyVideoConfiguration(cfg VideoAccountConfiguration) map[string]VideoModelProtocol {
	out := map[string]VideoModelProtocol{}
	pick := func(endpoints []VideoEndpoint) (VideoEndpoint, bool) {
		for _, preferred := range []VideoEndpoint{VideoEndpointOpenAIVideos, VideoEndpointCompat} {
			for _, endpoint := range endpoints {
				if endpoint == preferred {
					return endpoint, true
				}
			}
		}
		if len(endpoints) > 0 {
			return endpoints[0], true
		}
		return "", false
	}
	binding := func(model string, endpoint VideoEndpoint) (VideoModelProtocol, bool) {
		adapter, _ := cfg.legacyProtocolAdapter(model, endpoint)
		result := VideoModelProtocol{Protocol: string(endpoint) + "." + adapter, BaseURL: strings.TrimSpace(cfg.BaseURLs[endpoint])}
		switch endpoint {
		case VideoEndpointKling:
			result.Path = cfg.ModelPaths[model]
		case VideoEndpointVidu:
			result.Path = cfg.ViduModelPaths[model]
		}
		_, err := parseVideoModelProtocol(result)
		return result, err == nil
	}
	for model, endpoints := range cfg.ModelBindings {
		if endpoint, ok := pick(endpoints); ok {
			if result, valid := binding(model, endpoint); valid {
				out[model] = result
			}
		}
	}
	endpoint, ok := pick(cfg.Endpoints)
	if !ok {
		return out
	}
	// 仅有模型级方案覆盖或操作路径、没有显式端点绑定的模型，也要生成精确绑定，不能被默认协议吞掉。
	overridden := map[string]bool{}
	for model := range cfg.ModelAdapters {
		overridden[model] = true
	}
	for model := range cfg.ModelPaths {
		overridden[model] = endpoint == VideoEndpointKling || overridden[model]
	}
	for model := range cfg.ViduModelPaths {
		overridden[model] = endpoint == VideoEndpointVidu || overridden[model]
	}
	for model, needed := range overridden {
		if _, exists := out[model]; exists || !needed || model == "" {
			continue
		}
		if result, valid := binding(model, endpoint); valid {
			out[model] = result
		}
	}
	if endpoint == VideoEndpointKling || endpoint == VideoEndpointVidu {
		// 原生路径按模型保存，默认绑定无法表达，只为配置过路径的模型生成精确绑定。
		return out
	}
	if result, valid := binding("", endpoint); valid {
		out[videoModelProtocolWildcard] = result
	}
	return out
}

// legacyProtocolAdapter 保留旧配置的方案解析顺序：模型覆盖、端点默认、旧 OpenAI profile、原有协议。
func (cfg VideoAccountConfiguration) legacyProtocolAdapter(model string, endpoint VideoEndpoint) (string, bool) {
	if id, ok := cfg.ModelAdapters[model][endpoint]; ok {
		return id, true
	}
	if id, ok := cfg.EndpointAdapters[endpoint]; ok {
		return id, true
	}
	if endpoint == VideoEndpointOpenAIVideos && cfg.OpenAIProfile == VideoOpenAIProfileMetasoH3 {
		return VideoProtocolAdapterMetasoH3, false
	}
	return VideoProtocolAdapterLegacy, false
}

// videoLegacyCredentialKeys 是迁移后从账号凭据中清理的旧字段。
var videoLegacyCredentialKeys = []string{
	"video_endpoints", "video_model_bindings", "video_base_urls", "video_model_paths", "video_vidu_model_paths",
	"video_openai_profile", "video_endpoint_adapters", "video_model_adapters",
}

// BackfillVideoAccountProtocols 在启动时把仍保存旧端点配置的视频账号一次性迁移为 video_model_protocols。
// 换算规则与账号保存时相同；无法换算的账号保留原样并记录日志，运行时仍按旧配置即时换算，不会中断服务。
func BackfillVideoAccountProtocols(ctx context.Context, accounts AccountRepository) (int, error) {
	if accounts == nil {
		return 0, nil
	}
	list, err := accounts.ListByPlatform(ctx, PlatformVideo)
	if err != nil {
		return 0, err
	}
	migrated := 0
	for i := range list {
		account := &list[i]
		legacy := false
		for _, key := range videoLegacyCredentialKeys {
			if _, exists := account.Credentials[key]; exists {
				legacy = true
				break
			}
		}
		// 已是新格式但仍绑定被取代厂商方案的账号同样需要改绑到预置模板。
		if cfg, cfgErr := account.VideoConfiguration(); !legacy && cfgErr == nil {
			for _, binding := range cfg.ModelProtocols {
				if _, replaceable := videoDeprecatedProtocolReplacement(binding); replaceable {
					legacy = true
					break
				}
			}
		}
		if !legacy {
			continue
		}
		if err := normalizeVideoCredentials(account); err != nil {
			slog.Warn("video account protocol migration skipped", "account_id", account.ID, "error", err)
			continue
		}
		if err := accounts.Update(ctx, account); err != nil {
			slog.Warn("video account protocol migration save failed", "account_id", account.ID, "error", err)
			continue
		}
		migrated++
	}
	if migrated > 0 {
		slog.Info("video account protocols migrated", "count", migrated)
	}
	return migrated, nil
}
