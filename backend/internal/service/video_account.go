package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

// VideoAccountConfiguration 是视频协议资格和预算边界，凭据仍由账号保存。
// ModelProtocols 是唯一的协议来源；其余字段只用于读取尚未迁移的旧账号，并在保存时清理。
type VideoAccountConfiguration struct {
	ModelProtocols map[string]VideoModelProtocol `json:"video_model_protocols,omitempty"`

	Endpoints     []VideoEndpoint             `json:"video_endpoints"`
	ModelBindings map[string]VideoEndpointSet `json:"video_model_bindings"`
	BaseURLs      map[VideoEndpoint]string    `json:"video_base_urls"`
	ModelPaths    map[string]string           `json:"video_model_paths"`
	// Vidu 操作路径独立保存，避免同模型同时绑定 Kling 时相互覆盖。
	ViduModelPaths map[string]string `json:"video_vidu_model_paths,omitempty"`
	OpenAIProfile  string            `json:"video_openai_profile,omitempty"`
	// 协议选择与端点授权分离；模型覆盖使用账号映射完成后的精确模型名。
	EndpointAdapters map[VideoEndpoint]string            `json:"video_endpoint_adapters,omitempty"`
	ModelAdapters    map[string]map[VideoEndpoint]string `json:"video_model_adapters,omitempty"`
}

// VideoEndpointSet 兼容旧单字符串绑定；旧值仅表示一个端点，不自动扩展账号能力。
type VideoEndpointSet []VideoEndpoint

func (endpoints *VideoEndpointSet) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*endpoints = VideoEndpointSet{VideoEndpoint(single)}
	} else {
		var values []VideoEndpoint
		if err := json.Unmarshal(data, &values); err != nil {
			return fmt.Errorf("video model binding must be an endpoint string or array")
		}
		*endpoints = values
	}
	return validateVideoEndpointSet(*endpoints)
}

func validateVideoEndpointSet(endpoints []VideoEndpoint) error {
	if len(endpoints) == 0 {
		return fmt.Errorf("video endpoint set must not be empty")
	}
	seen := make(map[VideoEndpoint]bool, len(endpoints))
	for _, endpoint := range endpoints {
		if !validVideoEndpoint(endpoint) || seen[endpoint] {
			return fmt.Errorf("video endpoint set contains an unknown or duplicate endpoint")
		}
		seen[endpoint] = true
	}
	return nil
}

func validVideoEndpoint(endpoint VideoEndpoint) bool {
	switch endpoint {
	case VideoEndpointCompat, VideoEndpointOpenAIVideos, VideoEndpointSeedance, VideoEndpointKling, VideoEndpointWan, VideoEndpointMiniMax, VideoEndpointVidu:
		return true
	}
	return false
}

// VideoConfiguration 读取模型协议绑定；尚未迁移的旧账号按原端点与方案配置即时换算，不扩大其能力。
func (a *Account) VideoConfiguration() (VideoAccountConfiguration, error) {
	var out VideoAccountConfiguration
	if a == nil || a.Platform != PlatformVideo || a.Type != AccountTypeAPIKey {
		return out, fmt.Errorf("video requires a video API Key account")
	}
	body, err := json.Marshal(a.Credentials)
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(body, &out); err != nil {
		return out, err
	}
	if len(out.ModelProtocols) == 0 && len(out.Endpoints) > 0 {
		if out.OpenAIProfile, err = normalizeVideoOpenAIProfile(out.OpenAIProfile); err != nil {
			return out, err
		}
		if err = out.validateLegacyProtocolAdapters(); err != nil {
			return out, err
		}
		if err = validateVideoEndpointSet(out.Endpoints); err != nil {
			return out, err
		}
		enabled := map[VideoEndpoint]bool{}
		for _, endpoint := range out.Endpoints {
			enabled[endpoint] = true
		}
		for _, endpoints := range out.ModelBindings {
			for _, endpoint := range endpoints {
				if !enabled[endpoint] {
					// 旧绑定指向未启用端点属于含糊配置，迁移时不能静默改选其它端点。
					return out, fmt.Errorf("video model binding contains a disabled endpoint")
				}
			}
		}
		out.ModelProtocols = convertLegacyVideoConfiguration(out)
	}
	return out, nil
}

// 旧配置只接受内置版本；仅在换算旧账号时使用。
func (cfg VideoAccountConfiguration) validateLegacyProtocolAdapters() error {
	validate := func(adapters map[VideoEndpoint]string) error {
		for endpoint, id := range adapters {
			if !ValidVideoProtocolAdapter(endpoint, id) {
				return fmt.Errorf("video protocol adapter is unknown or incompatible with its endpoint")
			}
		}
		return nil
	}
	if err := validate(cfg.EndpointAdapters); err != nil {
		return err
	}
	for model, adapters := range cfg.ModelAdapters {
		// 旧模型覆盖必须是精确上游模型；空名、空白或通配符属于含糊配置，不能在迁移时被静默丢弃或扩大。
		if model == "" || model != strings.TrimSpace(model) || strings.ContainsAny(model, "*?") {
			return fmt.Errorf("video_model_adapters requires an exact non-empty upstream model")
		}
		if err := validate(adapters); err != nil {
			return err
		}
	}
	return nil
}

