package service

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	VideoOpenAIProfilePassthrough = "passthrough"
	VideoOpenAIProfileMetasoH3    = "metaso_h3"
)

// 配置必须显式选择，不根据主机或模型名称推断供应商。
func normalizeVideoOpenAIProfile(profile string) (string, error) {
	switch profile {
	case "", VideoOpenAIProfilePassthrough, VideoOpenAIProfileMetasoH3:
		return profile, nil
	default:
		return "", fmt.Errorf("unsupported video_openai_profile")
	}
}

// PrepareVideoOpenAIRequest 将计费维度与实际发送参数对齐，默认不改写任何请求字节。
// @project-doc docs/interfaces/video_upstream.md#video_protocols
func PrepareVideoOpenAIRequest(body []byte, modelPath, profile string) ([]byte, VideoRequestMetadata, error) {
	var metadata VideoRequestMetadata
	if _, err := normalizeVideoOpenAIProfile(profile); err != nil {
		return nil, metadata, infraerrors.BadRequest("VIDEO_ACCOUNT_INVALID", err.Error())
	}
	if _, err := ParseVideoRequestModel(body, modelPath); err != nil {
		return nil, metadata, err
	}
	root := gjson.ParseBytes(body)
	if !videoOpenAIKeysCanonical(root, "seconds", "input_reference") {
		return nil, metadata, videoOpenAIParameterError("OpenAI video parameter names must be canonical")
	}
	if profile == VideoOpenAIProfileMetasoH3 {
		var err error
		body, err = prepareMetasoH3Request(body)
		if err != nil {
			return nil, metadata, err
		}
		root = gjson.ParseBytes(body)
	}
	// 原兼容解析仍负责模型、视频输入和旧图片表示；独立移除维度后处理新旧别名。
	legacy := body
	for _, path := range []string{"resolution", "size", "duration", "parameters.resolution", "parameters.size", "parameters.duration"} {
		var err error
		legacy, err = sjson.DeleteBytes(legacy, path)
		if err != nil {
			return nil, metadata, err
		}
	}
	var err error
	metadata, err = ParseVideoEndpointRequestMetadata(legacy, modelPath, VideoEndpointCompat)
	if err != nil {
		return nil, metadata, err
	}
	metadata.DurationSeconds, _, err = videoOpenAIDuration(root, []string{"seconds", "duration", "parameters.duration"}, false)
	if err != nil {
		return nil, metadata, err
	}
	metadata.Resolution, metadata.Size, err = videoOpenAIResolution(root, []string{"resolution", "size", "parameters.resolution", "parameters.size"}, profile)
	if err != nil {
		return nil, metadata, err
	}
	if reference := root.Get("input_reference"); reference.Exists() {
		count := videoOpenAIReferenceCount(reference)
		// 不能猜测厂商会合并还是覆盖不同图片表示；保留未知让启用附加费的报价拒绝。
		if metadata.ReferenceImageCount == nil || count == nil || *metadata.ReferenceImageCount != 0 {
			metadata.ReferenceImageCount = nil
		} else {
			metadata.ReferenceImageCount = count
		}
	}
	return body, metadata, nil
}

func videoOpenAIParameterError(message string) error {
	return infraerrors.BadRequest("VIDEO_INVALID_OPENAI_PARAMETERS", message)
}

// 秒数使用有界正整数；旧 duration 自动时长语义只在未提供 seconds 时保留。
func videoOpenAIDuration(root gjson.Result, paths []string, response bool) (float64, bool, error) {
	var seconds float64
	present := false
	for _, path := range paths {
		value := root.Get(path)
		if !value.Exists() {
			continue
		}
		isSeconds := path == "seconds"
		n := value.Float()
		valid := value.Type == gjson.Number && !math.IsNaN(n) && !math.IsInf(n, 0) && n <= math.MaxInt32
		if isSeconds && value.Type == gjson.String {
			text := value.String()
			parsed, err := strconv.ParseInt(text, 10, 32)
			valid = err == nil && parsed > 0 && strconv.FormatInt(parsed, 10) == text
			n = float64(parsed)
		}
		if isSeconds {
			valid = valid && n > 0 && math.Trunc(n) == n
		} else if response {
			valid = valid && n > 0
		}
		if !valid {
			return 0, false, videoOpenAIParameterError("Video duration must be finite and within supported bounds")
		}
		if present && seconds != n {
			return 0, false, infraerrors.BadRequest("VIDEO_METADATA_CONFLICT", "Conflicting video duration fields")
		}
		seconds, present = n, true
	}
	return seconds, present, nil
}

