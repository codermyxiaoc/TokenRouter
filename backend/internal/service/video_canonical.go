package service

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/tidwall/gjson"
)

// VideoCanonicalRequest 是统一入口 /v1/videos 与云画布共用的规范请求；
// 所有上游协议都只从这里取值，转换为各自的请求体后再交给原有协议校验与计价。
// @project-doc docs/interfaces/video_upstream.md#video_unified_api
type VideoCanonicalRequest struct {
	Model          string                     `json:"model"`
	Prompt         string                     `json:"prompt,omitempty"`
	NegativePrompt string                     `json:"negative_prompt,omitempty"`
	Duration       *float64                   `json:"duration,omitempty"`
	Resolution     string                     `json:"resolution,omitempty"`
	AspectRatio    string                     `json:"aspect_ratio,omitempty"`
	Size           string                     `json:"size,omitempty"`
	Images         []VideoCanonicalMedia      `json:"images,omitempty"`
	Videos         []VideoCanonicalMedia      `json:"videos,omitempty"`
	GenerateAudio  *bool                      `json:"generate_audio,omitempty"`
	Watermark      *bool                      `json:"watermark,omitempty"`
	Seed           *int64                     `json:"seed,omitempty"`
	Extra          map[string]json.RawMessage `json:"extra,omitempty"`
}

// VideoCanonicalMedia 是一项参考素材；角色决定其在上游协议中的位置。
type VideoCanonicalMedia struct {
	URL  string `json:"url"`
	Role string `json:"role,omitempty"`
}

const (
	VideoMediaRoleFirstFrame     = "first_frame"
	VideoMediaRoleLastFrame      = "last_frame"
	VideoMediaRoleReferenceImage = "reference_image"
	VideoMediaRoleReferenceVideo = "reference_video"
	// 单次请求的参考素材上限，防止超大数组拖慢转换与审核。
	videoCanonicalMaxMedia = 16
)

// videoCanonicalTopLevelKeys 列出统一入口接受的全部顶层字段；其余字段须放入 extra，由协议白名单决定是否透传。
var videoCanonicalTopLevelKeys = map[string]bool{
	"model": true, "prompt": true, "negative_prompt": true, "duration": true, "resolution": true, "aspect_ratio": true,
	"size": true, "images": true, "videos": true, "generate_audio": true, "watermark": true, "seed": true, "extra": true,
	// OpenAI Videos 兼容别名，解析时换算为规范字段。
	"seconds": true, "input_reference": true, "ratio": true,
}

func videoCanonicalError(code, message string) error {
	return infraerrors.BadRequest(code, message)
}