func validKlingVideoPath(path string) bool {
	switch path {
	case "/text-to-video/{model}", "/image-to-video/{model}", "/omni-video/{model}", "/v1/videos/text2video", "/v1/videos/omni-video":
		return true
	}
	return false
}

// normalizeVideoCredentials 在创建、编辑、批量修改和导入共用的边界拒绝含糊配置，
// 并把旧端点配置迁移为 video_model_protocols 后清理旧字段。
// @project-doc docs/interfaces/video_upstream.md#video_accounts
func normalizeVideoCredentials(a *Account) error {
	if a == nil || a.Platform != PlatformVideo {
		return nil
	}
	invalid := func(message string) error { return infraerrors.BadRequest("VIDEO_ACCOUNT_INVALID", message) }
	cfg, err := a.VideoConfiguration()
	if err != nil {
		return invalid(err.Error())
	}
	if strings.TrimSpace(a.GetCredential("api_key")) == "" {
		return invalid("video api_key is required")
	}
	// 已由预置模板取代的厂商方案在保存时自动改绑；没有等价替代的旧绑定由下方校验拒绝，提示管理员手动改绑。
	rebindDeprecatedVideoProtocols(cfg.ModelProtocols)
	if err := cfg.validateModelProtocols(a.GetCredential("base_url")); err != nil {
		return invalid(err.Error())
	}
	if a.Credentials == nil {
		a.Credentials = map[string]any{}
	}
	protocols := make(map[string]any, len(cfg.ModelProtocols))
	for model, binding := range cfg.ModelProtocols {
		entry := map[string]any{"protocol": strings.TrimSpace(binding.Protocol)}
		if path := strings.TrimSpace(binding.Path); path != "" {
			entry["path"] = path
		}
		if base := strings.TrimSpace(binding.BaseURL); base != "" {
			entry["base_url"] = base
		}
		protocols[model] = entry
	}
	a.Credentials["video_model_protocols"] = protocols
	for _, key := range videoLegacyCredentialKeys {
		delete(a.Credentials, key)
	}
	// 旧 Token 预算配置在账号保存时清理；已受理任务的持久预算不受影响。
	delete(a.Credentials, "video_max_output_tokens")
	for _, field := range []string{"video_max_pending_tasks", "video_max_duration_seconds"} {
		value, exists := a.Credentials[field]
		if !exists {
			continue
		}
		// 自动时长预算允许表单以 null 表示未设置或清空。
		if value == nil && field != "video_max_pending_tasks" {
			continue
		}
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return invalid(field + " must be a positive number")
		}
		var n float64
		if json.Unmarshal(encoded, &n) != nil || n <= 0 || math.IsNaN(n) || math.IsInf(n, 0) {
			return invalid(field + " must be a positive number")
		}
		if field != "video_max_duration_seconds" && math.Trunc(n) != n {
			return invalid(field + " must be an integer")
		}
		if field == "video_max_pending_tasks" && n > 1000 {
			return invalid(field + " must be at most 1000")
		}
		if n > 1e12 {
			return invalid(field + " is too large")
		}
	}
	return nil
}

// validateVideoGroupBindings 不接受跳过混合渠道检查来绕过视频平台的硬隔离。
func (s *adminServiceImpl) validateVideoGroupBindings(ctx context.Context, platform string, ids []int64) error {
	return s.validateVideoGroupBindingsForPlatforms(ctx, []string{platform}, ids)
}

// 批量写入只读取一次目标分组，同时校验存在性与全部账号的平台。
func (s *adminServiceImpl) validateVideoGroupBindingsForPlatforms(ctx context.Context, platforms []string, ids []int64) error {
	if len(ids) > 0 && s.groupRepo == nil {
		return fmt.Errorf("group repository not configured")
	}
	for _, id := range ids {
		if id <= 0 {
			return ErrGroupNotFound
		}
		group, err := s.groupRepo.GetByID(ctx, id)
		if err != nil {
			return err
		}
		if group == nil {
			return ErrGroupNotFound
		}
		for _, platform := range platforms {
			if (platform == PlatformVideo) != (group.Platform == PlatformVideo) {
				return infraerrors.BadRequest("VIDEO_GROUP_PLATFORM_MISMATCH", "Video accounts can only bind video groups, and video groups can only contain video accounts")
			}
		}
	}
	return nil
}