// size 保留精确像素档位，只有显式 H3 配置才把四种已验证尺寸映射到供应商价档。
func videoOpenAIResolution(root gjson.Result, paths []string, profile string) (string, string, error) {
	resolution, size := "", ""
	for _, path := range paths {
		value := root.Get(path)
		if !value.Exists() {
			continue
		}
		if value.Type != gjson.String || strings.TrimSpace(value.String()) == "" {
			return "", "", videoOpenAIParameterError("Video resolution or size must be a non-empty string")
		}
		current, err := NormalizeVideoPriceResolution(value.String())
		if err != nil {
			return "", "", videoOpenAIParameterError("Video resolution or size is invalid")
		}
		if strings.Contains(current, "x") {
			if size != "" && size != current {
				return "", "", infraerrors.BadRequest("VIDEO_METADATA_CONFLICT", "Conflicting video sizes")
			}
			size = current
			if profile == VideoOpenAIProfileMetasoH3 {
				current, _ = metasoH3SizeTier(current)
				if current == "" {
					return "", "", videoOpenAIParameterError("Unsupported Metaso H3 size")
				}
			}
		}
		if profile == VideoOpenAIProfileMetasoH3 && current != "768p" && current != "2k" {
			return "", "", videoOpenAIParameterError("Metaso H3 resolution must be 768p or 2k")
		}
		if resolution != "" && resolution != current {
			return "", "", infraerrors.BadRequest("VIDEO_METADATA_CONFLICT", "Conflicting video resolution fields")
		}
		resolution = current
	}
	return resolution, size, nil
}

// 秘塔标注的 768P/2K 是产品档位，不能用像素短边推导价档或按数学比例重算尺寸。
func metasoH3SizeTier(size string) (string, string) {
	switch size {
	case "1280x720":
		return "768p", "16:9"
	case "720x1280":
		return "768p", "9:16"
	case "1792x1024":
		return "2k", "16:9"
	case "1024x1792":
		return "2k", "9:16"
	}
	return "", ""
}

func metasoH3TierSize(resolution, ratio string) string {
	for _, size := range []string{"1280x720", "720x1280", "1792x1024", "1024x1792"} {
		tier, orientation := metasoH3SizeTier(size)
		if tier == resolution && orientation == ratio {
			return size
		}
	}
	return ""
}

// 仅支持已经验证的单图 JSON 形式；未知结构不会伪装成零张图片。
func videoOpenAIReferenceCount(reference gjson.Result) *int {
	if !reference.IsObject() || !videoOpenAIKeysCanonical(reference, "image_url") {
		return nil
	}
	image := reference.Get("image_url")
	if image.Type != gjson.String || strings.TrimSpace(image.String()) == "" {
		return nil
	}
	count := 1
	return &count
}

// 已知别名要唯一且大小写规范，忽略无关扩展字段而不递归猜测供应商合同。
func videoOpenAIKeysCanonical(root gjson.Result, fields ...string) bool {
	if !root.IsObject() {
		return false
	}
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		seen[field] = false
	}
	valid := true
	root.ForEach(func(key, _ gjson.Result) bool {
		name := key.String()
		folded := strings.ToLower(name)
		if used, known := seen[folded]; known {
			if used || name != folded {
				valid = false
				return false
			}
			seen[folded] = true
		}
		return true
	})
	return valid
}

