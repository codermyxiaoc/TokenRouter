package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/tidwall/gjson"
)

// ParseVideoRequestMetadata 对未知兼容厂商拒绝计费维度冲突，不能猜测字段优先级。
func ParseVideoRequestMetadata(body []byte, modelPath string) (VideoRequestMetadata, error) {
	return ParseVideoEndpointRequestMetadata(body, modelPath, VideoEndpointCompat)
}

// ParseVideoEndpointRequestMetadata 按已选定的协议优先级提取有效参数，不改变原始JSON。
func ParseVideoEndpointRequestMetadata(body []byte, modelPath string, endpoint VideoEndpoint) (VideoRequestMetadata, error) {
	// OpenAI 视频 JSON 中继与 compat 共用保守维度校验，避免混用结构绕过计费冲突检查。
	if endpoint == VideoEndpointOpenAIVideos {
		endpoint = VideoEndpointCompat
	}
	out, err := ParseVideoRequestModel(body, modelPath)
	if err != nil {
		return out, err
	}
	root := gjson.ParseBytes(body)
	parameters, media := root, root
	if endpoint == VideoEndpointWan {
		if value := root.Get("parameters"); value.Exists() {
			if !value.IsObject() {
				return out, infraerrors.BadRequest("VIDEO_INVALID_PARAMETERS", "parameters must be an object")
			}
			parameters = value
		}
		if value := root.Get("input"); value.Exists() {
			if !value.IsObject() {
				return out, infraerrors.BadRequest("VIDEO_INVALID_INPUT", "input must be an object")
			}
			media = value
		}
	}
	resolutionPaths := []string{"resolution", "size"}
	durationPaths := []string{"duration"}
	if endpoint == VideoEndpointCompat {
		resolutionPaths = []string{"resolution", "size", "parameters.resolution", "parameters.size"}
		durationPaths = []string{"duration", "parameters.duration"}
	}
	for _, path := range resolutionPaths {
		value := parameters.Get(path)
		if !value.Exists() {
			continue
		}
		if value.Type != gjson.String || strings.TrimSpace(value.String()) == "" {
			return out, infraerrors.BadRequest("VIDEO_RESOLUTION_INVALID", "resolution must be a non-empty string")
		}
		resolution := strings.TrimSpace(value.String())
		if out.Resolution != "" && !strings.EqualFold(out.Resolution, resolution) {
			return out, infraerrors.BadRequest("VIDEO_METADATA_CONFLICT", "Conflicting resolution fields")
		}
		out.Resolution = resolution
	}
	durationSet := false
	for _, path := range durationPaths {
		value := parameters.Get(path)
		if !value.Exists() {
			continue
		}
		if value.Type != gjson.Number || math.IsInf(value.Float(), 0) || math.IsNaN(value.Float()) {
			return out, infraerrors.BadRequest("VIDEO_DURATION_INVALID", "duration must be a finite number")
		}
		if durationSet && out.DurationSeconds != value.Float() {
			return out, infraerrors.BadRequest("VIDEO_METADATA_CONFLICT", "Conflicting duration fields")
		}
		out.DurationSeconds = value.Float()
		durationSet = true
	}
	flat := videoFlatReference(media)
	content, hasContent := videoContentReference(media)
	if endpoint == VideoEndpointMiniMax || endpoint == VideoEndpointSeedance {
		// 这两类原生协议只把content作为多模态正文，未知media扩展不能改变计费。
		value := root.Get("content")
		content, hasContent = videoTypedReference(value), value.Exists()
	}
	out.HasReferenceVideo = flat || content
	if (endpoint == VideoEndpointMiniMax || endpoint == VideoEndpointSeedance) && hasContent {
		out.HasReferenceVideo = content
	}
	if endpoint == VideoEndpointWan && hasContent && videoHasFlatMedia(media) && flat != content {
		return out, infraerrors.BadRequest("VIDEO_METADATA_CONFLICT", "Conflicting native reference video inputs")
	}
	if endpoint == VideoEndpointCompat {
		// 不知道兼容上游将采用哪个结构时，只允许一致的计费维度。
		if hasContent && videoHasFlatMedia(media) && flat != content {
			return out, infraerrors.BadRequest("VIDEO_METADATA_CONFLICT", "Conflicting reference video inputs")
		}
		if input := root.Get("input"); input.Exists() {
			if !input.IsObject() {
				return out, infraerrors.BadRequest("VIDEO_INVALID_INPUT", "input must be an object")
			}
			inputContent, inputHasContent := videoContentReference(input)
			inputFlat := videoFlatReference(input)
			if inputHasContent && videoHasFlatMedia(input) && inputFlat != inputContent {
				return out, infraerrors.BadRequest("VIDEO_METADATA_CONFLICT", "Conflicting nested reference video inputs")
			}
			inputVideo := inputFlat || inputContent
			if (hasContent || videoHasFlatMedia(root)) && out.HasReferenceVideo != inputVideo {
				return out, infraerrors.BadRequest("VIDEO_METADATA_CONFLICT", "Conflicting reference video structures")
			}
			out.HasReferenceVideo = inputVideo
		}
	}
	out.ReferenceImageCount = videoReferenceImageCount(root, endpoint)
	return out, nil
}

