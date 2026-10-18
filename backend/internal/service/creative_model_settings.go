package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// CreativeModelSetting 是管理员配置的创作台分组、模型和能力白名单项。
type CreativeModelSetting struct {
	GroupID    int64    `json:"group_id"`
	Model      string   `json:"model"`
	Operations []string `json:"operations"`
	// 未填写类型的历史配置继续表示图片；视频必须显式选择端点。
	MediaType         string                     `json:"media_type,omitempty"`
	VideoEndpoint     VideoEndpoint              `json:"video_endpoint,omitempty"`
	VideoCapabilities *CreativeVideoCapabilities `json:"video_capabilities,omitempty"`
}

// CreativeModelCandidate 是管理端可选择的图片、视频和音频模型候选。
type CreativeModelCandidate struct {
	GroupID           int64                      `json:"group_id"`
	GroupName         string                     `json:"group_name"`
	Platform          string                     `json:"platform"`
	Model             string                     `json:"model"`
	Operations        []string                   `json:"operations"`
	MediaType         string                     `json:"media_type,omitempty"`
	VideoEndpoints    []VideoEndpoint            `json:"video_endpoints,omitempty"`
	AudioCapabilities *CreativeAudioCapabilities `json:"audio_capabilities,omitempty"`
}

// NormalizeCreativeModelSettingsForSave 按实际分组平台清理即将保存的能力白名单。
// Gemini 的独立 PNG mask inpaint 已从创作台移除，OpenAI 的同名能力保持不变。
// 无法解析的历史分组保留原配置，避免在不知道平台时误删管理员设置。
func (s *CreativePublicService) NormalizeCreativeModelSettingsForSave(ctx context.Context, input []CreativeModelSetting) ([]CreativeModelSetting, error) {
	normalized, err := NormalizeCreativeModelSettings(input)
	if err != nil {
		return nil, err
	}
	if s == nil || s.GroupRepo == nil {
		return normalized, nil
	}
	out := make([]CreativeModelSetting, 0, len(normalized))
	resolvedModels := make(map[int64]map[string]string)
	for _, item := range normalized {
		group, lookupErr := s.GroupRepo.GetByIDLite(ctx, item.GroupID)
		if lookupErr != nil || group == nil {
			out = append(out, item)
			continue
		}
		finalModel := item.Model
		if item.MediaType == "text" {
			if !creativeTextPlatformSupported(group.Platform) {
				return nil, fmt.Errorf("creative text requires a supported chat group")
			}
			out = append(out, item)
			continue
		}
		if item.MediaType == "audio" {
			if group.Platform != PlatformGrok {
				return nil, fmt.Errorf("creative audio model requires a grok group")
			}
			out = append(out, item)
			continue
		}
		if item.MediaType == "video" {
			if group.Platform != PlatformVideo {
				return nil, fmt.Errorf("creative video model requires a video group")
			}
			out = append(out, item)
			continue
		}
		if group.Platform == PlatformVideo {
			return nil, fmt.Errorf("creative video group requires media_type video")
		}
		if strings.TrimSpace(group.Platform) == PlatformOpenAI {
			// 配置页的请求别名也必须按最终模型收窄，不能给 MJ 保存编辑与局部重绘能力。
			models, loaded := resolvedModels[group.ID]
			if !loaded {
				var modelErr error
				models, modelErr = s.creativeModelsForGroup(ctx, group)
				if modelErr != nil {
					return nil, modelErr
				}
				resolvedModels[group.ID] = models
			}
			if resolved := models[item.Model]; resolved != "" {
				finalModel = resolved
			}
		}
		midjourney := strings.TrimSpace(group.Platform) == PlatformOpenAI && IsMidjourneyImageGenerationModel(finalModel)
		operations := make([]string, 0, len(item.Operations))
		for _, operation := range item.Operations {
			if (strings.TrimSpace(group.Platform) == PlatformGemini && operation == CreativeOperationInpaint) ||
				(midjourney && operation != CreativeOperationGenerate) {
				continue
			}
			operations = append(operations, operation)
		}
		if len(operations) == 0 {
			continue
		}
		item.Operations = operations
		out = append(out, item)
	}
	return out, nil
}

// creativeOperationOrder 保证设置、候选和公开目录中的能力顺序稳定。
var creativeOperationOrder = []string{
	CreativeOperationGenerate,
	CreativeOperationEdit,
	CreativeOperationInpaint,
}

