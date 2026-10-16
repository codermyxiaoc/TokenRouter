package service

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// 可美的兼容入口与各原生入口共享媒体角色，但不能根据域名或模型名猜测要走的协议。
// @project-doc docs/interfaces/video_upstream.md#video_adapters
func prepareVideoKemeiRequest(values map[string]json.RawMessage, target VideoUpstreamTarget) ([]byte, VideoRequestMetadata, error) {
	if err := validateVideoKemeiNames(videoProtocolRoot(values)); err != nil {
		return nil, VideoRequestMetadata{}, err
	}
	switch target.Endpoint {
	case VideoEndpointCompat:
		return prepareVideoKemeiCompat(values)
	case VideoEndpointMiniMax, VideoEndpointSeedance:
		return prepareVideoKemeiContent(values, target.Endpoint)
	case VideoEndpointWan:
		return prepareVideoKemeiWan(values)
	case VideoEndpointKling:
		return prepareVideoKemeiKling(values, target)
	case VideoEndpointVidu:
		return prepareVideoKemeiVidu(values, target)
	}
	return nil, VideoRequestMetadata{}, videoProtocolError("Unsupported Kemei video endpoint")
}

// 新方案额外校验供应商的多模态字段大小写，不改变既有官方方案的合同。
func validateVideoKemeiNames(root gjson.Result) error {
	var found error
	root.ForEach(func(key, value gjson.Result) bool {
		if root.IsObject() {
			switch strings.ToLower(key.String()) {
			case "videos", "audios", "files", "links", "sounds", "audio", "generate_audio", "url", "type", "duration", "resolution", "parameters", "input", "content", "model":
				if key.String() != strings.ToLower(key.String()) {
					found = videoProtocolError("Kemei video parameters must use canonical field names")
					return false
				}
			}
		}
		if value.IsObject() || value.IsArray() {
			found = validateVideoKemeiNames(value)
		}
		return found == nil
	})
	return found
}

type videoKemeiMedia struct {
	URL  string
	Role string
}

// URL 与 Base64 都作为原始素材值转发，不下载、转存、去重或更改图片字节。
func videoKemeiMediaItems(value gjson.Result, kind string) ([]videoKemeiMedia, error) {
	if !value.IsArray() {
		return nil, videoProtocolError(kind + " must be an array")
	}
	items := value.Array()
	result := make([]videoKemeiMedia, 0, len(items))
	for i, item := range items {
		media := videoKemeiMedia{}
		if item.Type == gjson.String {
			media.URL = item.String()
		} else if item.IsObject() {
			if url := item.Get("url"); url.Type == gjson.String {
				media.URL = url.String()
			}
			for _, key := range []string{"role", "type"} {
				if role := item.Get(key); role.Exists() {
					if role.Type != gjson.String || strings.TrimSpace(role.String()) == "" {
						return nil, videoProtocolError("Kemei media role must be a non-empty string")
					}
					canonical := videoKemeiRole(role.String())
					if media.Role != "" && media.Role != canonical {
						return nil, videoProtocolError("Conflicting Kemei media role and type")
					}
					media.Role = canonical
				}
			}
		}
		if strings.TrimSpace(media.URL) == "" {
			return nil, videoProtocolError("Kemei media requires a non-empty URL or Base64 string")
		}
		if media.Role == "" {
			media.Role = map[string]string{"videos": "reference_video", "audios": "reference_audio", "files": "file", "links": "link"}[kind]
			if kind == "images" {
				media.Role = "reference_image"
				if len(items) < 3 {
					media.Role = "first_frame"
					if i == 1 {
						media.Role = "last_frame"
					}
				}
			}
		}
		valid := (kind == "images" && (media.Role == "first_frame" || media.Role == "last_frame" || media.Role == "reference_image")) ||
			(kind == "videos" && media.Role == "reference_video") || (kind == "audios" && media.Role == "reference_audio") ||
			(kind == "files" && media.Role == "file") || (kind == "links" && media.Role == "link")
		if !valid {
			return nil, videoProtocolError("Unsupported Kemei media role in " + kind)
		}
		result = append(result, media)
	}
	return result, nil
}

func videoKemeiRole(role string) string {
	switch role {
	case "first":
		return "first_frame"
	case "last", "end_frame", "tail":
		return "last_frame"
	}
	return role
}

// 别名必须完全一致才允许合并，不能让图片计数与上游选中的正文不同。
func videoKemeiFlatMedia(values map[string]json.RawMessage) ([]videoKemeiMedia, bool, error) {
	if err := videoProtocolMove(values, values, "image_urls", "images"); err != nil {
		return nil, false, err
	}
	var result []videoKemeiMedia
	present := false
	for _, key := range []string{"images", "videos", "audios", "files", "links"} {
		if raw, exists := values[key]; exists {
			present = true
			items, err := videoKemeiMediaItems(gjson.ParseBytes(raw), key)
			if err != nil {
				return nil, false, err
			}
			result = append(result, items...)
		}
	}
	return result, present, nil
}

