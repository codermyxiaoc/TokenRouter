package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
)

// videoProtocolPreset 是系统预置的声明式模板。内置协议只保留各家官方格式，
// 厂商与中转格式（Metaso H3、Yezonghui、可美、通用扁平格式）都以预置模板提供，管理员可复制后修改。
// @project-doc docs/interfaces/video_upstream.md#video_protocol_presets
type videoProtocolPreset struct {
	Key  string
	Name string
	Spec VideoProtocolProfileSpec
}

var (
	// 通用状态词表沿用旧通用解析器，覆盖常见中转的排队、处理中与终态写法。
	videoPresetGenericStatusMap = map[string]string{
		"queued": "queued", "submitted": "queued", "pending": "queued",
		"running": "processing", "processing": "processing", "in_progress": "processing",
		"succeeded": "completed", "succeed": "completed", "success": "completed", "completed": "completed", "done": "completed",
		"failed": "failed", "fail": "failed", "error": "failed",
		"cancelled": "cancelled", "canceled": "cancelled", "deleted": "cancelled",
		"expired": "expired",
	}
	videoPresetGenericTaskIDs    = []string{"id", "task_id", "output.task_id", "data.task_id", "data.id"}
	videoPresetGenericStatuses   = VideoProfilePaths{"status", "task_status", "output.task_status", "data.task_status", "data.status"}
	videoPresetGenericVideoURLs  = []string{"content.video_url", "video.url", "output.video_url", "data.0.url", "data.video_url", "task_result.videos.0.url", "data.task_result.videos.0.url"}
	videoPresetGenericSeconds    = []string{"duration", "video.duration", "output.duration", "usage.duration", "data.duration", "task_result.videos.0.duration"}
	videoPresetGenericTokens     = []string{"usage.completion_tokens", "usage.output_tokens", "output.usage.completion_tokens", "data.usage.completion_tokens"}
	videoPresetGenericResolution = []string{"resolution", "video.resolution", "output.resolution"}
	// 可美的实际输出秒数优先于回显的请求时长；Token 只认输出用量。
	videoPresetKemeiSeconds = []string{"usage.output_seconds", "duration"}
	videoPresetKemeiTokens  = []string{"usage.completion_tokens", "usage.output_tokens"}
	// Metaso H3 只接受四种像素尺寸，分别对应 768p 与 2k 两个计价档位。
	videoPresetH3Sizes = map[string]string{"1280x720": "768p", "720x1280": "768p", "1792x1024": "2k", "1024x1792": "2k"}
)

// videoPresetMove 生成一条字段改名规则。
func videoPresetMove(from, to string) VideoProfileRule { return VideoProfileRule{From: from, To: to} }

