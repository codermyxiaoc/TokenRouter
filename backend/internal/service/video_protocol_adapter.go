package service

import (
	"bytes"
	"encoding/json"
	"math"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/tidwall/gjson"
)

const (
	VideoProtocolAdapterLegacy    = "legacy_v1"
	VideoProtocolAdapterOpenAI    = "openai_v1"
	VideoProtocolAdapterMetasoH3  = "metaso_h3_v1"
	VideoProtocolAdapterSeedance  = "seedance_v1"
	VideoProtocolAdapterMiniMax   = "minimax_v2"
	VideoProtocolAdapterWan       = "wan_v1"
	VideoProtocolAdapterKling     = "kling_v1"
	VideoProtocolAdapterKemei     = "kemei_v1"
	VideoProtocolAdapterVidu      = "vidu_v2"
	VideoProtocolAdapterYezonghui = "yezonghui_v1"
)

// ValidVideoProtocolAdapter 限制适配器与端点的组合，不能通过配置跨协议授权。
func ValidVideoProtocolAdapter(endpoint VideoEndpoint, id string) bool {
	if !validVideoEndpoint(endpoint) {
		return false
	}
	// 可美方案按显式端点启用；未公布的 /v1/videos 不借兼容入口自动授权。
	if id == VideoProtocolAdapterKemei {
		return endpoint != VideoEndpointOpenAIVideos
	}
	if id == VideoProtocolAdapterLegacy {
		return true
	}
	switch endpoint {
	case VideoEndpointCompat:
		// 兼容路径只借用明确的 OpenAI 参数合同，不迁移请求路径或端点权限。
		return id == VideoProtocolAdapterOpenAI
	case VideoEndpointOpenAIVideos:
		return id == VideoProtocolAdapterOpenAI || id == VideoProtocolAdapterMetasoH3 || id == VideoProtocolAdapterYezonghui
	case VideoEndpointSeedance:
		return id == VideoProtocolAdapterSeedance
	case VideoEndpointMiniMax:
		return id == VideoProtocolAdapterMiniMax
	case VideoEndpointWan:
		return id == VideoProtocolAdapterWan
	case VideoEndpointKling:
		return id == VideoProtocolAdapterKling
	case VideoEndpointVidu:
		return id == VideoProtocolAdapterVidu
	}
	return false
}

// VideoTargetProtocolAdapter 只解释创建时的快照；版本一任务始终沿用原协议。
func VideoTargetProtocolAdapter(target VideoUpstreamTarget) (string, error) {
	if target.Version == videoTargetVersionProfile {
		// 模板协议以快照哈希为准，端点枚举不参与；快照缺失或被篡改时整体拒绝。
		if _, err := videoProfileSpecFromTarget(target); err != nil {
			return "", err
		}
		if _, _, ok := ParseVideoProfileReference(target.ProtocolAdapter); !ok {
			return "", videoProtocolError("Unknown frozen video profile")
		}
		return target.ProtocolAdapter, nil
	}
	if !validVideoEndpoint(target.Endpoint) {
		return "", videoProtocolError("Unknown video endpoint")
	}
	if target.Version == 1 && target.ProtocolAdapter == "" {
		if target.Endpoint == VideoEndpointVidu {
			return "", videoProtocolError("Vidu requires an explicit versioned protocol adapter")
		}
		if target.Endpoint == VideoEndpointOpenAIVideos {
			if _, err := normalizeVideoOpenAIProfile(target.OpenAIProfile); err != nil {
				return "", videoProtocolError("Unknown frozen OpenAI profile")
			}
			if target.OpenAIProfile == VideoOpenAIProfileMetasoH3 {
				return VideoProtocolAdapterMetasoH3, nil
			}
		}
		return VideoProtocolAdapterLegacy, nil
	}
	if target.Version != 2 || !ValidVideoProtocolAdapter(target.Endpoint, target.ProtocolAdapter) {
		return "", videoProtocolError("Unknown frozen video protocol adapter")
	}
	return target.ProtocolAdapter, nil
}

