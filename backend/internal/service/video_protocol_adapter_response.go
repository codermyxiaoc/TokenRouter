package service

import (
	"math"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// VideoProtocolResponseMatchesTask 在重放、恢复和轮询阶段使用同一份冻结协议校验归属。
func VideoProtocolResponseMatchesTask(target VideoUpstreamTarget, body []byte, taskID string) bool {
	if target.Version == videoTargetVersionProfile {
		return videoProfileResponseMatchesTask(target, body, taskID)
	}
	if ValidateVideoProtocolTarget(target) != nil {
		return false
	}
	if adapter, _ := VideoTargetProtocolAdapter(target); adapter == VideoProtocolAdapterYezonghui {
		return taskID == "" || videoYezonghuiResponseIdentityMatches(body, taskID)
	}
	return videoResponseMatchesTask(videoProtocolResponseIdentityEndpoint(target, body), body, taskID)
}

// VideoProtocolResponseHasConflictingTaskIDs 只识别任务字段，不把 Kling outputs[].id 产物 ID 当成任务。
func VideoProtocolResponseHasConflictingTaskIDs(target VideoUpstreamTarget, body []byte) bool {
	if target.Version == videoTargetVersionProfile {
		return videoProfileHasConflictingTaskIDs(target, body)
	}
	if adapter, err := VideoTargetProtocolAdapter(target); err == nil && adapter == VideoProtocolAdapterYezonghui {
		return ValidateVideoProtocolTarget(target) != nil || !videoYezonghuiResponseIdentityMatches(body, "")
	}
	return ValidateVideoProtocolTarget(target) != nil || videoResponseHasConflictingTaskIDs(videoProtocolResponseIdentityEndpoint(target, body), body)
}

// ExtractVideoProtocolResponse 保留原生正文，官方适配器只补充明确的规范观察字段。
func ExtractVideoProtocolResponse(target VideoUpstreamTarget, status int, body []byte, expectedTaskID string) (*VideoUpstreamResponse, error) {
	if target.Version == videoTargetVersionProfile {
		return extractVideoProfileResponse(target, status, body, expectedTaskID)
	}
	if err := ValidateVideoProtocolTarget(target); err != nil {
		return nil, err
	}
	if !VideoProtocolResponseMatchesTask(target, body, expectedTaskID) {
		return nil, errVideoTaskResponseMismatch
	}
	id, _ := VideoTargetProtocolAdapter(target)
	if id == VideoProtocolAdapterYezonghui {
		// 本协议的 size 表示宽高比，不能经过 OpenAI 像素尺寸的响应解析器。
		return extractVideoYezonghuiResponse(status, body, expectedTaskID), nil
	}
	profile := ""
	if id == VideoProtocolAdapterMetasoH3 {
		profile = VideoOpenAIProfileMetasoH3
	}
	responseEndpoint := videoProtocolResponseIdentityEndpoint(target, body)
	if id == VideoProtocolAdapterOpenAI {
		// 兼容路径也可显式使用 OpenAI 字段；seconds/size 必须按选定参数合同提取，查询路径仍取冻结端点。
		responseEndpoint = VideoEndpointOpenAIVideos
	}
	out := ExtractVideoUpstreamResponseWithProfile(responseEndpoint, body, expectedTaskID, profile)
	out.StatusCode, out.Body = status, body
	if (id == VideoProtocolAdapterLegacy && target.Endpoint != VideoEndpointVidu) || id == VideoProtocolAdapterMetasoH3 {
		return out, nil
	}
	// 创建冲突仍保留受理证据供状态机核对，不能误当成无 ID 超时后直接退款。
	if VideoProtocolResponseHasConflictingTaskIDs(target, body) {
		out.TaskID, out.Status, out.VideoURL = "", "", ""
		out.Metadata = VideoRequestMetadata{}
		out.MetadataError = "Conflicting video task identifiers"
		return out, nil
	}
	root := gjson.ParseBytes(body)
	if root.IsObject() {
		if err := validateVideoJSONKeys(body); err != nil {
			// 官方响应含重复或大小写冲突字段时，不能选择某个失败状态退款。
			// 保留原包和已识别的受理 ID，后续仅通过原任务查询恢复。
			out.Status, out.UpstreamStatus, out.VideoURL = "", "", ""
			out.Metadata = VideoRequestMetadata{}
			out.MetadataError = "Ambiguous official video response"
			return out, nil
		}
	}
	validateVideoOfficialUsage(out, root, id)
	switch id {
	case VideoProtocolAdapterLegacy:
		// Vidu 新增的保留协议仅透传请求；仍识别该端点的 state/creations，避免完成任务永久排队。
		applyVideoKemeiViduResponse(out, root)
	case VideoProtocolAdapterVidu:
		applyVideoOfficialViduResponse(out, root)
	case VideoProtocolAdapterKemei:
		applyVideoKemeiResponse(out, root, target, expectedTaskID)
	case VideoProtocolAdapterWan:
		applyVideoOfficialWanResponse(out, root)
	case VideoProtocolAdapterKling:
		applyVideoOfficialKlingResponse(out, root, expectedTaskID)
	case VideoProtocolAdapterSeedance:
		applyVideoOfficialSeedanceResponse(out, root)
	}
	return out, nil
}

// 已给出的非法用量不能被解析成“未提供”后退回请求预估值，旧原样协议不改变规则。
func validateVideoOfficialUsage(out *VideoUpstreamResponse, root gjson.Result, adapter string) {
	if adapter == VideoProtocolAdapterMiniMax && root.Get("task").IsObject() {
		root = root.Get("task")
	}
	if adapter == VideoProtocolAdapterKling {
		return // 任务数组与产物级用量由 Kling 专属解析处理。
	}
	usage := root.Get("usage")
	if usage.Exists() && !usage.IsObject() {
		out.MetadataError = "Invalid official video usage object"
		return
	}
	var tokens *int64
	for _, field := range []string{"completion_tokens", "output_tokens"} {
		value := usage.Get(field)
		if !value.Exists() {
			continue
		}
		if value.Type != gjson.Number || value.Float() < 0 || value.Float() > math.MaxInt32 || math.Trunc(value.Float()) != value.Float() {
			out.MetadataError = "Invalid official video output tokens"
			return
		}
		n := value.Int()
		if tokens != nil && *tokens != n {
			out.MetadataError = "Conflicting official video output tokens"
			return
		}
		tokens = &n
	}
	// 不改写原解析器对零占位的判断，仅拒绝明确非法或冲突的用量。
	if adapter == VideoProtocolAdapterMiniMax || adapter == VideoProtocolAdapterSeedance {
		fields := []string{"duration"}
		if adapter == VideoProtocolAdapterMiniMax {
			fields = append([]string{"usage.output_seconds"}, fields...)
		}
		for _, field := range fields {
			if value := root.Get(field); value.Exists() {
				if _, ok := videoProtocolObservedSeconds(value, false); !ok {
					out.MetadataError = "Invalid official video output duration"
				}
				break // 明确的实际输出秒数优先于回显的请求时长。
			}
		}
		if value := root.Get("resolution"); value.Exists() && (value.Type != gjson.String || strings.TrimSpace(value.String()) == "") {
			out.MetadataError = "Invalid official video output resolution"
		}
	}
}

// 万相实际输出时长优先于合计时长；SR 是数字分辨率，不能误读请求的默认档位。
func applyVideoOfficialWanResponse(out *VideoUpstreamResponse, root gjson.Result) {
	usage := root.Get("usage")
	if !videoIdentityObjectKeysUnique(root, "usage", "output") || !videoIdentityObjectKeysUnique(usage, "output_video_duration", "duration", "SR") {
		out.MetadataError = "Ambiguous Wan video usage"
		return
	}
	for _, key := range []string{"output_video_duration", "duration"} {
		value := usage.Get(key)
		if !value.Exists() {
			continue
		}
		seconds, ok := videoProtocolObservedSeconds(value, false)
		if !ok {
			out.MetadataError = "Invalid Wan output video duration"
			return
		}
		out.Metadata.DurationSeconds = seconds
		break
	}
	if sr := usage.Get("SR"); sr.Exists() {
		if sr.Type != gjson.Number || sr.Float() <= 0 || sr.Float() > 10000 || math.Trunc(sr.Float()) != sr.Float() {
			out.MetadataError = "Invalid Wan output video resolution"
			return
		}
		out.Metadata.Resolution = strconv.FormatInt(sr.Int(), 10) + "p"
	}
}

// Kling 新版返回任务数组与多种产物；只接收对应任务的唯一视频产物。
func applyVideoOfficialKlingResponse(out *VideoUpstreamResponse, root gjson.Result, expectedTaskID string) {
	// 新合同只从本任务的 settings/outputs 获取媒体事实，没有已核实的 Token 用量字段。
	// 外层兼容扩展的 usage=0 不能使按 Token 定价任务被当作免费完成。
	out.Metadata, out.VideoURL = VideoRequestMetadata{}, ""
	if out.TaskID == "" && out.Status == "failed" && root.Get("code").Type == gjson.Number && root.Get("code").Int() != 0 && !videoCreationHasAcceptanceEvidence(out.Body) {
		// 未受理的业务拒绝不能被空 data 覆盖为未知状态，否则预扣会一直冻结。
		return
	}
	if out.Status == "failed" && out.UpstreamStatus == "" && videoCreationHasAcceptanceEvidence(out.Body) {
		// 必须在任何结构错误返回前撤销业务码推断，避免畸形任务数组掩盖受理证据。
		out.Status = ""
	}
	task := root.Get("data")
	if task.IsArray() {
		items := task.Array()
		if len(items) != 1 {
			out.MetadataError = "Kling video response must contain exactly one task"
			return
		}
		task = items[0]
	} else if !task.IsObject() {
		task = root
	}
	if id := firstVideoString(task, "id", "task_id"); id != "" {
		if expectedTaskID != "" && id != expectedTaskID {
			out.MetadataError = "Kling response task does not match"
			return
		}
		out.TaskID = id
	}
	if !videoIdentityObjectKeysUnique(task, "status", "outputs", "settings") {
		out.MetadataError = "Ambiguous Kling video response"
		return
	}
	if settings := task.Get("settings"); settings.Exists() {
		if !settings.IsObject() {
			out.MetadataError = "Invalid Kling output settings"
			return
		}
		if resolution := settings.Get("resolution"); resolution.Exists() {
			if resolution.Type != gjson.String || strings.TrimSpace(resolution.String()) == "" {
				out.MetadataError = "Invalid Kling output resolution"
				return
			}
			out.Metadata.Resolution = resolution.String()
		}
	}
	// 状态映射沿用已验证的通用规则，但不读取 outputs 内的同名字段。
	if task.Raw != "" {
		observation := ExtractVideoUpstreamResponse(VideoEndpointKling, []byte(task.Raw), out.TaskID)
		out.UpstreamStatus, out.Status = observation.UpstreamStatus, observation.Status
		if out.Status == "failed" && out.UpstreamStatus == "" && videoCreationHasAcceptanceEvidence(out.Body) {
			// 外层错误码与受理证据并存时仅等待查询，不能把业务码当作任务失败终态。
			out.Status = ""
		}
	}
	outputs := task.Get("outputs")
	if !outputs.Exists() {
		if out.Status == "completed" {
			out.MetadataError = "Completed Kling task has no output evidence"
		}
		return
	}
	if !outputs.IsArray() {
		out.MetadataError = "Kling outputs must be an array"
		return
	}
	count := 0
	for _, output := range outputs.Array() {
		if !videoIdentityObjectKeysUnique(output, "type", "url", "duration") {
			out.MetadataError = "Ambiguous Kling output"
			return
		}
		if output.Get("type").String() != "video" {
			continue
		}
		count++
		if count > 1 {
			out.MetadataError = "Multiple Kling video outputs require explicit pricing support"
			return
		}
		out.VideoURL = firstVideoString(output, "url")
		if duration := output.Get("duration"); duration.Exists() {
			seconds, ok := videoProtocolObservedSeconds(duration, true)
			if !ok {
				out.MetadataError = "Invalid Kling output video duration"
				return
			}
			out.Metadata.DurationSeconds = seconds
		}
	}
	if out.Status == "completed" && count != 1 {
		out.MetadataError = "Completed Kling task requires one video output"
	} else if out.Status == "completed" && out.Metadata.DurationSeconds <= 0 {
		out.MetadataError = "Completed Kling video has no output duration"
	} else if out.Status == "completed" && out.VideoURL == "" {
		out.MetadataError = "Completed Kling video has no output URL"
	}
}

const videoKlingResolutionConfirmationRequired = "Kling output resolution requires confirmation"

// 只有能由唯一视频产物直接证伪的缺失/时长错误，才允许不返回 settings 的官方查询恢复。
func videoKlingOutputErrorRecoverable(message string) bool {
	switch message {
	case "Completed Kling task has no output evidence", "Completed Kling task requires one video output",
		"Invalid Kling output video duration", "Completed Kling video has no output duration", "Completed Kling video has no output URL":
		return true
	}
	return false
}

// 某些官方查询不回显分辨率；只补齐已确认缺失的产物，不能抹掉先前价格档位冲突。
func videoProtocolMetadataRecovered(task *VideoTaskRecord, response *VideoUpstreamResponse) bool {
	if videoViduResponseTarget(task.Target) && videoViduOutputErrorRecoverable(task.ResponseMetadataError) {
		return videoViduMetadataRecovered(task, response)
	}
	if task.Quote == nil || response.Metadata.DurationSeconds <= 0 || (task.Quote.Mode == BillingModeVideoToken && response.Metadata.Tokens == nil) {
		return false
	}
	if response.Metadata.Resolution != "" {
		return true // 后续由冻结价卡校验真实分辨率是否一致。
	}
	id, err := VideoTargetProtocolAdapter(task.Target)
	return err == nil && id == VideoProtocolAdapterKling && videoKlingOutputErrorRecoverable(task.ResponseMetadataError) &&
		(!videoProtocolQuoteRequiresResolution(task.Quote) || (task.Quote.Resolution != "" && task.Metadata.Resolution == task.Quote.Resolution)) && safeMediaPreviewURL(response.VideoURL)
}

// 无分辨率条件的统一按次兜底价，不要求上游额外回显与价格无关的档位。
func videoProtocolQuoteRequiresResolution(quote *VideoPriceQuote) bool {
	return quote != nil && !(quote.Mode == BillingModeVideoPerRequest && quote.Resolution == "" && quote.FallbackUsed)
}

// 终态允许小数秒，但不接受 NaN、Infinity、负值或字符串空白隐式转换。
func videoProtocolObservedSeconds(value gjson.Result, allowString bool) (float64, bool) {
	seconds, valid := value.Float(), value.Type == gjson.Number
	if allowString && value.Type == gjson.String && value.String() == strings.TrimSpace(value.String()) {
		parsed, err := strconv.ParseFloat(value.String(), 64)
		seconds, valid = parsed, err == nil
	}
	return seconds, valid && seconds > 0 && seconds < math.MaxInt32 && !math.IsNaN(seconds) && !math.IsInf(seconds, 0)
}
