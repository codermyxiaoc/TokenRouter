package service

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/tidwall/gjson"
)

// Vidu 以明确操作路径区分请求合同，不能通过图片数量或模型名称替换操作。
func validViduVideoPath(path string) bool {
	switch path {
	case "/ent/v2/img2video", "/ent/v2/reference2video", "/ent/v2/text2video", "/ent/v2/start-end2video":
		return true
	}
	return false
}

// Q4 的不支持操作来自可美文档，仅用于提前拒绝，不推断其它模型的路径。
func validateVideoViduOperationModel(model, path string) error {
	if (strings.EqualFold(model, "viduq4") || strings.HasPrefix(strings.ToLower(model), "viduq4-")) &&
		(path == "/ent/v2/text2video" || path == "/ent/v2/start-end2video") {
		return videoProtocolError("Vidu Q4 does not support text2video or start-end2video")
	}
	return nil
}

// prepareVideoKemeiVidu 只转换文档定义的别名，并把默认计费维度写入真实发送正文。
func prepareVideoKemeiVidu(values map[string]json.RawMessage, target VideoUpstreamTarget) ([]byte, VideoRequestMetadata, error) {
	var metadata VideoRequestMetadata
	if !validViduVideoPath(target.CreatePath) {
		return nil, metadata, videoProtocolError("Vidu requires an explicit operation path")
	}
	if err := validateVideoViduOperationModel(target.Model, target.CreatePath); err != nil {
		return nil, metadata, err
	}
	for key := range values {
		switch strings.ToLower(key) {
		case "images", "image_urls", "sounds", "audios", "aspect_ratio", "ratio", "audio", "generate_audio", "off_peak", "seed":
			if key != strings.ToLower(key) {
				return nil, metadata, videoProtocolError("Vidu parameters must use canonical field names")
			}
		}
	}
	if err := videoProtocolReject(values, "content", "contents", "input", "parameters", "settings", "media", "input_reference", "image", "image_tail", "image_list", "image_url", "img_url", "first_frame_url", "last_frame_url", "video_list", "videos", "reference_videos", "reference_video", "reference_video_url", "video_url", "audio_url", "seconds", "size"); err != nil {
		return nil, metadata, err
	}
	for _, pair := range [][2]string{{"audios", "sounds"}, {"ratio", "aspect_ratio"}, {"generate_audio", "audio"}} {
		if err := videoProtocolMove(values, values, pair[0], pair[1]); err != nil {
			return nil, metadata, err
		}
	}
	images, imagesPresent, err := videoProtocolImages(values)
	if err != nil {
		return nil, metadata, err
	}
	switch target.CreatePath {
	case "/ent/v2/text2video":
		if len(images) != 0 {
			return nil, metadata, videoProtocolError("Vidu text2video does not accept images")
		}
	case "/ent/v2/img2video":
		if len(images) != 1 {
			return nil, metadata, videoProtocolError("Vidu img2video requires one image")
		}
	case "/ent/v2/reference2video":
		if len(images) == 0 {
			return nil, metadata, videoProtocolError("Vidu reference2video requires reference images")
		}
	case "/ent/v2/start-end2video":
		if len(images) != 2 {
			return nil, metadata, videoProtocolError("Vidu start-end2video requires two images")
		}
	}
	if imagesPresent {
		videoProtocolPut(values, "images", images)
	}
	if _, exists := values["duration"]; !exists {
		videoProtocolPut(values, "duration", 5)
	}
	if _, exists := values["resolution"]; !exists {
		videoProtocolPut(values, "resolution", "540p")
	}
	if _, exists := values["aspect_ratio"]; !exists {
		videoProtocolPut(values, "aspect_ratio", "16:9")
	}
	if err := videoProtocolCanonicalResolution(values, false); err != nil {
		return nil, metadata, err
	}
	root := videoProtocolRoot(values)
	if _, err := videoProtocolPositiveSeconds(root.Get("duration"), false); err != nil {
		return nil, metadata, err
	}
	if prompt := root.Get("prompt"); prompt.Exists() && prompt.Type != gjson.String ||
		target.CreatePath == "/ent/v2/text2video" && strings.TrimSpace(prompt.String()) == "" {
		return nil, metadata, videoProtocolError("Vidu text2video requires a text prompt")
	}
	if ratio := root.Get("aspect_ratio"); ratio.Type != gjson.String || !videoAccountTestRatio.MatchString(ratio.String()) || ratio.String() == "adaptive" {
		return nil, metadata, videoProtocolError("Vidu aspect_ratio must be a positive ratio")
	}
	for _, key := range []string{"audio", "off_peak"} {
		if value := root.Get(key); value.Exists() && value.Type != gjson.True && value.Type != gjson.False {
			return nil, metadata, videoProtocolError(key + " must be a boolean")
		}
	}
	if seed := root.Get("seed"); seed.Exists() && (seed.Type != gjson.Number || seed.Float() < 0 || seed.Float() > math.MaxInt32 || math.Trunc(seed.Float()) != seed.Float()) {
		return nil, metadata, videoProtocolError("Vidu seed must be a non-negative 32-bit integer")
	}
	if sounds := root.Get("sounds"); sounds.Exists() {
		if !sounds.IsArray() {
			return nil, metadata, videoProtocolError("Vidu sounds must be a URL string array")
		}
		// 文档只允许参考生视频携带参考音频，其它操作不能静默忽略用户素材。
		if target.CreatePath != "/ent/v2/reference2video" && len(sounds.Array()) > 0 {
			return nil, metadata, videoProtocolError("Vidu sounds are only supported by reference2video")
		}
		for _, sound := range sounds.Array() {
			if sound.Type != gjson.String || strings.TrimSpace(sound.String()) == "" {
				return nil, metadata, videoProtocolError("Vidu sound must be a non-empty URL")
			}
		}
	}
	body, err := json.Marshal(values)
	if err != nil {
		return nil, metadata, err
	}
	metadata, err = ParseVideoEndpointRequestMetadata(body, "", VideoEndpointVidu)
	return body, metadata, err
}