// ValidateVideoProtocolTarget 新版本只接受内置路径，旧版本不新增路径限制。
func ValidateVideoProtocolTarget(target VideoUpstreamTarget) error {
	if target.Version == videoTargetVersionProfile {
		spec, specErr := videoProfileSpecFromTarget(target)
		if specErr != nil {
			return specErr
		}
		if target.Model == "" || strings.TrimSpace(target.Model) != target.Model {
			return videoProtocolError("Invalid frozen video model")
		}
		createPath, pathErr := renderVideoProfilePath(spec.Create.Path, target, "", false)
		if pathErr != nil || createPath != target.CreatePath {
			return videoProtocolError("Invalid frozen video creation path")
		}
		return nil
	}
	id, err := VideoTargetProtocolAdapter(target)
	if err != nil || target.Version == 1 {
		return err
	}
	if target.Model == "" || strings.TrimSpace(target.Model) != target.Model || (target.Endpoint == VideoEndpointKling && validateUpstreamPathSegment("video model", target.Model) != nil) {
		return videoProtocolError("Invalid frozen video model")
	}
	expected := map[VideoEndpoint]string{
		VideoEndpointCompat: "/v1/video/generations", VideoEndpointOpenAIVideos: "/v1/videos",
		VideoEndpointSeedance: "/api/v3/contents/generations/tasks", VideoEndpointMiniMax: "/v2/video_generation",
		VideoEndpointWan: "/api/v1/services/aigc/video-generation/video-synthesis",
	}
	if target.OpenAIProfile != "" {
		return videoProtocolError("Version two target must use protocol_adapter without a legacy profile")
	}
	if target.Endpoint == VideoEndpointVidu {
		if !validViduVideoPath(target.CreatePath) {
			return videoProtocolError("Invalid frozen Vidu operation path")
		}
		return nil
	}
	if target.Endpoint != VideoEndpointKling {
		if target.CreatePath != expected[target.Endpoint] {
			return videoProtocolError("Invalid frozen video creation path")
		}
		return nil
	}
	for _, prefix := range []string{"/text-to-video/", "/image-to-video/", "/omni-video/"} {
		if prefix == "/omni-video/" && id != VideoProtocolAdapterLegacy && id != VideoProtocolAdapterKemei {
			continue
		}
		if target.CreatePath == prefix+url.PathEscape(target.Model) {
			return nil
		}
	}
	if id == VideoProtocolAdapterLegacy && (target.CreatePath == "/v1/videos/text2video" || target.CreatePath == "/v1/videos/omni-video") {
		return nil
	}
	return videoProtocolError("Kling official protocol requires a model operation path")
}

// VideoProtocolTaskPath 将查询路径绑定到冻结版本，不读取后来修改的账号配置。
func VideoProtocolTaskPath(target VideoUpstreamTarget, taskID string) (string, error) {
	if target.Version == videoTargetVersionProfile {
		return videoProfileTaskPath(target, taskID)
	}
	if err := ValidateVideoProtocolTarget(target); err != nil {
		return "", err
	}
	return videoTaskPath(target, taskID)
}

func videoProtocolError(message string) error {
	return infraerrors.BadRequest("VIDEO_PROTOCOL_PARAMETERS_INVALID", message)
}

