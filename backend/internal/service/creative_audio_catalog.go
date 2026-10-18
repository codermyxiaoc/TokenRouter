package service

import (
	"context"
	"strings"
)

const (
	// CreativeGrokTTSModel 是创作台虚拟模型；上游 /tts 请求本身没有 model 参数。
	CreativeGrokTTSModel = "grok-voice-tts"
	// CreativeGrokTTSSelectionModel 与正式 Voice 入口使用相同的账号模型准入。
	CreativeGrokTTSSelectionModel = "grok-4.5"
)

// CreativeAudioCapabilities 公开经过服务端限制的 TTS 参数，不允许管理员扩展上游能力。
type CreativeAudioCapabilities struct {
	Voices          []string `json:"voices"`
	Formats         []string `json:"formats"`
	SpeedMin        float64  `json:"speed_min"`
	SpeedMax        float64  `json:"speed_max"`
	DefaultVoice    string   `json:"default_voice"`
	DefaultFormat   string   `json:"default_format"`
	DefaultSpeed    float64  `json:"default_speed"`
	DefaultLanguage string   `json:"default_language"`
	MaxInputChars   int      `json:"max_input_chars"`
}

// 每次返回独立切片，防止调用方改动一份目录后污染其它用户的能力集合。
func creativeGrokTTSCapabilities() *CreativeAudioCapabilities {
	return &CreativeAudioCapabilities{
		Voices: []string{"eve", "ara", "rex", "sal", "leo"}, Formats: []string{"mp3", "wav"},
		SpeedMin: 0.7, SpeedMax: 1.5, DefaultVoice: "eve", DefaultFormat: "mp3",
		DefaultSpeed: 1, DefaultLanguage: "auto", MaxInputChars: 15000,
	}
}

// creativeAudioConfigured 只接受管理员显式开启的精确分组与固定 TTS 模型。
func (s *CreativePublicService) creativeAudioConfigured(ctx context.Context, groupID int64, model string) bool {
	return creativeAudioSettingConfigured(s.creativeModelSettings(ctx), groupID, model)
}

func creativeAudioSettingConfigured(settings []CreativeModelSetting, groupID int64, model string) bool {
	if strings.TrimSpace(model) != CreativeGrokTTSModel {
		return false
	}
	for _, setting := range settings {
		if setting.MediaType == "audio" && setting.GroupID == groupID && setting.Model == CreativeGrokTTSModel &&
			containsCreativeOperation(setting.Operations, CreativeOperationGenerate) {
			return true
		}
	}
	return false
}

// creativeAudioGroupEligible 只读扫描真实 Voice 候选，不获取并发槽、不探测计费、不请求上游。
// Voice 复用文本能力，不能因图片开关或媒体付费资格关闭而错误隐藏音频模型。
func (s *CreativePublicService) creativeAudioGroupEligible(ctx context.Context, group *Group) (bool, error) {
	if s == nil || s.AccountRepo == nil || group == nil || !group.IsActive() || group.Platform != PlatformGrok {
		return false, nil
	}
	accounts, err := s.AccountRepo.ListSchedulableByGroupIDAndPlatform(ctx, group.ID, PlatformGrok)
	if err != nil {
		return false, err
	}
	for i := range accounts {
		account := &accounts[i]
		if account.Platform != PlatformGrok || (account.Type != AccountTypeAPIKey && account.Type != AccountTypeOAuth) ||
			!account.IsSchedulable() || !account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityTextGeneration) ||
			!account.IsModelSupported(CreativeGrokTTSSelectionModel) || account.isModelRateLimitedWithContext(ctx, CreativeGrokTTSSelectionModel) {
			continue
		}
		return true, nil
	}
	return false, nil
}

func (s *CreativePublicService) creativeAudioCandidatesForGroup(ctx context.Context, group *Group) ([]CreativeModelCandidate, error) {
	out := []CreativeModelCandidate{}
	eligible, err := s.creativeAudioGroupEligible(ctx, group)
	if err != nil || !eligible {
		return out, err
	}
	return append(out, CreativeModelCandidate{
		GroupID: group.ID, GroupName: group.Name, Platform: PlatformGrok,
		Model: CreativeGrokTTSModel, MediaType: "audio", Operations: []string{CreativeOperationGenerate},
		AudioCapabilities: creativeGrokTTSCapabilities(),
	}), nil
}

func (s *CreativePublicService) creativeAudioModelsForGroup(ctx context.Context, group *Group, settings []CreativeModelSetting) ([]CreativeModelPublic, error) {
	out := []CreativeModelPublic{}
	if group == nil || !creativeAudioSettingConfigured(settings, group.ID, CreativeGrokTTSModel) {
		return out, nil
	}
	eligible, err := s.creativeAudioGroupEligible(ctx, group)
	if err != nil || !eligible {
		return out, err
	}
	return append(out, CreativeModelPublic{
		GroupID: group.ID, GroupName: group.Name, Model: CreativeGrokTTSModel, MediaType: "audio",
		Operations: []string{CreativeOperationGenerate}, AudioCapabilities: creativeGrokTTSCapabilities(),
		OutputCount: 1, MaxOutputCount: 1,
	}), nil
}