// 图片计数不改写正文；未知数量只影响启用图片附加费的报价，不改变旧请求的解析准入。
func videoReferenceImageCount(root gjson.Result, endpoint VideoEndpoint) *int {
	if endpoint == VideoEndpointMiniMax || endpoint == VideoEndpointSeedance {
		if content := root.Get("content"); content.Exists() {
			return videoTypedImageCount(content)
		}
		return videoImageCountInScope(root, endpoint)
	}
	if endpoint == VideoEndpointWan {
		if input := root.Get("input"); input.Exists() {
			return videoImageCountInScope(input, endpoint)
		}
		return videoImageCountInScope(root, endpoint)
	}
	count := videoImageCountInScope(root, endpoint)
	if endpoint == VideoEndpointCompat {
		if input := root.Get("input"); input.Exists() {
			inputCount := videoImageCountInScope(input, endpoint)
			// 未知兼容厂商可能覆盖或合并两套输入；不能把任何一套擅自当作免费扩展。
			if count == nil || inputCount == nil || *count != 0 {
				return nil
			}
			return inputCount
		}
	}
	return count
}

// 每个图片表示只扫描协议白名单字段，不递归搜索扩展、输出或音视频对象。
func videoImageCountInScope(root gjson.Result, endpoint VideoEndpoint) *int {
	if !root.IsObject() {
		return nil
	}
	fields := []string{"images", "image_urls"}
	if endpoint == VideoEndpointCompat || endpoint == VideoEndpointKling {
		fields = append(fields, "image", "image_tail", "image_list")
	}
	if endpoint == VideoEndpointCompat || endpoint == VideoEndpointWan {
		fields = append(fields, "img_url", "first_frame_url", "last_frame_url", "content", "media")
	}
	if videoImageKeyCaseAmbiguous(root, fields) {
		return nil
	}
	var groups []*int
	if count, present := videoImageAliasCount(root, []string{"images", "image_urls"}, true); present {
		groups = append(groups, count)
	}
	if endpoint == VideoEndpointCompat || endpoint == VideoEndpointKling {
		if value := root.Get("image_list"); value.Exists() {
			groups = append(groups, videoImageArrayCount(value, false))
		}
		if count, present := videoFrameImageCount(root, []string{"image"}, []string{"image_tail"}); present {
			groups = append(groups, count)
		}
	}
	if endpoint == VideoEndpointCompat || endpoint == VideoEndpointWan {
		if count, present := videoFrameImageCount(root, []string{"img_url", "first_frame_url"}, []string{"last_frame_url"}); present {
			groups = append(groups, count)
		}
		for _, path := range []string{"content", "media"} {
			if value := root.Get(path); value.Exists() {
				groups = append(groups, videoTypedImageCount(value))
			}
		}
	}
	count := 0
	for _, group := range groups {
		if group == nil {
			return nil
		}
		count += *group
	}
	// 只有 images/image_urls 的别名关系已知；其它混合表示的合并或覆盖规则不能猜测。
	if len(groups) > 1 && count > 0 {
		return nil
	}
	return &count
}

// 明确的首尾帧是两张输入，即使使用同一 URL 也分别计数。
func videoFrameImageCount(root gjson.Result, first, last []string) (*int, bool) {
	firstCount, firstPresent := videoImageAliasCount(root, first, false)
	lastCount, lastPresent := videoImageAliasCount(root, last, false)
	if firstCount == nil || lastCount == nil {
		return nil, firstPresent || lastPresent
	}
	count := *firstCount + *lastCount
	return &count, firstPresent || lastPresent
}

// 同名别名只计一次；不同数量无法证明哪一套会生效，保留未知状态。
func videoImageAliasCount(root gjson.Result, paths []string, array bool) (*int, bool) {
	count, present := 0, false
	for _, path := range paths {
		value := root.Get(path)
		if !value.Exists() {
			continue
		}
		var current *int
		if array {
			current = videoImageArrayCount(value, false)
		} else {
			current = videoSingleImageCount(value)
		}
		if current == nil || (present && count != *current) {
			return nil, true
		}
		count, present = *current, true
	}
	return &count, present
}

func videoTypedImageCount(value gjson.Result) *int {
	if value.Type == gjson.Null {
		return nil
	}
	return videoImageArrayCount(value, true)
}

