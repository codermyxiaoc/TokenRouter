package service

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/tidwall/gjson"
)

// 官方方舟的参数与计费事实独立于共享 content 转换，不能收紧可美或原有协议。
// @project-doc docs/interfaces/video_upstream.md#video_adapters
func prepareVideoOfficialSeedance(values map[string]json.RawMessage) ([]byte, VideoRequestMetadata, error) {
	body, metadata, err := prepareVideoOfficialContent(values, VideoEndpointSeedance)
	if err != nil {
		return nil, metadata, err
	}
	root := gjson.ParseBytes(body)
	model := strings.ToLower(root.Get("model").String())
	if !videoSeedanceFrameNamesCanonical(root) {
		return nil, VideoRequestMetadata{}, videoProtocolError("Seedance 帧数字段必须使用 frames 和 framespersecond")
	}
	if frames := root.Get("frames"); frames.Exists() {
		if !videoSeedanceFramesValid(frames) {
			return nil, VideoRequestMetadata{}, videoProtocolError("Seedance frames 必须为 29–289 范围内符合 25+4n 的整数")
		}
		if videoSeedanceOfficialModel(model, "doubao-seedance-2-0") || videoSeedanceOfficialModel(model, "doubao-seedance-2-0-mini") ||
			videoSeedanceOfficialModel(model, "doubao-seedance-2-0-fast") || videoSeedanceOfficialModel(model, "doubao-seedance-2-5") {
			return nil, VideoRequestMetadata{}, videoProtocolError("该 Seedance 官方模型不支持 frames，请使用 duration")
		}
		if fps := root.Get("framespersecond"); fps.Exists() && (fps.Type != gjson.Number || fps.Float() != 24) {
			return nil, VideoRequestMetadata{}, videoProtocolError("Seedance 官方帧数模式使用固定 24 FPS")
		}
		// 上游优先使用 frames，保留原始参数，仅按真正发送的帧数计算预算，不能沿用较短的 duration。
		metadata.DurationSeconds = frames.Float() / 24
	}
	if videoSeedanceOfficialModel(model, "doubao-seedance-2-5") && videoSeedanceHasBoundaryImage(root) {
		if ratio := root.Get("ratio"); ratio.Exists() && (ratio.Type != gjson.String || ratio.String() != "adaptive") {
			return nil, VideoRequestMetadata{}, videoProtocolError("Seedance 2.5 首帧/尾帧图生视频仅支持 adaptive 比例；可省略 ratio 使用官方默认值")
		}
	}
	resolution := root.Get("resolution").String()
	if resolution == "" {
		// 省略时交给当前模型使用官方默认值，不擅自增加参数或改变报价维度。
		return body, metadata, nil
	}
	switch resolution {
	case "480p", "720p", "1080p", "4k":
	default:
		return nil, VideoRequestMetadata{}, videoProtocolError("Seedance 官方分辨率仅支持 480p、720p、1080p 或 4k，具体档位取决于模型；不支持 768p")
	}
	if videoSeedanceOfficialModel(model, "doubao-seedance-2-0-mini") || videoSeedanceOfficialModel(model, "doubao-seedance-2-0-fast") {
		if resolution != "480p" && resolution != "720p" {
			return nil, VideoRequestMetadata{}, videoProtocolError("Seedance 2.0 Mini/Fast 官方分辨率仅支持 480p 或 720p；可留空使用上游默认值")
		}
	}
	if resolution == "4k" && (videoSeedanceOfficialModel(model, "doubao-seedance-2-5") ||
		videoSeedanceOfficialModel(model, "doubao-seedance-1-0-pro") || videoSeedanceOfficialModel(model, "doubao-seedance-1-0-pro-fast")) {
		return nil, VideoRequestMetadata{}, videoProtocolError("该 Seedance 官方模型仅支持 480p、720p 或 1080p；可留空使用上游默认值")
	}
	return body, metadata, nil
}

// 固定帧率和离散帧数来自官方 1.0 帧数合同，不能通过四舍五入改变实际时长。
func videoSeedanceFramesValid(frames gjson.Result) bool {
	n := frames.Float()
	return frames.Type == gjson.Number && n >= 29 && n <= 289 && math.Trunc(n) == n && (int64(n)-25)%4 == 0
}

// 仅在官方适配内约束字段大小写，避免改变旧透传协议的扩展字段处理。
func videoSeedanceFrameNamesCanonical(root gjson.Result) bool {
	valid := true
	root.ForEach(func(key, _ gjson.Result) bool {
		name := key.String()
		folded := strings.ToLower(name)
		if (folded == "frames" || folded == "framespersecond") && name != folded {
			valid = false
		}
		return valid
	})
	return valid
}

// 首帧/尾帧与多参考图的约束不同，不能只因请求含有图片就限制其比例。
func videoSeedanceHasBoundaryImage(root gjson.Result) bool {
	for _, item := range root.Get("content").Array() {
		role := item.Get("role").String()
		if item.Get("type").String() == "image_url" && (role == "first_frame" || role == "last_frame") {
			return true
		}
	}
	return false
}

// 完成包的帧数必须有明确帧率；不完整、冲突或非法事实进入核对，不能退回请求预估时长。
func applyVideoOfficialSeedanceResponse(out *VideoUpstreamResponse, root gjson.Result) {
	if !json.Valid(out.Body) || !root.IsObject() {
		// 解析器可以读取非法尾串前的对象，但它不能成为扣费或退款依据；保留任务 ID 等待重新查询。
		out.Status, out.UpstreamStatus, out.VideoURL = "", "", ""
		out.Metadata = VideoRequestMetadata{}
		out.MetadataError = "Invalid official Seedance response JSON"
		return
	}
	if !videoSeedanceFrameNamesCanonical(root) {
		out.Metadata.DurationSeconds = 0
		out.MetadataError = "Noncanonical Seedance frame fields"
		return
	}
	frames := root.Get("frames")
	if !frames.Exists() || out.Status != "completed" {
		return
	}
	out.Metadata.DurationSeconds = 0
	fps := root.Get("framespersecond")
	if !videoSeedanceFramesValid(frames) || fps.Type != gjson.Number || fps.Float() != 24 {
		out.MetadataError = "Invalid Seedance output frames or frame rate"
		return
	}
	// 官方只返回 duration、frames 之一，不能从同时出现的两套用量中挑选较便宜的一套。
	if root.Get("duration").Exists() {
		out.MetadataError = "Conflicting Seedance output duration and frames"
		return
	}
	out.Metadata.DurationSeconds = frames.Float() / fps.Float()
}

// 仅识别官方名称及六位日期版本；ep 接入点和自定义别名不能被子串匹配误认成某一模型。
func videoSeedanceOfficialModel(model, name string) bool {
	if model == name {
		return true
	}
	suffix, ok := strings.CutPrefix(model, name+"-")
	if !ok || len(suffix) != 6 {
		return false
	}
	for _, digit := range suffix {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}