func videoKemeiRemoveFlatMedia(values map[string]json.RawMessage) {
	for _, key := range []string{"images", "image_urls", "videos", "audios", "files", "links"} {
		delete(values, key)
	}
}

// 通用入口保留文档中的扁平格式，允许有明确官方结构的正文，但拒绝同时提交两套媒体表示。
func prepareVideoKemeiCompat(values map[string]json.RawMessage) ([]byte, VideoRequestMetadata, error) {
	var metadata VideoRequestMetadata
	if err := videoProtocolReject(values, "contents", "settings", "input_reference", "image", "image_tail", "image_list", "image_url", "img_url", "first_frame_url", "last_frame_url", "media"); err != nil {
		return nil, metadata, err
	}
	_, flat, err := videoKemeiFlatMedia(values)
	if err != nil {
		return nil, metadata, err
	}
	root := videoProtocolRoot(values)
	if root.Get("content").Exists() {
		if flat || root.Get("prompt").Exists() || root.Get("input").Exists() || root.Get("parameters").Exists() {
			return nil, metadata, videoProtocolError("Use native content or flat Kemei parameters, not both")
		}
		if _, err = videoKemeiContentMedia(root.Get("content")); err != nil {
			return nil, metadata, err
		}
	} else if root.Get("input").Exists() || root.Get("parameters").Exists() {
		if flat || root.Get("prompt").Exists() || root.Get("duration").Exists() || root.Get("resolution").Exists() || root.Get("size").Exists() {
			return nil, metadata, videoProtocolError("Use native input/parameters or flat Kemei parameters, not both")
		}
		return prepareVideoKemeiWan(values)
	}
	if err = videoKemeiDimensions(values, true); err != nil {
		return nil, metadata, err
	}
	body, err := json.Marshal(values)
	if err == nil {
		metadata, err = ParseVideoEndpointRequestMetadata(body, "", VideoEndpointCompat)
	}
	return body, metadata, err
}

// 仅处理有明确等价关系的参数别名；不从模型名称补出分辨率或生成时长。
func videoKemeiDimensions(values map[string]json.RawMessage, autoDuration bool) error {
	if err := videoProtocolMove(values, values, "aspect_ratio", "ratio"); err != nil {
		return err
	}
	if err := videoProtocolMove(values, values, "size", "resolution"); err != nil {
		return err
	}
	if _, exists := values["seconds"]; exists {
		n, err := videoProtocolPositiveSeconds(gjson.ParseBytes(values["seconds"]), true)
		if err != nil {
			return err
		}
		if d := gjson.ParseBytes(values["duration"]); d.Exists() && (d.Type != gjson.Number || d.Float() != n) {
			return videoProtocolError("Conflicting Kemei duration and seconds")
		}
		videoProtocolPut(values, "duration", n)
		delete(values, "seconds")
	}
	if raw, exists := values["duration"]; exists {
		value := gjson.ParseBytes(raw)
		if !(autoDuration && value.Type == gjson.Number && value.Float() == -1) {
			if _, err := videoProtocolPositiveSeconds(value, false); err != nil {
				return err
			}
		}
	}
	if raw, exists := values["ratio"]; exists {
		if ratio := gjson.ParseBytes(raw); ratio.Type != gjson.String || !videoAccountTestRatio.MatchString(ratio.String()) {
			return videoProtocolError("Kemei ratio must be adaptive or a positive aspect ratio")
		}
	}
	return videoProtocolCanonicalResolution(values, false)
}

// content 数组只接受文档约定的图片/视频/音频对象，拒绝未知媒体被错误计为零张。
func videoKemeiContentMedia(content gjson.Result) ([]videoKemeiMedia, error) {
	if !content.IsArray() || len(content.Array()) == 0 {
		return nil, videoProtocolError("Kemei content must be a non-empty array")
	}
	result := []videoKemeiMedia{}
	for _, item := range content.Array() {
		if !item.IsObject() {
			return nil, videoProtocolError("Kemei content requires typed objects")
		}
		typ := item.Get("type").String()
		if typ == "text" {
			if item.Get("text").Type != gjson.String {
				return nil, videoProtocolError("Kemei text content requires text")
			}
			continue
		}
		kind := map[string]string{"image_url": "images", "video_url": "videos", "audio_url": "audios"}[typ]
		if kind == "" || item.Get(typ+".url").Type != gjson.String {
			return nil, videoProtocolError("Unsupported Kemei content media shape")
		}
		role := item.Get("role").String()
		if role == "" {
			role = map[string]string{"images": "reference_image", "videos": "reference_video", "audios": "reference_audio"}[kind]
		}
		flat, _ := json.Marshal([]map[string]string{{"url": item.Get(typ + ".url").String(), "role": role}})
		items, err := videoKemeiMediaItems(gjson.ParseBytes(flat), kind)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
	}
	return result, nil
}