// 一个数组元素代表一张输入图，重复 URL 保持重复次数，绝不按链接去重。
func videoImageArrayCount(value gjson.Result, typed bool) *int {
	count := 0
	if value.Type == gjson.Null {
		return &count
	}
	if !value.IsArray() {
		return nil
	}
	for _, item := range value.Array() {
		if typed {
			if !item.IsObject() {
				return nil
			}
			switch strings.ToLower(strings.TrimSpace(item.Get("type").String())) {
			case "image", "image_url", "reference_image", "first_frame", "last_frame":
			case "text", "video", "video_url", "reference_video", "audio", "audio_url", "reference_audio", "file", "link":
				continue
			default:
				// 未识别类型不扫描内部图片字段，避免把扩展或输出误当作输入。
				continue
			}
		}
		current := videoSingleImageCount(item)
		if current == nil {
			return nil
		}
		count += *current
	}
	return &count
}

// 图片值只接受非空字符串或明确的 URL 对象，不从任意嵌套字段推断媒体类型。
func videoSingleImageCount(value gjson.Result) *int {
	count := 0
	if value.Type == gjson.Null {
		return &count
	}
	if value.Type == gjson.String {
		if strings.TrimSpace(value.String()) != "" {
			count = 1
		}
		return &count
	}
	if !value.IsObject() || videoImageKeyCaseAmbiguous(value, []string{"url", "image_url", "role", "type"}) {
		return nil
	}
	for _, field := range []string{"type", "role"} {
		if marker := value.Get(field); marker.Exists() && marker.Type != gjson.Null && marker.String() != "" {
			switch strings.ToLower(strings.TrimSpace(marker.String())) {
			case "image", "image_url", "reference_image", "first_frame", "last_frame", "first", "last", "end_frame", "tail":
			default:
				return nil
			}
		}
	}
	for _, path := range []string{"url", "image_url"} {
		url := value.Get(path)
		if url.IsObject() {
			if videoImageKeyCaseAmbiguous(url, []string{"url"}) {
				return nil
			}
			url = url.Get("url")
		}
		if url.Type == gjson.String && strings.TrimSpace(url.String()) != "" {
			count = 1
			return &count
		}
	}
	return nil
}

// 新计费字段的大小写歧义不改变旧请求准入，仅令可收费数量保持未知。
func videoImageKeyCaseAmbiguous(root gjson.Result, fields []string) bool {
	ambiguous := false
	root.ForEach(func(key, _ gjson.Result) bool {
		for _, field := range fields {
			if strings.EqualFold(key.String(), field) && key.String() != field {
				ambiguous = true
				return false
			}
		}
		return true
	})
	return ambiguous
}

func videoFlatReference(root gjson.Result) bool {
	for _, path := range []string{"videos", "reference_videos", "video", "video_url", "reference_video", "reference_video_url"} {
		if videoInputHasURL(root.Get(path)) {
			return true
		}
	}
	return false
}

func videoHasFlatMedia(root gjson.Result) bool {
	for _, path := range []string{"videos", "reference_videos", "video", "video_url", "reference_video", "reference_video_url", "images", "image_urls", "audios"} {
		if root.Get(path).Exists() {
			return true
		}
	}
	return false
}

func videoContentReference(root gjson.Result) (bool, bool) {
	hasVideo, present := false, false
	for _, path := range []string{"content", "media"} {
		value := root.Get(path)
		present = present || value.Exists()
		hasVideo = hasVideo || videoTypedReference(value)
	}
	return hasVideo, present
}

func videoTypedReference(value gjson.Result) bool {
	for _, item := range value.Array() {
		typ := item.Get("type").String()
		if (typ == "video_url" || typ == "video" || typ == "reference_video") && (videoInputHasURL(item.Get("video_url")) || videoInputHasURL(item.Get("url"))) {
			return true
		}
	}
	return false
}

// validateVideoJSONKeys 阻止不同JSON解析器对重复键取首值/末值造成计费与实际生成参数分叉。
func validateVideoJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var visit func(int) error
	visit = func(depth int) error {
		if depth > 64 {
			return fmt.Errorf("JSON nesting is too deep")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delim == '{' {
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key := keyToken.(string)
				folded := strings.ToLower(key)
				if seen[folded] {
					return fmt.Errorf("duplicate JSON key")
				}
				seen[folded] = true
				switch folded {
				case "model", "resolution", "size", "duration", "content", "input", "parameters", "videos", "reference_videos", "video", "video_url", "reference_video", "reference_video_url", "type", "url", "media":
					if key != folded {
						return fmt.Errorf("noncanonical video parameter name")
					}
				}
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
		} else if delim == '[' {
			for decoder.More() {
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
		} else {
			return fmt.Errorf("invalid JSON container")
		}
		_, err = decoder.Token()
		return err
	}
	if err := visit(0); err != nil {
		return infraerrors.BadRequest("VIDEO_JSON_AMBIGUOUS", err.Error())
	}
	return nil
}