// PrepareVideoProtocolRequest 统一正式请求与管理员测试的转换，并从实际发送正文提取报价维度。
// @project-doc docs/interfaces/video_upstream.md#video_adapters
func PrepareVideoProtocolRequest(body []byte, modelPath string, target VideoUpstreamTarget) ([]byte, VideoRequestMetadata, error) {
	var metadata VideoRequestMetadata
	if target.Version == videoTargetVersionProfile {
		// 模板协议的发送前正文就是规范请求，由引擎按快照生成上游请求体并提取计价维度。
		spec, specErr := videoProfileSpecFromTarget(target)
		if specErr != nil {
			return nil, metadata, specErr
		}
		canonical, parseErr := ParseVideoCanonicalRequest(body)
		if parseErr != nil {
			return nil, metadata, parseErr
		}
		canonical.Model = target.Model
		return buildVideoProfileCreateBody(spec, canonical, false)
	}
	if err := ValidateVideoProtocolTarget(target); err != nil {
		return nil, metadata, err
	}
	id, err := VideoTargetProtocolAdapter(target)
	if err != nil {
		return nil, metadata, err
	}
	if id == VideoProtocolAdapterLegacy {
		if target.Endpoint == VideoEndpointOpenAIVideos {
			return PrepareVideoOpenAIRequest(body, modelPath, "")
		}
		metadata, err = ParseVideoEndpointRequestMetadata(body, modelPath, target.Endpoint)
		if target.Endpoint == VideoEndpointVidu {
			// 保留请求正文不等于把未知主体素材计为零张；主体库无法在本地展开，交给图片附加费门禁处理。
			root := gjson.ParseBytes(body)
			if root.Get("subjects").Exists() || root.Get("auto_subjects").Bool() {
				metadata.ReferenceImageCount = nil
			}
		}
		return body, metadata, err
	}
	if id == VideoProtocolAdapterMetasoH3 {
		return PrepareVideoOpenAIRequest(body, modelPath, VideoOpenAIProfileMetasoH3)
	}
	if _, err = ParseVideoRequestModel(body, modelPath); err != nil {
		return nil, metadata, err
	}
	if err = validateVideoProtocolCanonicalNames(gjson.ParseBytes(body)); err != nil {
		return nil, metadata, err
	}
	var values map[string]json.RawMessage
	if err = json.Unmarshal(body, &values); err != nil {
		return nil, metadata, err
	}
	// 保留未知扩展的原始 JSON 数字精度，只改写已确认的官方字段及明确别名。
	videoProtocolPut(values, "model", target.Model)
	switch id {
	case VideoProtocolAdapterOpenAI:
		body, metadata, err = prepareVideoOfficialOpenAI(values)
	case VideoProtocolAdapterMiniMax:
		body, metadata, err = prepareVideoOfficialContent(values, target.Endpoint)
	case VideoProtocolAdapterSeedance:
		body, metadata, err = prepareVideoOfficialSeedance(values)
	case VideoProtocolAdapterWan:
		body, metadata, err = prepareVideoOfficialWan(values)
	case VideoProtocolAdapterKling:
		body, metadata, err = prepareVideoOfficialKling(values, target)
	case VideoProtocolAdapterKemei:
		body, metadata, err = prepareVideoKemeiRequest(values, target)
	case VideoProtocolAdapterVidu:
		body, metadata, err = prepareVideoOfficialVidu(values, target)
	case VideoProtocolAdapterYezonghui:
		body, metadata, err = prepareVideoYezonghui(values)
	}
	metadata.Model = target.Model
	return body, metadata, err
}

func videoProtocolPut(values map[string]json.RawMessage, key string, value any) {
	values[key], _ = json.Marshal(value)
}

// 官方适配器只接受规范字段大小写，防止素材留在扩展字段而被本地当作零张图片。
func validateVideoProtocolCanonicalNames(root gjson.Result) error {
	var invalid bool
	root.ForEach(func(key, value gjson.Result) bool {
		if root.IsObject() {
			name := key.String()
			switch strings.ToLower(name) {
			case "prompt", "settings", "contents", "images", "image_urls", "input_reference", "image", "image_tail", "image_list", "img_url", "first_frame_url", "last_frame_url", "seconds", "ratio", "aspect_ratio", "file_id", "image_url", "role", "text":
				if name != strings.ToLower(name) {
					invalid = true
					return false
				}
			}
		}
		if (value.IsObject() || value.IsArray()) && validateVideoProtocolCanonicalNames(value) != nil {
			invalid = true
			return false
		}
		return true
	})
	if invalid {
		return videoProtocolError("Video parameters must use canonical field names")
	}
	return nil
}

// 未知多模态类型不能证明没有图片，保留未知计数交给附加费校验拒绝或等待明确用量。
func videoProtocolHasUnknownMedia(value gjson.Result) bool {
	if !value.IsArray() {
		return value.Exists()
	}
	for _, item := range value.Array() {
		switch item.Get("type").String() {
		case "text", "image", "image_url", "reference_image", "first_frame", "last_frame", "video", "video_url", "reference_video", "audio", "audio_url", "reference_audio":
		default:
			return true
		}
	}
	return false
}

func videoProtocolRoot(values map[string]json.RawMessage) gjson.Result {
	body, _ := json.Marshal(values)
	return gjson.ParseBytes(body)
}

func videoProtocolReject(values map[string]json.RawMessage, keys ...string) error {
	for _, key := range keys {
		if _, exists := values[key]; exists {
			return videoProtocolError("Unsupported or conflicting video parameter: " + key)
		}
	}
	return nil
}