func prepareVideoKemeiContent(values map[string]json.RawMessage, endpoint VideoEndpoint) ([]byte, VideoRequestMetadata, error) {
	var metadata VideoRequestMetadata
	media, flat, err := videoKemeiFlatMedia(values)
	if err != nil {
		return nil, metadata, err
	}
	if native, exists := values["content"]; exists {
		if flat || values["prompt"] != nil {
			return nil, metadata, videoProtocolError("Use native content or flat Kemei media, not both")
		}
		media, err = videoKemeiContentMedia(gjson.ParseBytes(native))
		if err != nil {
			return nil, metadata, err
		}
	} else {
		prompt := gjson.ParseBytes(values["prompt"])
		if prompt.Type != gjson.String || strings.TrimSpace(prompt.String()) == "" {
			return nil, metadata, videoProtocolError("Kemei prompt or native content is required")
		}
		content := []any{map[string]string{"type": "text", "text": prompt.String()}}
		for _, item := range media {
			typ := map[string]string{"first_frame": "image_url", "last_frame": "image_url", "reference_image": "image_url", "reference_video": "video_url", "reference_audio": "audio_url"}[item.Role]
			if typ == "" {
				return nil, metadata, videoProtocolError("Files and links are only supported by Kemei Wan")
			}
			content = append(content, map[string]any{"type": typ, typ: map[string]string{"url": item.URL}, "role": item.Role})
		}
		videoProtocolPut(values, "content", content)
		delete(values, "prompt")
		videoKemeiRemoveFlatMedia(values)
	}
	if err = videoKemeiDimensions(values, endpoint == VideoEndpointSeedance); err != nil {
		return nil, metadata, err
	}
	if endpoint == VideoEndpointMiniMax {
		if err = videoKemeiExclusiveFrames(media); err != nil {
			return nil, metadata, err
		}
		images, videos, audios := 0, 0, 0
		for _, item := range media {
			switch item.Role {
			case "first_frame", "last_frame", "reference_image":
				images++
			case "reference_video":
				videos++
			case "reference_audio":
				audios++
			}
		}
		if images > 9 || videos > 3 || audios > 3 || (audios > 0 && images+videos == 0) {
			return nil, metadata, videoProtocolError("Kemei MiniMax media exceeds limits or contains audio-only references")
		}
		root := videoProtocolRoot(values)
		if d := root.Get("duration"); d.Type != gjson.Number || d.Int() < 4 || d.Int() > 15 {
			return nil, metadata, videoProtocolError("Kemei MiniMax requires duration between 4 and 15 seconds")
		}
		if len(media) == 0 && (root.Get("ratio").String() == "" || root.Get("ratio").String() == "adaptive") {
			return nil, metadata, videoProtocolError("Kemei MiniMax text-to-video requires a non-adaptive ratio")
		}
	}
	return prepareVideoOfficialContent(values, endpoint)
}

// MiniMax/万相不允许首尾帧与参考生素材混用；检测仅查看声明角色。
func videoKemeiExclusiveFrames(media []videoKemeiMedia) error {
	first, last, references := 0, 0, 0
	for _, item := range media {
		switch item.Role {
		case "first_frame":
			first++
		case "last_frame":
			last++
		default:
			references++
		}
	}
	if first > 1 || last > 1 || (last > 0 && first == 0) || ((first+last) > 0 && references > 0) {
		return videoProtocolError("Kemei frame inputs cannot be mixed with reference inputs")
	}
	return nil
}