// ParseVideoCanonicalRequest 严格解析统一请求：未知字段、别名冲突和非法类型都在选号与预扣前拒绝。
func ParseVideoCanonicalRequest(body []byte) (*VideoCanonicalRequest, error) {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, videoCanonicalError("VIDEO_INVALID_REQUEST", "Video request must be a JSON object")
	}
	if err := validateVideoJSONKeys(body); err != nil {
		return nil, err
	}
	root := gjson.ParseBytes(body)
	var parseErr error
	root.ForEach(func(key, _ gjson.Result) bool {
		if !videoCanonicalTopLevelKeys[key.String()] {
			parseErr = videoCanonicalError("VIDEO_PARAMETER_UNKNOWN", "Unknown video parameter "+strconv.Quote(key.String())+"; put vendor-specific fields in extra")
			return false
		}
		return true
	})
	if parseErr != nil {
		return nil, parseErr
	}
	req := &VideoCanonicalRequest{}
	var err error
	if req.Model, err = videoCanonicalString(root, "model", true); err != nil {
		return nil, err
	}
	for _, field := range []struct {
		name   string
		target *string
	}{{"prompt", &req.Prompt}, {"negative_prompt", &req.NegativePrompt}, {"resolution", &req.Resolution}, {"aspect_ratio", &req.AspectRatio}, {"size", &req.Size}} {
		if *field.target, err = videoCanonicalString(root, field.name, false); err != nil {
			return nil, err
		}
	}
	// ratio 是旧客户端常用的比例别名，与 aspect_ratio 同时出现时必须一致。
	ratio, err := videoCanonicalString(root, "ratio", false)
	if err != nil {
		return nil, err
	}
	if ratio != "" {
		if req.AspectRatio != "" && req.AspectRatio != ratio {
			return nil, videoCanonicalError("VIDEO_PARAMETER_CONFLICT", "aspect_ratio and ratio disagree")
		}
		req.AspectRatio = ratio
	}
	if req.Duration, err = videoCanonicalDuration(root); err != nil {
		return nil, err
	}
	if req.Images, err = videoCanonicalMediaList(root.Get("images"), "images", VideoMediaRoleFirstFrame, VideoMediaRoleLastFrame, VideoMediaRoleReferenceImage); err != nil {
		return nil, err
	}
	if reference := root.Get("input_reference"); reference.Exists() {
		if len(req.Images) > 0 {
			return nil, videoCanonicalError("VIDEO_PARAMETER_CONFLICT", "Use either images or input_reference, not both")
		}
		image := reference.Get("image_url")
		if !reference.IsObject() || image.Type != gjson.String || strings.TrimSpace(image.String()) == "" || len(reference.Map()) != 1 {
			return nil, videoCanonicalError("VIDEO_PARAMETER_INVALID", "input_reference must be an object with a single image_url")
		}
		req.Images = []VideoCanonicalMedia{{URL: strings.TrimSpace(image.String()), Role: VideoMediaRoleFirstFrame}}
	}
	if req.Videos, err = videoCanonicalMediaList(root.Get("videos"), "videos", VideoMediaRoleReferenceVideo); err != nil {
		return nil, err
	}
	if len(req.Images)+len(req.Videos) > videoCanonicalMaxMedia {
		return nil, videoCanonicalError("VIDEO_PARAMETER_INVALID", "Too many reference media items")
	}
	for _, field := range []struct {
		name   string
		target **bool
	}{{"generate_audio", &req.GenerateAudio}, {"watermark", &req.Watermark}} {
		if value := root.Get(field.name); value.Exists() {
			if value.Type != gjson.True && value.Type != gjson.False {
				return nil, videoCanonicalError("VIDEO_PARAMETER_INVALID", field.name+" must be a boolean")
			}
			flag := value.Bool()
			*field.target = &flag
		}
	}
	if value := root.Get("seed"); value.Exists() {
		// 直接按 JSON 原文精确解析整数：先转 float64 会把 9007199254740993 静默舍入为 ...992 后再通过范围检查。
		if value.Type != gjson.Number {
			return nil, videoCanonicalError("VIDEO_PARAMETER_INVALID", "seed must be an integer")
		}
		seed, parseErr := strconv.ParseInt(strings.TrimSpace(value.Raw), 10, 64)
		if parseErr != nil || seed > 1<<53 || seed < -(1<<53) {
			return nil, videoCanonicalError("VIDEO_PARAMETER_INVALID", "seed must be an integer between -9007199254740992 and 9007199254740992")
		}
		req.Seed = &seed
	}
	if value := root.Get("extra"); value.Exists() {
		if !value.IsObject() {
			return nil, videoCanonicalError("VIDEO_PARAMETER_INVALID", "extra must be an object")
		}
		req.Extra = map[string]json.RawMessage{}
		if err := json.Unmarshal([]byte(value.Raw), &req.Extra); err != nil {
			return nil, videoCanonicalError("VIDEO_PARAMETER_INVALID", "extra must be an object")
		}
		if len(req.Extra) == 0 {
			req.Extra = nil
		}
	}
	return req, nil
}

func videoCanonicalString(root gjson.Result, name string, required bool) (string, error) {
	value := root.Get(name)
	if !value.Exists() || value.Type == gjson.Null {
		if required {
			return "", videoCanonicalError("VIDEO_MODEL_REQUIRED", name+" is required")
		}
		return "", nil
	}
	if value.Type != gjson.String {
		return "", videoCanonicalError("VIDEO_PARAMETER_INVALID", name+" must be a string")
	}
	text := strings.TrimSpace(value.String())
	if required && text == "" {
		return "", videoCanonicalError("VIDEO_MODEL_REQUIRED", name+" is required")
	}
	return text, nil
}

// videoCanonicalDuration 统一 duration（数字秒）与 OpenAI seconds（字符串或数字），两者同时出现时必须一致。
func videoCanonicalDuration(root gjson.Result) (*float64, error) {
	read := func(name string, allowString bool) (*float64, error) {
		value := root.Get(name)
		if !value.Exists() || value.Type == gjson.Null {
			return nil, nil
		}
		var n float64
		switch {
		case value.Type == gjson.Number:
			n = value.Float()
		case allowString && value.Type == gjson.String:
			parsed, err := strconv.ParseFloat(strings.TrimSpace(value.String()), 64)
			if err != nil {
				return nil, videoCanonicalError("VIDEO_PARAMETER_INVALID", name+" must be a positive number of seconds")
			}
			n = parsed
		default:
			return nil, videoCanonicalError("VIDEO_PARAMETER_INVALID", name+" must be a positive number of seconds")
		}
		if n <= 0 || math.IsNaN(n) || math.IsInf(n, 0) || n > 3600 {
			return nil, videoCanonicalError("VIDEO_PARAMETER_INVALID", name+" must be a positive number of seconds")
		}
		return &n, nil
	}
	duration, err := read("duration", false)
	if err != nil {
		return nil, err
	}
	seconds, err := read("seconds", true)
	if err != nil {
		return nil, err
	}
	if duration != nil && seconds != nil && *duration != *seconds {
		return nil, videoCanonicalError("VIDEO_PARAMETER_CONFLICT", "duration and seconds disagree")
	}
	if duration == nil {
		return seconds, nil
	}
	return duration, nil
}