// 字段合并必须等值，不能让上游与计费分别选用冲突的别名。
func videoProtocolMove(values, destination map[string]json.RawMessage, source, key string) error {
	value, exists := values[source]
	if !exists {
		return nil
	}
	if previous, set := destination[key]; set {
		var a, b any
		left, right := json.NewDecoder(bytes.NewReader(previous)), json.NewDecoder(bytes.NewReader(value))
		left.UseNumber()
		right.UseNumber()
		if left.Decode(&a) != nil || right.Decode(&b) != nil || !reflect.DeepEqual(a, b) {
			return infraerrors.BadRequest("VIDEO_METADATA_CONFLICT", "Conflicting video parameter: "+key)
		}
	} else {
		destination[key] = value
	}
	delete(values, source)
	return nil
}

func videoProtocolObject(values map[string]json.RawMessage, key string) (map[string]json.RawMessage, error) {
	result := map[string]json.RawMessage{}
	if raw, ok := values[key]; ok {
		if !gjson.ParseBytes(raw).IsObject() || json.Unmarshal(raw, &result) != nil {
			return nil, videoProtocolError(key + " must be an object")
		}
	}
	return result, nil
}

func videoProtocolPositiveSeconds(value gjson.Result, allowString bool) (float64, error) {
	n := value.Float()
	valid := value.Type == gjson.Number
	if allowString && value.Type == gjson.String {
		parsed, err := strconv.ParseInt(value.String(), 10, 32)
		valid = err == nil && strconv.FormatInt(parsed, 10) == value.String()
		n = float64(parsed)
	}
	if !valid || n <= 0 || n > math.MaxInt32 || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n {
		return 0, videoProtocolError("Video duration must be a positive integer")
	}
	return n, nil
}

func videoProtocolCanonicalResolution(values map[string]json.RawMessage, upper bool) error {
	if raw, ok := values["resolution"]; ok {
		value := gjson.ParseBytes(raw)
		if value.Type != gjson.String || strings.TrimSpace(value.String()) == "" {
			return videoProtocolError("resolution must be a non-empty string")
		}
		resolution := strings.TrimSpace(value.String())
		if _, err := NormalizeVideoPriceResolution(resolution); err != nil {
			return videoProtocolError("resolution is invalid")
		}
		if upper {
			resolution = strings.ToUpper(resolution)
		} else {
			resolution = strings.ToLower(resolution)
		}
		videoProtocolPut(values, "resolution", resolution)
	}
	return nil
}

// 通用图片别名只允许 URL 字符串数组；同时给出两个别名时必须逐项相等。
func videoProtocolImages(values map[string]json.RawMessage) ([]string, bool, error) {
	var result []string
	present := false
	for _, key := range []string{"images", "image_urls"} {
		raw, exists := values[key]
		if !exists {
			continue
		}
		var items []string
		if !gjson.ParseBytes(raw).IsArray() || json.Unmarshal(raw, &items) != nil {
			return nil, false, videoProtocolError("Reference images must be URL string arrays")
		}
		for _, image := range items {
			if strings.TrimSpace(image) == "" {
				return nil, false, videoProtocolError("Reference image must not be empty")
			}
		}
		if present && !reflect.DeepEqual(result, items) {
			return nil, false, infraerrors.BadRequest("VIDEO_METADATA_CONFLICT", "Conflicting reference image arrays")
		}
		result, present = items, true
		delete(values, key)
	}
	return result, present, nil
}