// 万相原生正文继续使用 input/parameters；平铺的多模态数组转换为官方 input.media。
func prepareVideoKemeiWan(values map[string]json.RawMessage) ([]byte, VideoRequestMetadata, error) {
	var metadata VideoRequestMetadata
	if err := videoProtocolReject(values, "content", "contents", "settings", "media", "input_reference"); err != nil {
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
	media, flat, err := videoKemeiFlatMedia(values)
	if err != nil {
		return nil, metadata, err
	}
	if flat {
		if err = videoProtocolReject(input, "media", "content", "img_url", "first_frame_url", "last_frame_url", "images", "image_urls"); err != nil {
			return nil, metadata, err
		}
		if err = videoKemeiExclusiveFrames(media); err != nil {
			return nil, metadata, err
		}
		native := []map[string]string{}
		for _, item := range media {
			native = append(native, map[string]string{"type": item.Role, "url": item.URL})
		}
		videoProtocolPut(input, "media", native)
		videoKemeiRemoveFlatMedia(values)
	}
	for _, field := range []string{"prompt", "negative_prompt", "img_url", "first_frame_url", "last_frame_url"} {
		if err = videoProtocolMove(values, input, field, field); err != nil {
			return nil, metadata, err
		}
	}
	if raw, exists := input["media"]; exists {
		if err = videoProtocolReject(input, "content", "img_url", "first_frame_url", "last_frame_url", "images", "image_urls"); err != nil {
			return nil, metadata, err
		}
		native := gjson.ParseBytes(raw)
		if !native.IsArray() {
			return nil, metadata, videoProtocolError("Kemei Wan input.media must be an array")
		}
		media = nil
		for _, item := range native.Array() {
			kind := map[string]string{"first_frame": "images", "last_frame": "images", "reference_image": "images", "reference_video": "videos", "reference_audio": "audios", "file": "files", "link": "links"}[item.Get("type").String()]
			if kind == "" {
				return nil, metadata, videoProtocolError("Unsupported Kemei Wan media type")
			}
			items, itemErr := videoKemeiMediaItems(gjson.Parse("["+item.Raw+"]"), kind)
			if itemErr != nil {
				return nil, metadata, itemErr
			}
			media = append(media, items...)
		}
		if err = videoKemeiExclusiveFrames(media); err != nil {
			return nil, metadata, err
		}
	}
	if err = videoKemeiDimensions(values, true); err != nil {
		return nil, metadata, err
	}
	for _, field := range []string{"duration", "resolution", "ratio", "watermark", "seed", "prompt_extend", "audio"} {
		if err = videoProtocolMove(values, parameters, field, field); err != nil {
			return nil, metadata, err
		}
	}
	if err = videoProtocolMove(values, parameters, "generate_audio", "audio"); err != nil {
		return nil, metadata, err
	}
	if err = videoKemeiDimensions(parameters, true); err != nil {
		return nil, metadata, err
	}
	if err = videoProtocolCanonicalResolution(parameters, true); err != nil {
		return nil, metadata, err
	}
	videoProtocolPut(values, "input", input)
	videoProtocolPut(values, "parameters", parameters)
	body, err := json.Marshal(values)
	if err == nil {
		metadata, err = ParseVideoEndpointRequestMetadata(body, "", VideoEndpointWan)
	}
	return body, metadata, err
}

// 可灵使用文档明确的模型操作路径，补齐首尾帧/Omni 平铺素材转换；不会重定向到兼容端点。
func prepareVideoKemeiKling(values map[string]json.RawMessage, target VideoUpstreamTarget) ([]byte, VideoRequestMetadata, error) {
	var metadata VideoRequestMetadata
	media, flat, err := videoKemeiFlatMedia(values)
	if err != nil {
		return nil, metadata, err
	}
	if native, exists := values["contents"]; exists {
		if flat || values["prompt"] != nil {
			return nil, metadata, videoProtocolError("Use native Kling contents or flat Kemei media, not both")
		}
		if _, _, err = videoKemeiKlingContentCount(gjson.ParseBytes(native), strings.HasPrefix(target.CreatePath, "/image-to-video/")); err != nil {
			return nil, metadata, err
		}
	} else if !strings.HasPrefix(target.CreatePath, "/text-to-video/") && (flat || strings.HasPrefix(target.CreatePath, "/omni-video/")) {
		contents := []any{}
		if prompt := gjson.ParseBytes(values["prompt"]); prompt.Exists() {
			if prompt.Type != gjson.String {
				return nil, metadata, videoProtocolError("Kemei prompt must be a string")
			}
			contents = append(contents, map[string]string{"type": "prompt", "text": prompt.String()})
		}
		for _, item := range media {
			if item.Role == "file" || item.Role == "link" || item.Role == "reference_audio" {
				return nil, metadata, videoProtocolError("Kling does not support file, link or audio URL references")
			}
			role := item.Role
			if role == "reference_image" {
				role = "refer_image"
			} else if role == "reference_video" {
				role = "feature_video"
			}
			contents = append(contents, map[string]string{"type": role, "url": item.URL})
		}
		videoProtocolPut(values, "contents", contents)
		videoKemeiRemoveFlatMedia(values)
		delete(values, "prompt")
	}
	if err = videoKemeiDimensions(values, false); err != nil {
		return nil, metadata, err
	}
	if err = videoKemeiKlingSettings(values); err != nil {
		return nil, metadata, err
	}
	// 仅复用稳定字段转换；可美的新原生素材名称独立校验和计数，不改变既有官方方案。
	body, metadata, err := prepareVideoOfficialKling(values, target)
	if err == nil && !strings.HasPrefix(target.CreatePath, "/text-to-video/") {
		metadata.ReferenceImageCount, metadata.HasReferenceVideo, err = videoKemeiKlingContentCount(gjson.GetBytes(body, "contents"), strings.HasPrefix(target.CreatePath, "/image-to-video/"))
	}
	return body, metadata, err
}