// videoPresetRaw 把 Go 值编码为模板中的 JSON 常量或默认值。
func videoPresetRaw(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

// videoPresetContent 返回内容获取配置：给出路径时直链优先、缺失时回退内容路径，否则只用直链。
func videoPresetContent(path string) VideoProfileContent {
	if path == "" {
		return VideoProfileContent{Mode: "url"}
	}
	return VideoProfileContent{Mode: "path", Path: path}
}

// videoPresetGenericFlat 是旧“原样协议”（legacy_v1）的模板化版本：扁平字段、图片 URL 数组与通用响应解析。
func videoPresetGenericFlat(createPath, pollPath, contentPath string) VideoProtocolProfileSpec {
	return VideoProtocolProfileSpec{
		Auth:         VideoProfileAuth{Scheme: "bearer"},
		Capabilities: &VideoProfileCapabilities{ImageRoles: []string{VideoMediaRoleFirstFrame, VideoMediaRoleReferenceImage}},
		Create: VideoProfileCreate{
			Path: createPath,
			Body: []VideoProfileRule{
				videoPresetMove("model", "model"), videoPresetMove("prompt", "prompt"), videoPresetMove("negative_prompt", "negative_prompt"),
				videoPresetMove("duration", "duration"), videoPresetMove("resolution", "resolution"), videoPresetMove("aspect_ratio", "ratio"),
				videoPresetMove("size", "size"), {From: "images", To: "images", Item: "$url"},
				videoPresetMove("generate_audio", "generate_audio"), videoPresetMove("watermark", "watermark"), videoPresetMove("seed", "seed"),
			},
			Response: VideoProfileCreateResponse{TaskID: videoPresetGenericTaskIDs},
		},
		Poll: VideoProfilePoll{
			Path: pollPath,
			Response: VideoProfilePollResponse{
				TaskID: videoPresetGenericTaskIDs, Status: videoPresetGenericStatuses, StatusMap: videoPresetGenericStatusMap,
				VideoURL: videoPresetGenericVideoURLs, Seconds: videoPresetGenericSeconds, Tokens: videoPresetGenericTokens, Resolution: videoPresetGenericResolution,
			},
		},
		Content: videoPresetContent(contentPath),
	}
}

// videoPresetMetasoH3 对应旧 metaso_h3_v1：整数秒、分辨率档位与比例联合换算为四种像素尺寸、单张参考图。
func videoPresetMetasoH3() VideoProtocolProfileSpec {
	sizeLookup := map[string]any{}
	sizeIdentity := map[string]any{}
	for size, tier := range videoPresetH3Sizes {
		_, orientation := metasoH3SizeTier(size)
		sizeLookup[tier+"|"+orientation] = size
		sizeIdentity[size] = size
	}
	return VideoProtocolProfileSpec{
		Auth: VideoProfileAuth{Scheme: "bearer"},
		Capabilities: &VideoProfileCapabilities{
			MaxImages: 1, ImageRoles: []string{VideoMediaRoleFirstFrame, VideoMediaRoleReferenceImage}, IntegerDuration: true,
			Resolutions: []string{"768p", "2k"}, AspectRatios: []string{"16:9", "9:16"},
			MaxReferenceBytes: CreativeVideoH3ReferenceImageLimit, AdaptiveRatio: "orientation", SizeResolutions: videoPresetH3Sizes,
		},
		Create: VideoProfileCreate{
			Path: "/v1/videos",
			Body: []VideoProfileRule{
				videoPresetMove("model", "model"), videoPresetMove("prompt", "prompt"), videoPresetMove("negative_prompt", "negative_prompt"),
				{From: "duration", To: "seconds", Type: "int"},
				{FromAll: []string{"resolution", "aspect_ratio"}, To: "size", Map: sizeLookup},
				{From: "size", To: "size", Map: sizeIdentity},
				videoPresetMove("images[0]", "input_reference.image_url"),
				videoPresetMove("generate_audio", "generate_audio"), videoPresetMove("watermark", "watermark"), videoPresetMove("seed", "seed"),
			},
			Response: VideoProfileCreateResponse{TaskID: videoPresetGenericTaskIDs},
		},
		Poll: VideoProfilePoll{
			Path: "/v1/videos/{task_id}",
			Response: VideoProfilePollResponse{
				TaskID: videoPresetGenericTaskIDs, Status: videoPresetGenericStatuses, StatusMap: videoPresetGenericStatusMap, VideoURL: videoPresetGenericVideoURLs,
				Seconds:    append([]string{"seconds"}, videoPresetGenericSeconds...),
				Resolution: []string{"size", "resolution", "video.resolution", "output.resolution"},
				Tokens:     videoPresetGenericTokens, Strict: true,
			},
		},
		Content: videoPresetContent("/v1/videos/{task_id}/content"),
	}
}

// videoPresetYezonghui 对应旧 yezonghui_v1：必填提示词、整数秒字符串与分辨率档位，素材只能是公网链接，分钟级查询。
func videoPresetYezonghui() VideoProtocolProfileSpec {
	exists := true
	return VideoProtocolProfileSpec{
		Auth: VideoProfileAuth{Scheme: "bearer"},
		Capabilities: &VideoProfileCapabilities{
			IntegerDuration: true, MinDuration: 1, MaxDuration: 3600, PublicMedia: true, AdaptiveRatio: "omit",
		},
		Create: VideoProfileCreate{
			Path: "/v1/videos",
			Body: []VideoProfileRule{
				videoPresetMove("model", "model"),
				{From: "prompt", To: "prompt", Required: true},
				{From: "duration", To: "seconds", Type: "string_int", Required: true},
				{From: "resolution", To: "resolution", Type: "resolution_tier", Required: true},
				{From: "aspect_ratio", To: "size", Type: "ratio"},
				{From: "size", To: "size", Type: "ratio"},
				{From: "images", To: "image_urls", Item: "$url"},
				{From: "videos", To: "video_urls", Item: "$url"},
				videoPresetMove("negative_prompt", "negative_prompt"), videoPresetMove("seed", "seed"), videoPresetMove("generate_audio", "audio"),
				videoPresetMove("extra.metadata", "metadata"), videoPresetMove("extra.n", "n"), videoPresetMove("extra.fps", "fps"),
				videoPresetMove("extra.camera_fixed", "camera_fixed"), videoPresetMove("extra.audio_urls", "audio_urls"),
			},
			Response: VideoProfileCreateResponse{TaskID: []string{"id", "task_id"}},
		},
		Poll: VideoProfilePoll{
			Path:            "/v1/videos/{task_id}",
			IntervalSeconds: 60,
			Response: VideoProfilePollResponse{
				TaskID: []string{"id", "task_id"}, Status: VideoProfilePaths{"status"},
				StatusMap: map[string]string{"queued": "queued", "in_progress": "processing", "completed": "completed", "failed": "failed"},
				VideoURL:  []string{"url"}, Seconds: []string{"seconds"}, Resolution: []string{"resolution"},
				Tokens: []string{"usage.output_tokens", "usage.completion_tokens"}, Strict: true, PublicURL: true,
				// 完成包同时带错误或旧格式结果封装时无法确认交付。
				CompletedInvalidWhen: []VideoProfileCondition{
					{Path: "code", Exists: &exists}, {Path: "error", Exists: &exists}, {Path: "data", Exists: &exists},
					{Path: "output", Exists: &exists}, {Path: "task", Exists: &exists}, {Path: "object", NotIn: []any{"video"}},
				},
			},
		},
		Content: videoPresetContent("/v1/videos/{task_id}/content"),
	}
}

// videoPresetKemeiResponse 返回可美通用响应解析；可美各端点在中转层统一了大部分字段。
func videoPresetKemeiResponse(taskIDs []string, extraURLs ...string) VideoProfilePollResponse {
	return VideoProfilePollResponse{
		TaskID: taskIDs, Status: videoPresetGenericStatuses, StatusMap: videoPresetGenericStatusMap,
		VideoURL: append(append([]string{}, extraURLs...), videoPresetGenericVideoURLs...),
		Seconds:  videoPresetKemeiSeconds, Tokens: videoPresetKemeiTokens, Resolution: []string{"resolution"},
		Outputs: &VideoProfileOutputs{Path: "data", Max: 1},
	}
}

// videoPresetKemeiCompat 对应可美 /v1/video/generations：扁平字段，素材保留 url 与 role。
func videoPresetKemeiCompat() VideoProtocolProfileSpec {
	return VideoProtocolProfileSpec{
		Auth: VideoProfileAuth{Scheme: "bearer"},
		Create: VideoProfileCreate{
			Path: "/v1/video/generations",
			Body: []VideoProfileRule{
				videoPresetMove("model", "model"), videoPresetMove("prompt", "prompt"), videoPresetMove("negative_prompt", "negative_prompt"),
				videoPresetMove("duration", "duration"), {From: "resolution", To: "resolution", Type: "resolution"}, videoPresetMove("aspect_ratio", "ratio"),
				{From: "images", To: "images", Each: map[string]string{"url": "$url", "role": "$role"}},
				{From: "videos", To: "videos", Each: map[string]string{"url": "$url", "role": "$role"}},
				videoPresetMove("generate_audio", "generate_audio"), videoPresetMove("watermark", "watermark"), videoPresetMove("seed", "seed"),
			},
			Response: VideoProfileCreateResponse{TaskID: videoPresetGenericTaskIDs},
		},
		Poll:    VideoProfilePoll{Path: "/v1/video/generations/{task_id}", Response: videoPresetKemeiResponse(videoPresetGenericTaskIDs)},
		Content: videoPresetContent(""),
	}
}

// videoPresetKemeiContent 对应可美 Seedance 与 MiniMax：提示词与素材按角色写入 content 数组。
func videoPresetKemeiContent(minimax bool) VideoProtocolProfileSpec {
	watermark, resolutionType := "watermark", "resolution"
	if minimax {
		// MiniMax 官方合同使用 aigc_watermark 与大写分辨率。
		watermark, resolutionType = "aigc_watermark", "resolution_upper"
	}
	spec := VideoProtocolProfileSpec{
		Auth: VideoProfileAuth{Scheme: "bearer"},
		Create: VideoProfileCreate{
			Path:             "/api/v3/contents/generations/tasks",
			ImageRoleDefault: "frames",
			Body: []VideoProfileRule{
				videoPresetMove("model", "model"),
				{From: "prompt", To: "content", Each: map[string]string{"type": "text", "text": "$value"}, Append: true, Required: true},
				{From: "images", To: "content", Each: map[string]string{"type": "image_url", "image_url.url": "$url", "role": "$role"}, Append: true},
				{From: "videos", To: "content", Each: map[string]string{"type": "video_url", "video_url.url": "$url", "role": VideoMediaRoleReferenceVideo}, Append: true},
				{From: "duration", To: "duration", Required: minimax}, {From: "resolution", To: "resolution", Type: resolutionType, Required: minimax},
				videoPresetMove("aspect_ratio", "ratio"), videoPresetMove("negative_prompt", "negative_prompt"),
				videoPresetMove("generate_audio", "generate_audio"), videoPresetMove("watermark", watermark), videoPresetMove("seed", "seed"),
			},
			Response: VideoProfileCreateResponse{TaskID: videoPresetGenericTaskIDs},
		},
		Poll:    VideoProfilePoll{Path: "/api/v3/contents/generations/tasks/{task_id}", Response: videoPresetKemeiResponse(videoPresetGenericTaskIDs)},
		Content: videoPresetContent(""),
	}
	if minimax {
		// MiniMax 中继把任务放在 task 封装内；素材数量、时长与文生视频比例都有硬限制。
		spec.Capabilities = &VideoProfileCapabilities{MaxImages: 9, MaxVideos: 3, MinDuration: 4, MaxDuration: 15, ExclusiveFrames: true, RequireRatioWithoutMedia: true}
		spec.Create.Path = "/v2/video_generation"
		spec.Create.Response = VideoProfileCreateResponse{
			TaskID:       []string{"TaskId", "task_id", "data.task_id", "id", "task.id", "task.task_id"},
			RejectedWhen: []VideoProfileCondition{{Path: "base_resp.status_code", NotIn: []any{float64(0)}}},
		}
		response := videoPresetKemeiResponse([]string{"id", "task_id"}, "content.url")
		response.Root = VideoProfilePaths{"task"}
		spec.Poll = VideoProfilePoll{Path: "/v2/query/video_generation/{task_id}", Response: response}
	}
	return spec
}

// videoPresetKemeiWan 对应可美万相：提示词与素材进 input，生成参数进 parameters。
func videoPresetKemeiWan() VideoProtocolProfileSpec {
	taskIDs := []string{"output.task_id", "task_id", "id"}
	response := videoPresetKemeiResponse(taskIDs, "output.video_url")
	response.Status = VideoProfilePaths{"output.task_status", "status", "task_status"}
	response.Seconds = []string{"usage.output_video_duration", "usage.duration", "usage.output_seconds", "duration"}
	response.Resolution = []string{"usage.SR", "resolution"}
	return VideoProtocolProfileSpec{
		Auth:         VideoProfileAuth{Scheme: "bearer"},
		Capabilities: &VideoProfileCapabilities{ExclusiveFrames: true},
		Create: VideoProfileCreate{
			Path: "/api/v1/services/aigc/video-generation/video-synthesis",
			// 万相创建必须声明异步模式，旧可美方案同样发送该请求头。
			Headers:          map[string]string{"X-DashScope-Async": "enable"},
			ImageRoleDefault: "frames",
			Body: []VideoProfileRule{
				videoPresetMove("model", "model"), videoPresetMove("prompt", "input.prompt"), videoPresetMove("negative_prompt", "input.negative_prompt"),
				{From: "images", To: "input.media", Each: map[string]string{"type": "$role", "url": "$url"}, Append: true},
				{From: "videos", To: "input.media", Each: map[string]string{"type": VideoMediaRoleReferenceVideo, "url": "$url"}, Append: true},
				videoPresetMove("duration", "parameters.duration"), {From: "resolution", To: "parameters.resolution", Type: "resolution_upper"},
				videoPresetMove("aspect_ratio", "parameters.ratio"), videoPresetMove("watermark", "parameters.watermark"),
				videoPresetMove("seed", "parameters.seed"), videoPresetMove("generate_audio", "parameters.audio"),
				videoPresetMove("extra.prompt_extend", "parameters.prompt_extend"),
			},
			Response: VideoProfileCreateResponse{TaskID: taskIDs},
		},
		Poll:    VideoProfilePoll{Path: "/api/v1/tasks/{task_id}", Response: response},
		Content: videoPresetContent(""),
	}
}

// videoPresetKemeiKling 对应可美 Kling 的三种模型操作；模型写在路径中，参数进 settings/options。
func videoPresetKemeiKling(operation string) VideoProtocolProfileSpec {
	duration := 5.0
	body := []VideoProfileRule{}
	capabilities := &VideoProfileCapabilities{}
	switch operation {
	case "text-to-video":
		body = append(body, VideoProfileRule{From: "prompt", To: "prompt", Required: true})
	case "image-to-video":
		// 图生视频只接受首帧与可选尾帧。
		capabilities = &VideoProfileCapabilities{MinImages: 1, MaxImages: 2, ImageRoles: []string{VideoMediaRoleFirstFrame, VideoMediaRoleLastFrame}, ExclusiveFrames: true}
		body = append(body,
			VideoProfileRule{From: "prompt", To: "contents", Each: map[string]string{"type": "prompt", "text": "$value"}, Append: true},
			VideoProfileRule{From: "images", To: "contents", Each: map[string]string{"type": "$role", "url": "$url"}, Roles: map[string]string{VideoMediaRoleFirstFrame: "first_frame", VideoMediaRoleLastFrame: "last_frame"}, Append: true},
		)
	default:
		// 全能视频：参考图改名为 refer_image，参考视频作为 feature_video，至多一个视频。
		capabilities = &VideoProfileCapabilities{MaxVideos: 1}
		body = append(body,
			VideoProfileRule{From: "prompt", To: "contents", Each: map[string]string{"type": "prompt", "text": "$value"}, Append: true},
			VideoProfileRule{From: "images", To: "contents", Each: map[string]string{"type": "$role", "url": "$url"}, Roles: map[string]string{VideoMediaRoleFirstFrame: "first_frame", VideoMediaRoleLastFrame: "last_frame", VideoMediaRoleReferenceImage: "refer_image"}, Append: true},
			VideoProfileRule{From: "videos", To: "contents", Each: map[string]string{"type": "feature_video", "url": "$url"}, Append: true},
		)
	}
	body = append(body,
		videoPresetMove("negative_prompt", "negative_prompt"), videoPresetMove("seed", "seed"),
		videoPresetMove("duration", "settings.duration"), VideoProfileRule{From: "resolution", To: "settings.resolution", Type: "resolution"}, videoPresetMove("aspect_ratio", "settings.aspect_ratio"),
		VideoProfileRule{From: "generate_audio", To: "settings.audio", Map: map[string]any{"true": "native", "false": "off"}, Default: videoPresetRaw("off")},
		videoPresetMove("extra.multi_shot", "settings.multi_shot"),
		videoPresetMove("watermark", "options.watermark_info.enabled"),
		videoPresetMove("extra.callback_url", "options.callback_url"), videoPresetMove("extra.external_task_id", "options.external_task_id"),
	)
	createIDs := []string{"data.task_id", "task_id", "data.id", "id"}
	response := videoPresetKemeiResponse([]string{"task_id", "id", "data.task_id", "data.id"})
	response.List = &VideoProfileList{Path: "data", Match: VideoProfilePaths{"task_id", "id"}, Optional: true}
	// 官方 Kling 只认任务内唯一的视频产物，直链与实际秒数取自该产物；完成时缺任一项都不能计价。
	response.Outputs = &VideoProfileOutputs{Path: "outputs", Max: 1, Filter: map[string]string{"type": "video"}, URL: []string{"url"}, Seconds: []string{"duration"}}
	// Kling 没有已核实的 Token 用量字段，外层 usage 占位不能当作免费完成；分辨率来自任务回显的 settings。
	response.Tokens = nil
	response.Resolution = []string{"settings.resolution", "resolution"}
	response.Seconds = append(append([]string{}, videoPresetKemeiSeconds...), "task_result.videos.0.duration")
	return VideoProtocolProfileSpec{
		Auth:         VideoProfileAuth{Scheme: "bearer"},
		Capabilities: capabilities,
		Create: VideoProfileCreate{
			Path:             "/" + operation + "/{model}",
			Defaults:         VideoProfileDefaults{Duration: &duration, Resolution: "720p", AspectRatio: "16:9"},
			ImageRoleDefault: map[bool]string{true: "", false: "frames"}[operation == "text-to-video"],
			Body:             body,
			Response: VideoProfileCreateResponse{
				TaskID:       createIDs,
				RejectedWhen: []VideoProfileCondition{{Path: "code", NotIn: []any{float64(0)}}},
			},
		},
		Poll: VideoProfilePoll{
			Path: "/tasks", Query: map[string]string{"task_ids": "{task_id}"}, Response: response,
		},
		Content: videoPresetContent(""),
	}
}

// videoPresetKemeiVidu 对应可美 Vidu 的四种操作；Vidu Q4 不支持文生视频与首尾帧。
func videoPresetKemeiVidu(operation string) VideoProtocolProfileSpec {
	duration := 5.0
	capabilities := &VideoProfileCapabilities{}
	prompt := VideoProfileRule{From: "prompt", To: "prompt"}
	images := true
	switch operation {
	case "text2video":
		capabilities.DenyModels = []string{"viduq4", "viduq4-*"}
		prompt.Required, images = true, false
	case "img2video":
		capabilities.MinImages, capabilities.MaxImages = 1, 1
	case "reference2video":
		capabilities.MinImages = 1
	case "start-end2video":
		capabilities.DenyModels = []string{"viduq4", "viduq4-*"}
		capabilities.MinImages, capabilities.MaxImages = 2, 2
	}
	body := []VideoProfileRule{videoPresetMove("model", "model"), prompt}
	if images {
		body = append(body, VideoProfileRule{From: "images", To: "images", Item: "$url"})
	}
	if operation == "reference2video" {
		body = append(body, videoPresetMove("extra.sounds", "sounds"))
	}
	body = append(body,
		videoPresetMove("duration", "duration"), VideoProfileRule{From: "resolution", To: "resolution", Type: "resolution"}, videoPresetMove("aspect_ratio", "aspect_ratio"),
		videoPresetMove("generate_audio", "audio"), videoPresetMove("seed", "seed"), videoPresetMove("watermark", "watermark"),
		videoPresetMove("extra.off_peak", "off_peak"),
	)
	return VideoProtocolProfileSpec{
		Auth:         VideoProfileAuth{Scheme: "bearer"},
		Capabilities: capabilities,
		Create: VideoProfileCreate{
			Path:     "/ent/v2/" + operation,
			Defaults: VideoProfileDefaults{Duration: &duration, Resolution: "540p", AspectRatio: "16:9"},
			Body:     body,
			Response: VideoProfileCreateResponse{TaskID: []string{"task_id", "id"}},
		},
		Poll: VideoProfilePoll{
			Path: "/ent/v2/tasks/{task_id}/creations",
			Response: VideoProfilePollResponse{
				TaskID: []string{"task_id", "id"}, Status: VideoProfilePaths{"state"},
				StatusMap: map[string]string{"created": "queued", "queueing": "queued", "queued": "queued", "processing": "processing", "success": "completed", "failed": "failed"},
				VideoURL:  []string{"creations.0.url"}, Outputs: &VideoProfileOutputs{Path: "creations", Max: 1},
				Seconds: []string{"duration"}, Resolution: []string{"resolution"},
				// 声称成功却带非零错误码时不能确认交付。
				CompletedInvalidWhen: []VideoProfileCondition{{Path: "err_code", NotIn: []any{"", "0", float64(0)}}},
			},
		},
		Content: videoPresetContent(""),
	}
}

// videoProtocolPresets 返回全部系统预置模板，键名即账号绑定中的 profile:键名。
func videoProtocolPresets() []videoProtocolPreset {
	presets := []videoProtocolPreset{
		{Key: "generic-flat", Name: "通用扁平格式（/v1/video/generations）", Spec: videoPresetGenericFlat("/v1/video/generations", "/v1/video/generations/{task_id}", "")},
		{Key: "generic-flat-videos", Name: "通用扁平格式（/v1/videos）", Spec: videoPresetGenericFlat("/v1/videos", "/v1/videos/{task_id}", "/v1/videos/{task_id}/content")},
		{Key: "metaso-h3", Name: "Metaso H3", Spec: videoPresetMetasoH3()},
		{Key: "yezonghui", Name: "Yezonghui", Spec: videoPresetYezonghui()},
		{Key: "kemei-compat", Name: "可美 /v1/video/generations", Spec: videoPresetKemeiCompat()},
		{Key: "kemei-seedance", Name: "可美 Seedance", Spec: videoPresetKemeiContent(false)},
		{Key: "kemei-minimax", Name: "可美 MiniMax", Spec: videoPresetKemeiContent(true)},
		{Key: "kemei-wan", Name: "可美万相", Spec: videoPresetKemeiWan()},
	}
	for _, op := range []struct{ key, operation, name string }{
		{"kemei-kling-text", "text-to-video", "可美 Kling 文生视频"}, {"kemei-kling-image", "image-to-video", "可美 Kling 图生视频"}, {"kemei-kling-omni", "omni-video", "可美 Kling 全能视频"},
	} {
		presets = append(presets, videoProtocolPreset{Key: op.key, Name: op.name, Spec: videoPresetKemeiKling(op.operation)})
	}
	for _, op := range []struct{ key, operation, name string }{
		{"kemei-vidu-img2video", "img2video", "可美 Vidu 图生视频"}, {"kemei-vidu-reference2video", "reference2video", "可美 Vidu 参考生视频"},
		{"kemei-vidu-text2video", "text2video", "可美 Vidu 文生视频"}, {"kemei-vidu-start-end2video", "start-end2video", "可美 Vidu 首尾帧"},
	} {
		presets = append(presets, videoProtocolPreset{Key: op.key, Name: op.name, Spec: videoPresetKemeiVidu(op.operation)})
	}
	return presets
}

// videoProtocolPresetProfile 返回内置预置模板，视为该键名的第 1 版启用模板；未知键名或其它版本返回未找到。
func videoProtocolPresetProfile(key string, version int) (*VideoProtocolProfile, error) {
	if version > 1 {
		return nil, ErrVideoProtocolProfileNotFound
	}
	for _, preset := range videoProtocolPresets() {
		if preset.Key != key {
			continue
		}
		hash, err := preset.Spec.Hash()
		if err != nil {
			return nil, err
		}
		return &VideoProtocolProfile{ProfileKey: preset.Key, Version: 1, Name: preset.Name, Status: VideoProtocolProfileActive, Spec: preset.Spec, SpecHash: hash}, nil
	}
	return nil, ErrVideoProtocolProfileNotFound
}

// EnsureVideoProtocolPresets 在启动时补齐缺失的预置模板并启用；系统创建且未被管理员改动的预置模板在内置定义更新后升为新版本，
// 管理员保存过、停用或改为草稿的键名一律不覆盖。
func EnsureVideoProtocolPresets(ctx context.Context, profiles *VideoProtocolProfileService) (int, error) {
	if profiles == nil || profiles.repo == nil {
		return 0, nil
	}
	existing, err := profiles.List(ctx)
	if err != nil {
		return 0, err
	}
	// latest 记录每个键名的最高版本：系统创建（created_by 为空）且仍启用、内容与当前内置定义不同的，视为未被管理员接管，随版本升级。
	latest := map[string]VideoProtocolProfile{}
	for _, profile := range existing {
		if current, ok := latest[profile.ProfileKey]; !ok || profile.Version > current.Version {
			latest[profile.ProfileKey] = profile
		}
	}
	created := 0
	// 管理员删除过的预置模板不再补齐。
	deleted := profiles.deletedPresets(ctx)
	for _, preset := range videoProtocolPresets() {
		if deleted[preset.Key] {
			continue
		}
		if current, ok := latest[preset.Key]; ok {
			hash, hashErr := preset.Spec.Hash()
			if hashErr != nil || current.CreatedBy != nil || current.Status != VideoProtocolProfileActive || current.SpecHash == hash {
				continue
			}
		}
		if _, err := profiles.Save(ctx, VideoProtocolProfileInput{ProfileKey: preset.Key, Name: preset.Name, Spec: preset.Spec, Activate: true}, 0); err != nil {
			slog.Warn("video protocol preset seeding failed", "profile_key", preset.Key, "error", err)
			continue
		}
		created++
	}
	if created > 0 {
		slog.Info("video protocol presets created", "count", created)
	}
	return created, nil
}

// videoDeprecatedProtocolAdapters 是已由预置模板取代的厂商方案；仅用于在途任务的恢复与结算，不能用于新绑定。
var videoDeprecatedProtocolAdapters = map[string]bool{
	VideoProtocolAdapterLegacy: true, VideoProtocolAdapterMetasoH3: true, VideoProtocolAdapterKemei: true, VideoProtocolAdapterYezonghui: true,
}

// videoDeprecatedProtocolReplacement 返回旧方案绑定的替代协议：厂商方案换为预置模板，原样协议在原生端点换为官方协议。
// 没有等价替代时返回 false（例如原样协议的旧版 Kling 路径），需要管理员手动改绑。
func videoDeprecatedProtocolReplacement(binding VideoModelProtocol) (VideoModelProtocol, bool) {
	resolved, err := parseVideoModelProtocol(binding)
	if err != nil || resolved.IsProfile() || !videoDeprecatedProtocolAdapters[resolved.Adapter] {
		return binding, false
	}
	profile := func(key string) (VideoModelProtocol, bool) {
		return VideoModelProtocol{Protocol: videoProfileProtocolPrefix + key, BaseURL: binding.BaseURL}, true
	}
	official := func(adapter string) (VideoModelProtocol, bool) {
		return VideoModelProtocol{Protocol: string(resolved.Endpoint) + "." + adapter, Path: binding.Path, BaseURL: binding.BaseURL}, true
	}
	switch resolved.Adapter {
	case VideoProtocolAdapterMetasoH3:
		return profile("metaso-h3")
	case VideoProtocolAdapterYezonghui:
		return profile("yezonghui")
	case VideoProtocolAdapterKemei:
		switch resolved.Endpoint {
		case VideoEndpointCompat:
			return profile("kemei-compat")
		case VideoEndpointSeedance:
			return profile("kemei-seedance")
		case VideoEndpointMiniMax:
			return profile("kemei-minimax")
		case VideoEndpointWan:
			return profile("kemei-wan")
		case VideoEndpointKling:
			operation := strings.TrimSuffix(strings.TrimPrefix(resolved.Path, "/"), "/{model}")
			return profile(map[string]string{"text-to-video": "kemei-kling-text", "image-to-video": "kemei-kling-image", "omni-video": "kemei-kling-omni"}[operation])
		case VideoEndpointVidu:
			return profile("kemei-vidu-" + strings.TrimPrefix(resolved.Path, "/ent/v2/"))
		}
	case VideoProtocolAdapterLegacy:
		switch resolved.Endpoint {
		case VideoEndpointCompat:
			return profile("generic-flat")
		case VideoEndpointOpenAIVideos:
			return profile("generic-flat-videos")
		case VideoEndpointSeedance:
			return official(VideoProtocolAdapterSeedance)
		case VideoEndpointMiniMax:
			return official(VideoProtocolAdapterMiniMax)
		case VideoEndpointWan:
			return official(VideoProtocolAdapterWan)
		case VideoEndpointVidu:
			return official(VideoProtocolAdapterVidu)
		case VideoEndpointKling:
			if resolved.Path == "/text-to-video/{model}" || resolved.Path == "/image-to-video/{model}" {
				return official(VideoProtocolAdapterKling)
			}
		}
	}
	return binding, false
}

// rebindDeprecatedVideoProtocols 把可替代的旧方案绑定换为预置模板或官方协议，返回是否有改动。
func rebindDeprecatedVideoProtocols(protocols map[string]VideoModelProtocol) bool {
	changed := false
	for model, binding := range protocols {
		if replacement, ok := videoDeprecatedProtocolReplacement(binding); ok {
			protocols[model] = replacement
			changed = true
		}
	}
	return changed
}