// 官方 OpenAI JSON 只转换含义明确的旧别名；尺寸始终按精确像素报价。
func prepareVideoOfficialOpenAI(values map[string]json.RawMessage) ([]byte, VideoRequestMetadata, error) {
	var metadata VideoRequestMetadata
	if err := videoProtocolReject(values, "content", "contents", "input", "parameters", "media", "image", "image_tail", "image_list", "image_url", "img_url", "first_frame_url", "last_frame_url", "video_list", "element_list", "videos", "reference_videos", "reference_video", "reference_video_url", "video_url", "audio_url"); err != nil {
		return nil, metadata, err
	}
	root := videoProtocolRoot(values)
	if value := root.Get("prompt"); value.Type != gjson.String || strings.TrimSpace(value.String()) == "" {
		return nil, metadata, videoProtocolError("OpenAI prompt is required")
	}
	seconds, present, err := videoOpenAIDuration(root, []string{"seconds", "duration"}, false)
	if err != nil || (present && seconds != 4 && seconds != 8 && seconds != 12) {
		return nil, metadata, videoProtocolError("OpenAI seconds must be 4, 8 or 12")
	}
	if present {
		videoProtocolPut(values, "seconds", strconv.FormatInt(int64(seconds), 10))
	}
	delete(values, "duration")
	// 官方文档明确的默认值写入发送正文，避免只在本地报价而上游采用另一值。
	if !present {
		videoProtocolPut(values, "seconds", "4")
	}
	size, err := videoOfficialOpenAISize(root)
	if err != nil {
		return nil, metadata, err
	}
	videoProtocolPut(values, "size", size)
	delete(values, "resolution")
	delete(values, "ratio")
	images, imagePresent, err := videoProtocolImages(values)
	if err != nil || len(images) > 1 {
		return nil, metadata, videoProtocolError("OpenAI accepts one reference image")
	}
	if imagePresent && len(images) == 1 {
		if _, exists := values["input_reference"]; exists {
			return nil, metadata, videoProtocolError("Use only one reference image representation")
		}
		videoProtocolPut(values, "input_reference", map[string]string{"image_url": images[0]})
	}
	count := 0
	if raw, exists := values["input_reference"]; exists {
		reference := gjson.ParseBytes(raw)
		if !reference.IsObject() || !videoOpenAIKeysCanonical(reference, "image_url", "file_id") {
			return nil, metadata, videoProtocolError("input_reference must be an object")
		}
		for _, key := range []string{"image_url", "file_id"} {
			if value := reference.Get(key); value.Exists() {
				if value.Type != gjson.String || strings.TrimSpace(value.String()) == "" {
					return nil, metadata, videoProtocolError("Reference image URL or file ID must not be empty")
				}
				count++
			}
		}
		if count != 1 {
			return nil, metadata, videoProtocolError("input_reference requires exactly one image_url or file_id")
		}
	}
	body, err := json.Marshal(values)
	if err != nil {
		return nil, metadata, err
	}
	_, metadata, err = PrepareVideoOpenAIRequest(body, "", "")
	metadata.ReferenceImageCount = &count
	return body, metadata, err
}

func videoOfficialOpenAISize(root gjson.Result) (string, error) {
	size, resolution, ratio := "", "", ""
	for _, key := range []string{"size", "resolution", "ratio"} {
		value := root.Get(key)
		if !value.Exists() {
			continue
		}
		if value.Type != gjson.String || strings.TrimSpace(value.String()) == "" {
			return "", videoProtocolError("OpenAI size, resolution and ratio must be strings")
		}
		switch key {
		case "size":
			size = strings.ToLower(value.String())
		case "resolution":
			resolution = strings.ToLower(value.String())
		case "ratio":
			ratio = value.String()
		}
	}
	if size != "" {
		if _, err := NormalizeVideoPriceResolution(size); err != nil || !strings.Contains(size, "x") {
			return "", videoProtocolError("OpenAI size must use WIDTHxHEIGHT")
		}
	}
	if resolution != "" {
		candidate := resolution
		if !strings.Contains(resolution, "x") {
			if ratio == "" && size == "" {
				return "", videoProtocolError("resolution alias requires an explicit ratio or size")
			}
			if ratio == "" {
				switch size {
				case "1280x720", "1792x1024":
					ratio = "16:9"
				case "720x1280", "1024x1792":
					ratio = "9:16"
				}
			}
			candidate = map[string]string{"720p/16:9": "1280x720", "720p/9:16": "720x1280"}[resolution+"/"+ratio]
			if candidate == "" {
				return "", videoProtocolError("Unknown OpenAI resolution and ratio mapping; use size")
			}
		}
		if size != "" && size != candidate {
			return "", infraerrors.BadRequest("VIDEO_METADATA_CONFLICT", "OpenAI size and resolution disagree")
		}
		size = candidate
	}
	if size == "" {
		if ratio != "" {
			return "", videoProtocolError("ratio requires an explicit OpenAI size")
		}
		size = "720x1280"
	}
	if ratio != "" {
		orientation := map[string]string{"1280x720": "16:9", "720x1280": "9:16", "1792x1024": "16:9", "1024x1792": "9:16"}[size]
		if orientation != ratio {
			return "", videoProtocolError("OpenAI size and ratio disagree")
		}
	}
	if size != "720x1280" && size != "1280x720" && size != "1024x1792" && size != "1792x1024" {
		return "", videoProtocolError("OpenAI size must be 720x1280, 1280x720, 1024x1792 or 1792x1024")
	}
	return size, nil
}