// H3 只改写显式支持的旧字段，任何多图、方向歧义或不一致别名都在发送之前拒绝。
func prepareMetasoH3Request(body []byte) ([]byte, error) {
	root := gjson.ParseBytes(body)
	if !videoOpenAIKeysCanonical(root, "seconds", "input_reference", "images", "image_urls", "ratio") {
		return nil, videoOpenAIParameterError("Metaso H3 parameter names must be canonical")
	}
	for _, field := range []string{"parameters", "input", "content", "media", "image", "image_tail", "image_list", "img_url", "first_frame_url", "last_frame_url", "videos", "reference_videos", "video", "video_url", "reference_video", "reference_video_url", "audios", "audio", "audio_url"} {
		if root.Get(field).Exists() {
			return nil, videoOpenAIParameterError("Metaso H3 OpenAI profile only supports prompt and a single reference image")
		}
	}
	seconds, hasSeconds, err := videoOpenAIDuration(root, []string{"seconds", "duration"}, false)
	if err != nil {
		return nil, err
	}
	if hasSeconds && (seconds <= 0 || math.Trunc(seconds) != seconds) {
		return nil, videoOpenAIParameterError("Metaso H3 duration must be a positive integer")
	}
	resolution, size, err := videoOpenAIResolution(root, []string{"resolution", "size"}, VideoOpenAIProfileMetasoH3)
	if err != nil {
		return nil, err
	}
	ratio := ""
	if value := root.Get("ratio"); value.Exists() {
		if value.Type != gjson.String || (value.String() != "16:9" && value.String() != "9:16") {
			return nil, videoOpenAIParameterError("Metaso H3 ratio must be 16:9 or 9:16")
		}
		ratio = value.String()
	}
	if size != "" {
		_, orientation := metasoH3SizeTier(size)
		if ratio != "" && orientation != ratio {
			return nil, infraerrors.BadRequest("VIDEO_METADATA_CONFLICT", "Video size and ratio disagree")
		}
	} else if resolution != "" || ratio != "" {
		size = metasoH3TierSize(resolution, ratio)
		if size == "" {
			return nil, videoOpenAIParameterError("Metaso H3 requires both resolution and ratio, or an explicit size")
		}
	}
	imageURL, imagePresent := "", false
	for _, field := range []string{"images", "image_urls", "input_reference"} {
		value := root.Get(field)
		if !value.Exists() {
			continue
		}
		current := ""
		if field == "input_reference" {
			if videoOpenAIReferenceCount(value) == nil {
				return nil, videoOpenAIParameterError("input_reference must contain one image_url")
			}
			current = value.Get("image_url").String()
		} else {
			if !value.IsArray() || len(value.Array()) > 1 {
				return nil, videoOpenAIParameterError("Metaso H3 supports at most one reference image")
			}
			if items := value.Array(); len(items) == 1 {
				if items[0].Type != gjson.String || strings.TrimSpace(items[0].String()) == "" {
					return nil, videoOpenAIParameterError("Metaso H3 reference image must be a non-empty URL or data URL")
				}
				current = items[0].String()
			}
		}
		if imagePresent && imageURL != current {
			return nil, infraerrors.BadRequest("VIDEO_METADATA_CONFLICT", "Conflicting reference image fields")
		}
		imageURL, imagePresent = current, true
	}
	// RawMessage 保留未知扩展值的 JSON 精度，转换仅发生在明确启用该配置的请求。
	var values map[string]json.RawMessage
	if err := json.Unmarshal(body, &values); err != nil {
		return nil, err
	}
	for _, field := range []string{"duration", "resolution", "ratio", "images", "image_urls"} {
		delete(values, field)
	}
	if hasSeconds {
		values["seconds"], _ = json.Marshal(seconds)
	}
	if size != "" {
		values["size"], _ = json.Marshal(size)
	}
	if imageURL != "" && !root.Get("input_reference").Exists() {
		values["input_reference"], _ = json.Marshal(map[string]string{"image_url": imageURL})
	}
	return json.Marshal(values)
}

// 终态元数据有冲突时保留任务状态，并交给状态机待核对，不回退请求预估秒数收费。
func applyVideoOpenAIResponseMetadata(out *VideoUpstreamResponse, root gjson.Result, profile string) {
	if _, err := normalizeVideoOpenAIProfile(profile); err != nil {
		out.MetadataError = "Unknown frozen OpenAI video profile"
		return
	}
	if !root.IsObject() {
		return
	}
	for _, object := range []gjson.Result{root, root.Get("video"), root.Get("output"), root.Get("usage"), root.Get("data"), root.Get("task_result"), root.Get("task_result.videos.0")} {
		if object.IsObject() && !videoOpenAIKeysCanonical(object, "seconds", "duration", "resolution", "size", "video", "output", "usage", "data", "task_result", "videos") {
			out.MetadataError = "Ambiguous OpenAI video metadata"
			return
		}
	}
	seconds, present, err := videoOpenAIDuration(root, []string{"seconds", "duration", "video.duration", "output.duration", "usage.duration", "data.duration", "task_result.videos.0.duration"}, true)
	if err != nil {
		out.MetadataError = "Invalid or conflicting OpenAI video duration"
		return
	}
	resolution, size, err := videoOpenAIResolution(root, []string{"size", "resolution", "video.resolution", "output.resolution"}, profile)
	if err != nil {
		out.MetadataError = "Invalid or conflicting OpenAI video resolution"
		return
	}
	if present {
		out.Metadata.DurationSeconds = seconds
	}
	out.Metadata.Resolution, out.Metadata.Size = resolution, size
}