// applyVideoKemeiViduResponse 只读文档中的任务字段，creations[].id 是产物 ID。
func applyVideoKemeiViduResponse(out *VideoUpstreamResponse, root gjson.Result) {
	out.Status, out.UpstreamStatus, out.VideoURL = "", "", ""
	out.Metadata = VideoRequestMetadata{}
	if !root.IsObject() {
		return
	}
	if out.TaskID == "" {
		out.TaskID = firstVideoString(root, "task_id", "id")
	}
	out.UpstreamStatus = firstVideoString(root, "state")
	switch out.UpstreamStatus {
	case "created", "queueing", "queued":
		out.Status = "queued"
	case "processing":
		out.Status = "processing"
	case "success":
		out.Status = "completed"
	case "failed":
		out.Status = "failed"
	}
	if code := root.Get("err_code"); code.Exists() && code.Type != gjson.Null && code.String() != "" && code.String() != "0" {
		if out.Status == "completed" {
			out.Status = ""
			out.MetadataError = "Conflicting Vidu success state and error code"
		}
	}
	if value := root.Get("duration"); value.Exists() {
		seconds, valid := videoProtocolObservedSeconds(value, false)
		if !valid {
			out.MetadataError = "Invalid Vidu output duration"
		} else {
			out.Metadata.DurationSeconds = seconds
		}
	}
	if value := root.Get("resolution"); value.Exists() {
		if value.Type != gjson.String || strings.TrimSpace(value.String()) == "" {
			out.MetadataError = "Invalid Vidu output resolution"
		} else {
			out.Metadata.Resolution = value.String()
		}
	}
	out.Metadata.Model = firstVideoString(root, "model")
	if out.Status == "completed" {
		creations := root.Get("creations")
		if !creations.Exists() || creations.IsArray() && len(creations.Array()) == 0 {
			// 明确缺产物可以在后续查询补齐；不得覆盖同包已经发现的真实计费维度异常。
			if out.MetadataError == "" {
				out.MetadataError = videoViduMissingCreation
			}
			return
		}
		if !creations.IsArray() || len(creations.Array()) != 1 {
			if out.MetadataError == "" {
				out.MetadataError = "Vidu completion requires exactly one video creation"
			}
			return
		}
		creation := creations.Array()[0]
		mediaURL := creation.Get("url")
		if !creation.IsObject() || mediaURL.Exists() && mediaURL.Type != gjson.String && mediaURL.Type != gjson.Null {
			if out.MetadataError == "" {
				out.MetadataError = "Invalid Vidu video creation"
			}
			return
		}
		out.VideoURL = firstVideoString(creation, "url")
		if out.VideoURL == "" && out.MetadataError == "" {
			out.MetadataError = videoViduMissingVideoURL
		}
	}
}