// MiniMax V2 与 Seedance 使用 content 正文；转换不会下载、转存或重编码素材。
func prepareVideoOfficialContent(values map[string]json.RawMessage, endpoint VideoEndpoint) ([]byte, VideoRequestMetadata, error) {
	var metadata VideoRequestMetadata
	if err := videoProtocolReject(values, "input", "parameters", "contents", "settings", "media", "input_reference", "image", "image_tail", "image_list", "image_url", "img_url", "first_frame_url", "last_frame_url", "video_list", "element_list", "videos", "reference_videos", "reference_video", "reference_video_url", "video_url", "audio_url", "seconds", "size"); err != nil {
		return nil, metadata, err
	}
	images, imagesPresent, err := videoProtocolImages(values)
	if err != nil {
		return nil, metadata, err
	}
	if content, exists := values["content"]; exists {
		if !gjson.ParseBytes(content).IsArray() || len(gjson.ParseBytes(content).Array()) == 0 {
			return nil, metadata, videoProtocolError("content must be a non-empty array")
		}
		if _, hasPrompt := values["prompt"]; hasPrompt || (imagesPresent && len(images) > 0) {
			return nil, metadata, videoProtocolError("Use content or prompt/images, not both")
		}
	} else {
		prompt := gjson.ParseBytes(values["prompt"])
		if prompt.Type != gjson.String || strings.TrimSpace(prompt.String()) == "" {
			return nil, metadata, videoProtocolError("A text prompt or native content is required")
		}
		content := []any{map[string]any{"type": "text", "text": prompt.String()}}
		for _, image := range images {
			role := "reference_image"
			if len(images) == 1 {
				role = "first_frame"
			}
			content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{"url": image}, "role": role})
		}
		videoProtocolPut(values, "content", content)
		delete(values, "prompt")
	}
	if err := videoProtocolCanonicalResolution(values, endpoint == VideoEndpointMiniMax); err != nil {
		return nil, metadata, err
	}
	if endpoint == VideoEndpointMiniMax {
		if _, exists := values["resolution"]; !exists {
			return nil, metadata, videoProtocolError("MiniMax official V2 requires resolution")
		}
		if raw, exists := values["watermark"]; exists {
			if _, set := values["aigc_watermark"]; set {
				return nil, metadata, videoProtocolError("Use only one watermark parameter")
			}
			values["aigc_watermark"] = raw
			delete(values, "watermark")
		}
	}
	body, err := json.Marshal(values)
	if err != nil {
		return nil, metadata, err
	}
	metadata, err = ParseVideoEndpointRequestMetadata(body, "", endpoint)
	if videoProtocolHasUnknownMedia(gjson.GetBytes(body, "content")) {
		metadata.ReferenceImageCount = nil
	}
	if err == nil && gjson.GetBytes(body, "duration").Exists() && !(endpoint == VideoEndpointSeedance && metadata.DurationSeconds == -1) {
		_, err = videoProtocolPositiveSeconds(gjson.GetBytes(body, "duration"), false)
	}
	return body, metadata, err
}

