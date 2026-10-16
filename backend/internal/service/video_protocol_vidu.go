package service

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/tidwall/gjson"
)

// prepareVideoOfficialVidu 使用官方原生参数；不同模型的默认时长和分辨率由上游决定，不能套用可美默认值。
func prepareVideoOfficialVidu(values map[string]json.RawMessage, target VideoUpstreamTarget) ([]byte, VideoRequestMetadata, error) {
	var metadata VideoRequestMetadata
	if !validViduVideoPath(target.CreatePath) {
		return nil, metadata, videoProtocolError("Vidu requires an explicit operation path")
	}
	// 只检查协议固定字段，供应商扩展和透传 payload 不参与本地媒体判定。
	for key := range values {
		switch strings.ToLower(key) {
		case "images", "image_urls", "sounds", "audios", "aspect_ratio", "ratio", "audio", "generate_audio", "off_peak", "seed", "subjects", "auto_subjects", "watermark", "bgm":
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
	if imagesPresent {
		videoProtocolPut(values, "images", images)
	}
	if err := videoProtocolCanonicalResolution(values, false); err != nil {
		return nil, metadata, err
	}
	root := videoProtocolRoot(values)
	if duration := root.Get("duration"); duration.Exists() {
		if _, err := videoProtocolPositiveSeconds(duration, false); err != nil {
			return nil, metadata, err
		}
	}
	if prompt := root.Get("prompt"); prompt.Exists() && prompt.Type != gjson.String ||
		(target.CreatePath == "/ent/v2/text2video" || target.CreatePath == "/ent/v2/reference2video") && strings.TrimSpace(prompt.String()) == "" {
		return nil, metadata, videoProtocolError("Vidu text2video and reference2video require a text prompt")
	}
	if ratio := root.Get("aspect_ratio"); ratio.Exists() {
		if ratio.Type != gjson.String || !(videoAccountTestRatio.MatchString(ratio.String()) && ratio.String() != "adaptive" || target.CreatePath == "/ent/v2/reference2video" && ratio.String() == "auto") {
			return nil, metadata, videoProtocolError("Vidu aspect_ratio must be a positive ratio or reference2video auto")
		}
	}
	for _, key := range []string{"audio", "off_peak", "auto_subjects", "watermark", "bgm"} {
		if value := root.Get(key); value.Exists() && value.Type != gjson.True && value.Type != gjson.False {
			return nil, metadata, videoProtocolError(key + " must be a boolean")
		}
	}
	if seed := root.Get("seed"); seed.Exists() && (seed.Type != gjson.Number || seed.Float() < 0 || math.IsInf(seed.Float(), 0) || math.Trunc(seed.Float()) != seed.Float()) {
		return nil, metadata, videoProtocolError("Vidu seed must be a non-negative integer")
	}
	for _, key := range []string{"payload", "callback_url", "wm_url", "meta_data"} {
		if value := root.Get(key); value.Exists() && value.Type != gjson.String {
			return nil, metadata, videoProtocolError(key + " must be a string")
		}
	}
	if position := root.Get("wm_position"); position.Exists() && (position.Type != gjson.Number || position.Float() < 1 || position.Float() > 4 || math.Trunc(position.Float()) != position.Float()) {
		return nil, metadata, videoProtocolError("Vidu wm_position must be an integer from 1 to 4")
	}
	sounds := root.Get("sounds")
	if sounds.Exists() {
		if err := validateVideoOfficialViduMediaArray(sounds, "sounds"); err != nil {
			return nil, metadata, err
		}
		if target.CreatePath != "/ent/v2/reference2video" && len(sounds.Array()) > 0 {
			return nil, metadata, videoProtocolError("Vidu sounds are only supported by reference2video")
		}
	}
	subjectsPresent, subjectVideo, err := validateVideoOfficialViduSubjects(root.Get("subjects"))
	if err != nil {
		return nil, metadata, err
	}
	autoSubjects := root.Get("auto_subjects").Bool()
	if target.CreatePath != "/ent/v2/reference2video" && (subjectsPresent || autoSubjects) {
		return nil, metadata, videoProtocolError("Vidu subjects are only supported by reference2video")
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
	case "/ent/v2/start-end2video":
		if len(images) != 2 {
			return nil, metadata, videoProtocolError("Vidu start-end2video requires two images")
		}
	case "/ent/v2/reference2video":
		if len(images) == 0 && !subjectsPresent && !autoSubjects {
			return nil, metadata, videoProtocolError("Vidu reference2video requires images or subjects")
		}
	}
	body, err := json.Marshal(values)
	if err != nil {
		return nil, metadata, err
	}
	metadata, err = ParseVideoEndpointRequestMetadata(body, "", VideoEndpointVidu)
	if subjectsPresent || autoSubjects {
		// 主体库内容无法在此证明图片数量，图片附加费继续使用既有未知数量保护，不能当成零张。
		metadata.ReferenceImageCount = nil
	}
	metadata.HasReferenceVideo = subjectVideo
	return body, metadata, err
}

// 原生素材只检查非空字符串形态，URL 与 Data URL 原样发送，不在适配层下载或重编码。
func validateVideoOfficialViduMediaArray(value gjson.Result, name string) error {
	if !value.IsArray() {
		return videoProtocolError("Vidu " + name + " must be a string array")
	}
	for _, item := range value.Array() {
		if item.Type != gjson.String || strings.TrimSpace(item.String()) == "" {
			return videoProtocolError("Vidu " + name + " must contain non-empty strings")
		}
	}
	return nil
}

// 官方主体示例同时出现单对象与对象数组；保留原形，不展开主体库 ID 或推测其媒体数量。
func validateVideoOfficialViduSubjects(value gjson.Result) (present, hasVideo bool, err error) {
	if !value.Exists() {
		return false, false, nil
	}
	var subjects []gjson.Result
	if value.IsObject() {
		subjects = []gjson.Result{value}
	} else if value.IsArray() {
		subjects = value.Array()
	} else {
		return false, false, videoProtocolError("Vidu subjects must be an object or object array")
	}
	for _, subject := range subjects {
		name := subject.Get("name")
		if !subject.IsObject() || name.Type != gjson.String || strings.TrimSpace(name.String()) == "" {
			return false, false, videoProtocolError("Vidu subject requires a non-empty name")
		}
		for _, key := range []string{"images", "videos"} {
			if media := subject.Get(key); media.Exists() {
				// 部分官方主体示例以字符串表示单个素材，原生数组与单素材字符串均不做隐式转换。
				if media.Type == gjson.String && strings.TrimSpace(media.String()) != "" {
					hasVideo = hasVideo || key == "videos"
					continue
				}
				if err := validateVideoOfficialViduMediaArray(media, "subject "+key); err != nil {
					return false, false, err
				}
				hasVideo = hasVideo || key == "videos" && len(media.Array()) > 0
			}
		}
	}
	return len(subjects) > 0, hasVideo, nil
}

// 官方与可美 Vidu 使用相同的只读状态结构；credits 不是 Token，不参与站内 Token 结算。
func applyVideoOfficialViduResponse(out *VideoUpstreamResponse, root gjson.Result) {
	applyVideoKemeiViduResponse(out, root)
}

const (
	videoViduMissingCreation             = "Vidu completion has no video creation"
	videoViduMissingVideoURL             = "Vidu completion has no video URL"
	videoViduDimensionConfirmationNeeded = "Vidu output billing dimensions require confirmation"
)

// 仅明确缺少产物或链接允许补齐恢复，多产物、未知结构和真实维度错误仍须完整确认。
func videoViduOutputErrorRecoverable(message string) bool {
	return message == videoViduMissingCreation || message == videoViduMissingVideoURL
}

// Vidu 三种显式方案共用原生查询响应，其它端点不进入该恢复特例。
func videoViduResponseTarget(target VideoUpstreamTarget) bool {
	if target.Endpoint != VideoEndpointVidu {
		return false
	}
	_, err := VideoTargetProtocolAdapter(target)
	return err == nil
}

// 缺产物不能掩盖同包已观测到的档位冲突或与冻结时长不同的输出事实。
func videoViduOutputDimensionsConflict(task *VideoTaskRecord, response *VideoUpstreamResponse) bool {
	if resolution := response.Metadata.Resolution; resolution != "" {
		normalized, err := NormalizeVideoPriceResolution(resolution)
		if err != nil || videoProtocolQuoteRequiresResolution(task.Quote) && normalized != task.Quote.Resolution {
			return true
		}
	}
	// 合法时长变化只影响秒价；按次与 Token 收费不因无关输出时长变化增加恢复门槛。
	return task.Quote != nil && task.Quote.Mode == BillingModeVideo && response.Metadata.DurationSeconds > 0 && task.Metadata.DurationSeconds > 0 && response.Metadata.DurationSeconds != task.Metadata.DurationSeconds
}

// 标准 Vidu 查询可能只返回 creations；产物缺失补齐时复用已冻结的可信维度，不猜测未知用量。
func videoViduMetadataRecovered(task *VideoTaskRecord, response *VideoUpstreamResponse) bool {
	if task.Quote == nil || response.MetadataError != "" || response.Status != "completed" || !safeMediaPreviewURL(response.VideoURL) {
		return false
	}
	if !videoViduOutputErrorRecoverable(task.ResponseMetadataError) {
		return false
	}
	if task.Quote.Mode == BillingModeVideoToken && response.Metadata.Tokens == nil {
		return false
	}
	if task.Quote.Mode == BillingModeVideo {
		seconds := response.Metadata.DurationSeconds
		if seconds == 0 {
			seconds = task.Metadata.DurationSeconds
		}
		if seconds <= 0 || seconds > math.MaxInt32 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
			return false
		}
	}
	if !videoProtocolQuoteRequiresResolution(task.Quote) {
		return true
	}
	resolution := response.Metadata.Resolution
	if resolution == "" {
		resolution = task.Metadata.Resolution
	}
	normalized, err := NormalizeVideoPriceResolution(resolution)
	return err == nil && normalized != "" && normalized == task.Quote.Resolution
}