// videoCanonicalMediaList 接受对象数组或 URL 字符串数组；角色只能取该类素材允许的值。
func videoCanonicalMediaList(value gjson.Result, name string, roles ...string) ([]VideoCanonicalMedia, error) {
	if !value.Exists() || value.Type == gjson.Null {
		return nil, nil
	}
	if !value.IsArray() {
		return nil, videoCanonicalError("VIDEO_PARAMETER_INVALID", name+" must be an array")
	}
	allowed := map[string]bool{"": true}
	for _, role := range roles {
		allowed[role] = true
	}
	out := []VideoCanonicalMedia{}
	for _, item := range value.Array() {
		media := VideoCanonicalMedia{}
		switch {
		case item.Type == gjson.String:
			media.URL = strings.TrimSpace(item.String())
		case item.IsObject():
			for key := range item.Map() {
				if key != "url" && key != "role" {
					return nil, videoCanonicalError("VIDEO_PARAMETER_INVALID", name+" items only accept url and role")
				}
			}
			url, role := item.Get("url"), item.Get("role")
			if url.Type != gjson.String || (role.Exists() && role.Type != gjson.String) {
				return nil, videoCanonicalError("VIDEO_PARAMETER_INVALID", name+" items require a string url")
			}
			media.URL, media.Role = strings.TrimSpace(url.String()), strings.TrimSpace(role.String())
		default:
			return nil, videoCanonicalError("VIDEO_PARAMETER_INVALID", name+" items must be URL strings or objects")
		}
		if media.URL == "" || !allowed[media.Role] {
			return nil, videoCanonicalError("VIDEO_PARAMETER_INVALID", name+" items require a URL and a supported role")
		}
		out = append(out, media)
	}
	return out, nil
}