// Wan 保留原生 input/parameters，并将明确的平铺参数移入对应对象。
func prepareVideoOfficialWan(values map[string]json.RawMessage) ([]byte, VideoRequestMetadata, error) {
	var metadata VideoRequestMetadata
	if err := videoProtocolReject(values, "content", "contents", "settings", "input_reference", "image", "image_tail", "image_list", "image_url", "video_list", "element_list", "seconds", "size", "media", "video", "videos", "video_url", "reference_videos", "reference_video", "reference_video_url", "audio_url"); err != nil {
		return nil, metadata, err
	}
	input, err := videoProtocolObject(values, "input")
	if err != nil {
		return nil, metadata, err
	}
	parameters, err := videoProtocolObject(values, "parameters")
	if err != nil {
		return nil, metadata, err
	}
	if err = videoProtocolMove(values, input, "prompt", "prompt"); err != nil {
		return nil, metadata, err
	}
	for _, key := range []string{"img_url", "first_frame_url", "last_frame_url"} {
		if err = videoProtocolMove(values, input, key, key); err != nil {
			return nil, metadata, err
		}
	}
	for _, key := range []string{"duration", "resolution", "ratio"} {
		if err = videoProtocolMove(values, parameters, key, key); err != nil {
			return nil, metadata, err
		}
	}
	images, present, err := videoProtocolImages(values)
	if err != nil || len(images) > 1 {
		return nil, metadata, videoProtocolError("Use native Wan input.media for multiple reference images")
	}
	if present && len(images) > 0 {
		for _, key := range []string{"img_url", "media", "first_frame_url", "last_frame_url"} {
			if _, exists := input[key]; exists {
				return nil, metadata, videoProtocolError("Use only one Wan image representation")
			}
		}
		videoProtocolPut(input, "img_url", images[0])
	}
	if err = videoProtocolCanonicalResolution(parameters, true); err != nil {
		return nil, metadata, err
	}
	videoProtocolPut(values, "input", input)
	videoProtocolPut(values, "parameters", parameters)
	body, err := json.Marshal(values)
	if err != nil {
		return nil, metadata, err
	}
	metadata, err = ParseVideoEndpointRequestMetadata(body, "", VideoEndpointWan)
	for _, key := range []string{"input.media", "input.content"} {
		if value := gjson.GetBytes(body, key); value.Exists() && videoProtocolHasUnknownMedia(value) {
			metadata.ReferenceImageCount = nil
		}
	}
	if err == nil && gjson.GetBytes(body, "parameters.duration").Exists() {
		_, err = videoProtocolPositiveSeconds(gjson.GetBytes(body, "parameters.duration"), false)
	}
	return body, metadata, err
}

