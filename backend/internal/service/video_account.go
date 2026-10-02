package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

// VideoAccountConfiguration 是视频协议资格和预算边界，凭据仍由账号保存。
type VideoAccountConfiguration struct {
	Endpoints     []VideoEndpoint             `json:"video_endpoints"`
	ModelBindings map[string]VideoEndpointSet `json:"video_model_bindings"`
	BaseURLs      map[VideoEndpoint]string    `json:"video_base_urls"`
	ModelPaths    map[string]string           `json:"video_model_paths"`
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

// SelectEndpoint 是实际转发和智能路由共用的入站适配规则，不猜模型或转换原生参数。
func (cfg VideoAccountConfiguration) SelectEndpoint(model, inboundProtocol string, native bool) (VideoEndpoint, error) {
	invalid := func(message string) (VideoEndpoint, error) {
		return "", infraerrors.BadRequest("VIDEO_ACCOUNT_INVALID", message)
	}
	if err := validateVideoEndpointSet(cfg.Endpoints); err != nil {
		return invalid(err.Error())
	}
	enabled := make(map[VideoEndpoint]bool, len(cfg.Endpoints))
	for _, endpoint := range cfg.Endpoints {
		enabled[endpoint] = true
	}
	allowed := cfg.Endpoints
	if binding, exists := cfg.ModelBindings[model]; exists {
		allowed = binding
	}
	if err := validateVideoEndpointSet(allowed); err != nil {
		return invalid(err.Error())
	}
	for _, endpoint := range allowed {
		if !enabled[endpoint] {
			return invalid("video model binding contains a disabled endpoint")
		}
	}
	if native {
		for _, endpoint := range allowed {
			if string(endpoint) == inboundProtocol {
				return endpoint, nil
			}
		}
		return "", infraerrors.BadRequest("VIDEO_ENDPOINT_MODEL_MISMATCH", "Model does not allow the requested native protocol")
	}
	// 两个 OpenAI 视频入口分别授权，不能由另一兼容端点或唯一原生端点隐式开放。
	// 空协议只保留内部旧调用对 unified 的约定，不扩展账号或模型的端点集合。
	var requested VideoEndpoint
	switch inboundProtocol {
	case "", "unified":
		requested = VideoEndpointCompat
	case string(VideoEndpointOpenAIVideos):
		requested = VideoEndpointOpenAIVideos
	default:
		return "", infraerrors.BadRequest("VIDEO_ENDPOINT_MODEL_MISMATCH", "Model does not allow the requested video protocol")
	}
	for _, endpoint := range allowed {
		if endpoint == requested {
			return endpoint, nil
		}
	}
	return "", infraerrors.BadRequest("VIDEO_ENDPOINT_MODEL_MISMATCH", "Model does not allow the requested video protocol")
}

func validVideoEndpoint(endpoint VideoEndpoint) bool {
	switch endpoint {
	case VideoEndpointCompat, VideoEndpointOpenAIVideos, VideoEndpointSeedance, VideoEndpointKling, VideoEndpointWan, VideoEndpointMiniMax:
		return true
	}
	return false
}

// VideoConfiguration 读取显式协议，不把未配置账号扩展为视频账号。
func (a *Account) VideoConfiguration() (VideoAccountConfiguration, error) {
	var out VideoAccountConfiguration
	if a == nil || a.Platform != PlatformVideo || a.Type != AccountTypeAPIKey {
		return out, fmt.Errorf("video requires a video API Key account")
	}
	body, err := json.Marshal(a.Credentials)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(body, &out)
	return out, err
}

func validKlingVideoPath(path string) bool {
	switch path {
	case "/text-to-video/{model}", "/image-to-video/{model}", "/omni-video/{model}", "/v1/videos/text2video", "/v1/videos/omni-video":
		return true
	}
	return false
}

// normalizeVideoCredentials 在创建、编辑、批量修改和导入共用的边界拒绝含糊配置。
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
	if len(cfg.Endpoints) == 0 {
		return invalid("video_endpoints must contain at least one endpoint")
	}
	seen := map[VideoEndpoint]bool{}
	for _, endpoint := range cfg.Endpoints {
		if !validVideoEndpoint(endpoint) || seen[endpoint] {
			return invalid("video_endpoints contains an unknown or duplicate endpoint")
		}
		seen[endpoint] = true
		base := strings.TrimSpace(cfg.BaseURLs[endpoint])
		if base == "" {
			base = strings.TrimSpace(a.GetCredential("base_url"))
		}
		u, parseErr := url.Parse(base)
		if parseErr != nil || u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return invalid("each video endpoint requires an HTTP(S) base URL without credentials, query or fragment")
		}
	}
	for endpoint := range cfg.BaseURLs {
		if !seen[endpoint] {
			return invalid("video_base_urls contains a disabled endpoint")
		}
	}
	for model, endpoints := range cfg.ModelBindings {
		if strings.TrimSpace(model) == "" || model != strings.TrimSpace(model) {
			return invalid("video_model_bindings must bind non-empty models to enabled endpoints")
		}
		if err := validateVideoEndpointSet(endpoints); err != nil {
			return invalid(err.Error())
		}
		for _, endpoint := range endpoints {
			if !seen[endpoint] {
				return invalid("video_model_bindings contains a disabled endpoint")
			}
		}
	}
	for model, path := range cfg.ModelPaths {
		if strings.TrimSpace(model) == "" || !validKlingVideoPath(path) {
			return invalid("video_model_paths contains an invalid Kling path template")
		}
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