// videoBuiltinRequestBody 把规范请求转换为内置协议端点期望的请求体，随后仍由原协议适配器校验并提取计费维度。
// 端点无法表达的字段直接拒绝，不能静默丢弃参考素材或改变语义。
func videoBuiltinRequestBody(req *VideoCanonicalRequest, target VideoUpstreamTarget) ([]byte, error) {
	endpoint := target.Endpoint
	if req == nil {
		return nil, videoCanonicalError("VIDEO_INVALID_REQUEST", "Video request is required")
	}
	unsupported := func(field string) error {
		return videoCanonicalError("VIDEO_PARAMETER_UNSUPPORTED", "The configured video protocol does not support "+field)
	}
	values := map[string]any{"model": req.Model}
	putCommon := func(target map[string]any) {
		if req.Duration != nil {
			target["duration"] = *req.Duration
		}
		if req.Resolution != "" {
			target["resolution"] = req.Resolution
		}
		if req.GenerateAudio != nil {
			target["generate_audio"] = *req.GenerateAudio
		}
		if req.Watermark != nil {
			target["watermark"] = *req.Watermark
		}
		if req.Seed != nil {
			target["seed"] = *req.Seed
		}
	}
	if req.Size != "" && endpoint != VideoEndpointCompat && endpoint != VideoEndpointOpenAIVideos {
		return nil, unsupported("size; use resolution and aspect_ratio")
	}
	if adapter, _ := VideoTargetProtocolAdapter(target); adapter == VideoProtocolAdapterKemei {
		// 可美方案在各端点都接受扁平请求，并依赖素材角色区分首帧、尾帧与参考；保留角色交由适配器转换。
		if req.Prompt != "" {
			values["prompt"] = req.Prompt
		}
		if req.NegativePrompt != "" {
			values["negative_prompt"] = req.NegativePrompt
		}
		putCommon(values)
		if req.AspectRatio != "" {
			values["ratio"] = req.AspectRatio
		}
		media := func(items []VideoCanonicalMedia) []map[string]string {
			out := make([]map[string]string, 0, len(items))
			for _, item := range items {
				entry := map[string]string{"url": item.URL}
				if item.Role != "" {
					entry["role"] = item.Role
				}
				out = append(out, entry)
			}
			return out
		}
		if len(req.Images) > 0 {
			values["images"] = media(req.Images)
		}
		if len(req.Videos) > 0 {
			values["videos"] = media(req.Videos)
		}
		if req.Size != "" {
			values["size"] = req.Size
		}
		return encodeVideoBuiltinBody(values, req.Extra)
	}
	switch endpoint {
	case VideoEndpointSeedance, VideoEndpointMiniMax:
		content := []any{}
		if req.Prompt != "" {
			content = append(content, map[string]string{"type": "text", "text": req.Prompt})
		}
		for _, image := range req.Images {
			role := image.Role
			if role == "" {
				role = VideoMediaRoleReferenceImage
				if len(req.Images) == 1 {
					role = VideoMediaRoleFirstFrame
				}
			}
			content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{"url": image.URL}, "role": role})
		}
		if len(req.Videos) > 0 {
			if endpoint != VideoEndpointSeedance {
				return nil, unsupported("reference videos")
			}
			for _, video := range req.Videos {
				content = append(content, map[string]any{"type": "video_url", "video_url": map[string]string{"url": video.URL}, "role": VideoMediaRoleReferenceVideo})
			}
		}
		values["content"] = content
		putCommon(values)
		if req.AspectRatio != "" {
			values["ratio"] = req.AspectRatio
		}
		if req.NegativePrompt != "" {
			values["negative_prompt"] = req.NegativePrompt
		}
	case VideoEndpointWan:
		if len(req.Videos) > 0 {
			return nil, unsupported("reference videos")
		}
		if len(req.Images) > 1 || len(req.Images) == 1 && req.Images[0].Role != "" && req.Images[0].Role != VideoMediaRoleFirstFrame {
			return nil, unsupported("more than one image or non first-frame images")
		}
		input := map[string]any{}
		if req.Prompt != "" {
			input["prompt"] = req.Prompt
		}
		if req.NegativePrompt != "" {
			input["negative_prompt"] = req.NegativePrompt
		}
		if len(req.Images) == 1 {
			input["img_url"] = req.Images[0].URL
		}
		parameters := map[string]any{}
		putCommon(parameters)
		if req.AspectRatio != "" {
			parameters["ratio"] = req.AspectRatio
		}
		values["input"] = input
		values["parameters"] = parameters
	default:
		if len(req.Videos) > 0 {
			return nil, unsupported("reference videos")
		}
		for _, image := range req.Images {
			if image.Role == VideoMediaRoleLastFrame {
				return nil, unsupported("last-frame images")
			}
		}
		if req.Prompt != "" {
			values["prompt"] = req.Prompt
		}
		if req.NegativePrompt != "" {
			values["negative_prompt"] = req.NegativePrompt
		}
		putCommon(values)
		if len(req.Images) > 0 {
			urls := make([]string, 0, len(req.Images))
			for _, image := range req.Images {
				urls = append(urls, image.URL)
			}
			adapter, _ := VideoTargetProtocolAdapter(target)
			switch {
			case endpoint == VideoEndpointKling && adapter == VideoProtocolAdapterLegacy && strings.HasPrefix(target.CreatePath, "/image-to-video/"):
				// 原样协议的 Kling 图生视频只接受单张 image 字段。
				if len(urls) != 1 {
					return nil, unsupported("more than one image on Kling image-to-video")
				}
				values["image"] = urls[0]
			case endpoint == VideoEndpointKling && adapter == VideoProtocolAdapterLegacy:
				// 原样协议的旧 Kling 接口使用 image_list 对象数组。
				list := make([]map[string]string, 0, len(urls))
				for _, url := range urls {
					list = append(list, map[string]string{"image_url": url})
				}
				values["image_list"] = list
			default:
				values["images"] = urls
			}
		}
		if req.AspectRatio != "" {
			if endpoint == VideoEndpointKling || endpoint == VideoEndpointVidu {
				values["aspect_ratio"] = req.AspectRatio
			} else {
				values["ratio"] = req.AspectRatio
			}
		}
		if req.Size != "" {
			values["size"] = req.Size
		}
	}
	return encodeVideoBuiltinBody(values, req.Extra)
}

// videoCanonicalBodyForTarget 按账号冻结的协议生成发送前请求体：内置协议转换为端点格式，模板协议保留规范 JSON 交给引擎。
func videoCanonicalBodyForTarget(req *VideoCanonicalRequest, target VideoUpstreamTarget) ([]byte, error) {
	if target.Version == videoTargetVersionProfile {
		return json.Marshal(req)
	}
	return videoBuiltinRequestBody(req, target)
}

// encodeVideoBuiltinBody 合并 extra 并编码请求体；extra 只合并到顶层且不能覆盖规范字段，由协议适配器决定是否接受。
func encodeVideoBuiltinBody(values map[string]any, extra map[string]json.RawMessage) ([]byte, error) {
	for key, raw := range extra {
		if _, exists := values[key]; exists || videoCanonicalTopLevelKeys[key] {
			return nil, videoCanonicalError("VIDEO_PARAMETER_CONFLICT", "extra."+key+" conflicts with a canonical video parameter")
		}
		values[key] = json.RawMessage(raw)
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(values); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}