// Kling 新协议以 URL 指定模型，settings 与 contents 是唯一有效的报价输入。
func prepareVideoOfficialKling(values map[string]json.RawMessage, target VideoUpstreamTarget) ([]byte, VideoRequestMetadata, error) {
	var metadata VideoRequestMetadata
	if err := ValidateVideoProtocolTarget(target); err != nil {
		return nil, metadata, err
	}
	if err := videoProtocolReject(values, "input", "parameters", "content", "media", "input_reference", "model_name", "seconds", "size", "video_url", "videos", "reference_videos", "reference_video", "reference_video_url", "audio_url", "video_list", "element_list", "image_url", "img_url", "first_frame_url", "last_frame_url"); err != nil {
		return nil, metadata, err
	}
	settings, err := videoProtocolObject(values, "settings")
	if err != nil {
		return nil, metadata, err
	}
	for _, key := range []string{"duration", "resolution", "aspect_ratio"} {
		if err = videoProtocolMove(values, settings, key, key); err != nil {
			return nil, metadata, err
		}
	}
	if err = videoProtocolMove(values, settings, "ratio", "aspect_ratio"); err != nil {
		return nil, metadata, err
	}
	if err = videoProtocolCanonicalResolution(settings, false); err != nil {
		return nil, metadata, err
	}
	if value := videoProtocolRoot(settings).Get("duration"); value.Exists() {
		metadata.DurationSeconds, err = videoProtocolPositiveSeconds(value, false)
		if err != nil {
			return nil, metadata, err
		}
	}
	metadata.Resolution = videoProtocolRoot(settings).Get("resolution").String()
	images, present, err := videoProtocolImages(values)
	if err != nil {
		return nil, metadata, err
	}
	textOperation := strings.HasPrefix(target.CreatePath, "/text-to-video/")
	imageOperation := strings.HasPrefix(target.CreatePath, "/image-to-video/")
	if textOperation {
		if len(images) > 0 {
			return nil, metadata, videoProtocolError("Kling text-to-video does not accept reference images")
		}
		if err := videoProtocolReject(values, "contents", "image", "image_tail", "image_list"); err != nil {
			return nil, metadata, err
		}
		if prompt := videoProtocolRoot(values).Get("prompt"); prompt.Type != gjson.String || strings.TrimSpace(prompt.String()) == "" {
			return nil, metadata, videoProtocolError("Kling text-to-video requires prompt")
		}
		zero := 0
		metadata.ReferenceImageCount = &zero
	} else {
		if _, exists := values["contents"]; exists {
			if _, hasPrompt := values["prompt"]; hasPrompt || (present && len(images) > 0) {
				return nil, metadata, videoProtocolError("Use native contents or prompt/images, not both")
			}
			if err := videoProtocolReject(values, "image", "image_tail", "image_list"); err != nil {
				return nil, metadata, err
			}
		} else {
			if !imageOperation {
				return nil, metadata, videoProtocolError("Kling omni-video requires native contents")
			}
			if err := videoProtocolReject(values, "image_list"); err != nil {
				return nil, metadata, err
			}
			root := videoProtocolRoot(values)
			imageURL := root.Get("image")
			if len(images) > 1 || (len(images) > 0 && imageURL.Exists()) {
				return nil, metadata, videoProtocolError("Kling first frame must be specified once")
			}
			firstFrame := ""
			if len(images) == 1 {
				firstFrame = images[0]
			} else if imageURL.Type == gjson.String {
				firstFrame = imageURL.String()
			}
			if strings.TrimSpace(firstFrame) == "" {
				return nil, metadata, videoProtocolError("Kling image-to-video requires a first frame")
			}
			contents := []any{}
			if prompt := root.Get("prompt"); prompt.Exists() {
				if prompt.Type != gjson.String {
					return nil, metadata, videoProtocolError("prompt must be a string")
				}
				contents = append(contents, map[string]string{"type": "prompt", "text": prompt.String()})
			}
			contents = append(contents, map[string]string{"type": "first_frame", "url": firstFrame})
			if lastFrame := root.Get("image_tail"); lastFrame.Exists() {
				if lastFrame.Type != gjson.String || strings.TrimSpace(lastFrame.String()) == "" {
					return nil, metadata, videoProtocolError("Kling last frame must be a non-empty URL")
				}
				contents = append(contents, map[string]string{"type": "last_frame", "url": lastFrame.String()})
			}
			videoProtocolPut(values, "contents", contents)
			for _, key := range []string{"prompt", "image", "image_tail"} {
				delete(values, key)
			}
		}
		count, hasVideo, err := videoOfficialKlingContentCount(gjson.ParseBytes(values["contents"]), imageOperation)
		if err != nil {
			return nil, metadata, err
		}
		metadata.ReferenceImageCount, metadata.HasReferenceVideo = count, hasVideo
	}
	videoProtocolPut(values, "settings", settings)
	delete(values, "model")
	body, err := json.Marshal(values)
	return body, metadata, err
}

// 元素 ID 的内部素材数量不可推断；遇到元素保留未知张数而不是当作免费图片。
func videoOfficialKlingContentCount(contents gjson.Result, requireFirstFrame bool) (*int, bool, error) {
	if !contents.IsArray() || len(contents.Array()) == 0 {
		return nil, false, videoProtocolError("Kling contents must be a non-empty array")
	}
	count, first, last := 0, 0, 0
	hasVideo, unknown := false, false
	for _, item := range contents.Array() {
		if !item.IsObject() || item.Get("type").Type != gjson.String {
			return nil, false, videoProtocolError("Kling contents require typed objects")
		}
		switch item.Get("type").String() {
		case "prompt":
			if item.Get("text").Type != gjson.String {
				return nil, false, videoProtocolError("Kling prompt content requires text")
			}
		case "image", "reference_image", "first_frame", "last_frame":
			if item.Get("url").Type != gjson.String || strings.TrimSpace(item.Get("url").String()) == "" {
				return nil, false, videoProtocolError("Kling image content requires URL")
			}
			count++
			if item.Get("type").String() == "first_frame" {
				first++
			}
			if item.Get("type").String() == "last_frame" {
				last++
			}
		case "video", "reference_video":
			hasVideo = true
		case "audio", "voice":
		case "element":
			unknown = true
		default:
			unknown = true
		}
	}
	if requireFirstFrame && (first != 1 || last > 1 || hasVideo) {
		return nil, false, videoProtocolError("Kling image-to-video requires one first frame and at most one last frame")
	}
	if unknown {
		return nil, hasVideo, nil
	}
	return &count, hasVideo, nil
}