// NormalizeCreativeModelSettings 校验并规范化管理员配置。
func NormalizeCreativeModelSettings(input []CreativeModelSetting) ([]CreativeModelSetting, error) {
	out := make([]CreativeModelSetting, 0, len(input))
	seenModels := make(map[string]struct{}, len(input))
	for index, item := range input {
		if item.GroupID <= 0 {
			return nil, fmt.Errorf("creative model setting %d group_id must be positive", index)
		}
		model := strings.TrimSpace(item.Model)
		if model == "" {
			return nil, fmt.Errorf("creative model setting %d model is required", index)
		}
		mediaType := strings.ToLower(strings.TrimSpace(item.MediaType))
		if mediaType != "" && mediaType != "image" && mediaType != "video" && mediaType != "audio" && mediaType != "text" {
			return nil, fmt.Errorf("creative model setting %d media_type is invalid", index)
		}
		// 图片维持历史序列化，避免旧设置及任务指纹发生无关变化。
		if mediaType == "image" {
			mediaType = ""
		}
		// TTS 是固定音频能力，不能通过图片白名单或任意模型别名扩展上游能力。
		if (mediaType == "audio" && model != CreativeGrokTTSModel) || (mediaType != "audio" && model == CreativeGrokTTSModel) {
			return nil, fmt.Errorf("creative audio model must use grok-voice-tts and media_type audio")
		}
		var videoCapabilities *CreativeVideoCapabilities
		if mediaType == "video" {
			if !validVideoEndpoint(item.VideoEndpoint) {
				return nil, fmt.Errorf("creative video endpoint is required")
			}
			var capabilityErr error
			videoCapabilities, capabilityErr = normalizeCreativeVideoCapabilities(item.VideoEndpoint, item.VideoCapabilities)
			if capabilityErr != nil {
				return nil, capabilityErr
			}
		} else if item.VideoEndpoint != "" || item.VideoCapabilities != nil {
			return nil, fmt.Errorf("creative non-video setting cannot configure video parameters")
		}

		configured := make(map[string]struct{}, len(item.Operations))
		for _, operation := range item.Operations {
			operation = strings.ToLower(strings.TrimSpace(operation))
			if (mediaType == "video" || mediaType == "audio" || mediaType == "text") && operation != CreativeOperationGenerate {
				return nil, fmt.Errorf("creative %s only supports generate", mediaType)
			}
			switch operation {
			case CreativeOperationGenerate, CreativeOperationEdit, CreativeOperationInpaint:
				configured[operation] = struct{}{}
			default:
				return nil, fmt.Errorf("creative model setting %d operation %q is invalid", index, operation)
			}
		}
		if len(configured) == 0 {
			return nil, fmt.Errorf("creative model setting %d must contain at least one operation", index)
		}

		modelKey := fmt.Sprintf("%d:%s", item.GroupID, model)
		if _, exists := seenModels[modelKey]; exists {
			return nil, fmt.Errorf("creative model setting for group %d model %q is duplicated", item.GroupID, model)
		}
		seenModels[modelKey] = struct{}{}

		operations := make([]string, 0, len(configured))
		for _, operation := range creativeOperationOrder {
			if _, ok := configured[operation]; ok {
				operations = append(operations, operation)
			}
		}
		out = append(out, CreativeModelSetting{
			GroupID:           item.GroupID,
			Model:             model,
			Operations:        operations,
			MediaType:         mediaType,
			VideoEndpoint:     item.VideoEndpoint,
			VideoCapabilities: videoCapabilities,
		})
	}
	return out, nil
}

// parseCreativeModelSettings 解析持久化设置；任何异常都按空白名单处理，避免误放行模型。
func parseCreativeModelSettings(raw string) []CreativeModelSetting {
	if strings.TrimSpace(raw) == "" {
		return []CreativeModelSetting{}
	}
	var input []CreativeModelSetting
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		slog.Warn("invalid persisted creative model settings", "error", err)
		return []CreativeModelSetting{}
	}
	normalized, err := NormalizeCreativeModelSettings(input)
	if err != nil {
		slog.Warn("invalid persisted creative model settings", "error", err)
		return []CreativeModelSetting{}
	}
	return normalized
}

// marshalCreativeModelSettings 校验后生成稳定 JSON，供 settings 表持久化。
func marshalCreativeModelSettings(input []CreativeModelSetting) (string, []CreativeModelSetting, error) {
	normalized, err := NormalizeCreativeModelSettings(input)
	if err != nil {
		return "", nil, err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", nil, fmt.Errorf("marshal creative model settings: %w", err)
	}
	return string(raw), normalized, nil
}

// creativeModelSettingsIndex 将配置转换为精确的分组+模型索引。
func creativeModelSettingsIndex(settings []CreativeModelSetting) map[string][]string {
	index := make(map[string][]string, len(settings))
	for _, setting := range settings {
		// 图片入口不能借用音视频白名单绕过各自的准入及账本。
		if setting.MediaType == "video" || setting.MediaType == "audio" || setting.MediaType == "text" {
			continue
		}
		key := fmt.Sprintf("%d:%s", setting.GroupID, strings.TrimSpace(setting.Model))
		index[key] = append([]string(nil), setting.Operations...)
	}
	return index
}

// creativeOperationsForModel 计算配置能力与平台能力的交集。
func creativeOperationsForModel(settings map[string][]string, groupID int64, model string, supported []string) ([]string, bool) {
	configured, ok := settings[fmt.Sprintf("%d:%s", groupID, strings.TrimSpace(model))]
	if !ok {
		return nil, false
	}
	supportedSet := make(map[string]struct{}, len(supported))
	for _, operation := range supported {
		supportedSet[operation] = struct{}{}
	}
	operations := make([]string, 0, len(configured))
	for _, operation := range creativeOperationOrder {
		if containsCreativeOperation(configured, operation) {
			if _, supported := supportedSet[operation]; supported {
				operations = append(operations, operation)
			}
		}
	}
	return operations, true
}

func containsCreativeOperation(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
