//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 音频目录测试使用纯内存账号，不创建任务、不访问真实供应商。
func newCreativeAudioCatalogTestService() *CreativePublicService {
	svc := newCreativeTestService()
	group := &Group{ID: 12, Name: "Grok Voice", Platform: PlatformGrok, Status: StatusActive}
	svc.GroupRepo = &creativeFakeGroupRepo{byID: map[int64]*Group{12: group}, active: []Group{*group}}
	svc.AccountRepo = &creativeFakeAccountRepo{byGroup: map[int64][]Account{12: {{
		ID: 55, Platform: PlatformGrok, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"model_whitelist": []any{CreativeGrokTTSSelectionModel}},
	}}}}
	svc.Settings = &creativeFakeSettingReader{enabled: true, models: []CreativeModelSetting{{
		GroupID: 12, Model: CreativeGrokTTSModel, MediaType: "audio", Operations: []string{CreativeOperationGenerate},
	}}}
	return svc
}

func TestCreativeAudioCatalogAvailableWithoutImagePermission(t *testing.T) {
	svc := newCreativeAudioCatalogTestService()
	ctx := context.Background()
	models, err := svc.ListModels(ctx, 7)
	require.NoError(t, err)
	require.Len(t, models.Data, 1)
	model := models.Data[0]
	require.Equal(t, "audio", model.MediaType)
	require.Equal(t, CreativeGrokTTSModel, model.Model)
	require.Equal(t, []string{CreativeOperationGenerate}, model.Operations)
	require.Equal(t, 1, model.OutputCount)
	require.Equal(t, 1, model.MaxOutputCount)
	require.Equal(t, 0, model.MaxReferenceImages)
	require.Equal(t, creativeGrokTTSCapabilities(), model.AudioCapabilities)
	require.Empty(t, model.ImageSizes)
	require.Nil(t, model.VideoCapabilities)
	candidates, err := svc.ListCreativeModelCandidates(ctx)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, "audio", candidates[0].MediaType)
	require.Equal(t, creativeGrokTTSCapabilities(), candidates[0].AudioCapabilities)
	// 读取目录不得预扣、入队或供应执行 Key。
	require.Zero(t, svc.BillingRepo.(*creativeFakeBillingRepo).reserveN)
	require.Empty(t, svc.Repo.(*creativeFakeRunRepo).runs)
}

func TestCreativeAudioCatalogKeepsImageModelsIndependent(t *testing.T) {
	svc := newCreativeAudioCatalogTestService()
	svc.GroupRepo.(*creativeFakeGroupRepo).active[0].AllowImageGeneration = true
	svc.AccountRepo.(*creativeFakeAccountRepo).byGroup[12][0].Credentials["model_whitelist"] = []any{CreativeGrokTTSSelectionModel, "grok-imagine"}
	svc.Settings.(*creativeFakeSettingReader).models = append(svc.Settings.(*creativeFakeSettingReader).models,
		CreativeModelSetting{GroupID: 12, Model: "grok-imagine", Operations: []string{CreativeOperationGenerate, CreativeOperationEdit}})
	models, err := svc.ListModels(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, models.Data, 2)
	require.Equal(t, "audio", models.Data[0].MediaType)
	require.Equal(t, "image", models.Data[1].MediaType)
	require.Equal(t, []string{CreativeOperationGenerate, CreativeOperationEdit}, models.Data[1].Operations)
	require.Nil(t, models.Data[1].AudioCapabilities)
	index := creativeModelSettingsIndex(svc.Settings.(*creativeFakeSettingReader).models)
	require.Len(t, index, 1)
	require.Contains(t, index, "12:grok-imagine")
}

func TestCreativeAudioCatalogRequiresUserAndAdminPermission(t *testing.T) {
	for name, configure := range map[string]func(*CreativePublicService){
		"创作台关闭":       func(s *CreativePublicService) { s.Settings.(*creativeFakeSettingReader).enabled = false },
		"未启用模型":       func(s *CreativePublicService) { s.Settings.(*creativeFakeSettingReader).models = nil },
		"错分组":         func(s *CreativePublicService) { s.Settings.(*creativeFakeSettingReader).models[0].GroupID = 99 },
		"图片白名单不能授权音频": func(s *CreativePublicService) { s.Settings.(*creativeFakeSettingReader).models[0].MediaType = "image" },
		"不支持的操作": func(s *CreativePublicService) {
			s.Settings.(*creativeFakeSettingReader).models[0].Operations = []string{CreativeOperationEdit}
		},
		"专属组无权限": func(s *CreativePublicService) { s.GroupRepo.(*creativeFakeGroupRepo).active[0].IsExclusive = true },
		"公开组被禁用": func(s *CreativePublicService) {
			s.UserRepo.(*creativeFakeUserRepo).user.DisabledPublicGroups = []int64{12}
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc := newCreativeAudioCatalogTestService()
			configure(svc)
			models, err := svc.ListModels(context.Background(), 7)
			require.NoError(t, err)
			require.Empty(t, models.Data)
		})
	}
	svc := newCreativeAudioCatalogTestService()
	svc.GroupRepo.(*creativeFakeGroupRepo).active[0].IsExclusive = true
	svc.UserRepo.(*creativeFakeUserRepo).user.AllowedGroups = []int64{12}
	models, err := svc.ListModels(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, models.Data, 1)
}

func TestCreativeAudioCatalogRequiresSchedulableVoiceAccount(t *testing.T) {
	for name, configure := range map[string]func(*Account){
		"错误平台":    func(a *Account) { a.Platform = PlatformOpenAI },
		"不支持账号类型": func(a *Account) { a.Type = AccountTypeSetupToken },
		"账号禁用":    func(a *Account) { a.Status = StatusDisabled },
		"禁止调度":    func(a *Account) { a.Schedulable = false },
		"仅有图片模型":  func(a *Account) { a.Credentials["model_whitelist"] = []any{"grok-imagine"} },
		"全局限流":    func(a *Account) { until := time.Now().Add(time.Hour); a.RateLimitResetAt = &until },
		"模型限流": func(a *Account) {
			setAccountModelRateLimitSnapshot(a, CreativeGrokTTSSelectionModel, time.Now().Add(time.Hour), "test", time.Now())
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc := newCreativeAudioCatalogTestService()
			configure(&svc.AccountRepo.(*creativeFakeAccountRepo).byGroup[12][0])
			models, err := svc.ListModels(context.Background(), 7)
			require.NoError(t, err)
			require.Empty(t, models.Data)
			candidates, err := svc.ListCreativeModelCandidates(context.Background())
			require.NoError(t, err)
			require.Empty(t, candidates)
		})
	}
	// Voice 沿用文本资格，OAuth 未观测媒体付费资格不影响目录，也不触发探测。
	svc := newCreativeAudioCatalogTestService()
	svc.AccountRepo.(*creativeFakeAccountRepo).byGroup[12][0].Type = AccountTypeOAuth
	svc.AccountRepo.(*creativeFakeAccountRepo).byGroup[12][0].Extra = map[string]any{GrokMediaEligibleExtraKey: false}
	models, err := svc.ListModels(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, models.Data, 1)
}

type creativeAudioCatalogFailingAccounts struct{}

func (creativeAudioCatalogFailingAccounts) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	return nil, errors.New("account lookup unavailable")
}

func TestCreativeAudioCatalogLookupFailureDoesNotInventCandidate(t *testing.T) {
	svc := newCreativeAudioCatalogTestService()
	svc.AccountRepo = creativeAudioCatalogFailingAccounts{}
	_, err := svc.ListModels(context.Background(), 7)
	require.Error(t, err)
	_, err = svc.ListCreativeModelCandidates(context.Background())
	require.Error(t, err)
}

func TestCreativeAudioModelSettingsStrictWhitelist(t *testing.T) {
	setting := CreativeModelSetting{GroupID: 12, Model: CreativeGrokTTSModel, MediaType: "audio", Operations: []string{CreativeOperationGenerate}}
	normalized, err := NormalizeCreativeModelSettings([]CreativeModelSetting{setting})
	require.NoError(t, err)
	require.Equal(t, []CreativeModelSetting{setting}, normalized)
	require.Empty(t, creativeModelSettingsIndex(normalized))
	for name, mutate := range map[string]func(*CreativeModelSetting){
		"未知音频模型":   func(s *CreativeModelSetting) { s.Model = "gpt-4o-mini-tts" },
		"TTS伪装图片":  func(s *CreativeModelSetting) { s.MediaType = "image" },
		"TTS伪装旧图片": func(s *CreativeModelSetting) { s.MediaType = "" },
		"TTS编辑":    func(s *CreativeModelSetting) { s.Operations = []string{CreativeOperationEdit} },
		"混入视频端点":   func(s *CreativeModelSetting) { s.VideoEndpoint = VideoEndpointMiniMax },
		"混入视频能力":   func(s *CreativeModelSetting) { s.VideoCapabilities = &CreativeVideoCapabilities{} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := setting
			mutate(&changed)
			_, err := NormalizeCreativeModelSettings([]CreativeModelSetting{changed})
			require.Error(t, err)
		})
	}
	svc := newCreativeAudioCatalogTestService()
	_, err = svc.NormalizeCreativeModelSettingsForSave(context.Background(), []CreativeModelSetting{setting})
	require.NoError(t, err)
	svc.GroupRepo.(*creativeFakeGroupRepo).byID[12].Platform = PlatformOpenAI
	_, err = svc.NormalizeCreativeModelSettingsForSave(context.Background(), []CreativeModelSetting{setting})
	require.Error(t, err)
	// 未知分组仍保留配置，但目录不会给不存在的分组生成能力。
	setting.GroupID = 999
	_, err = svc.NormalizeCreativeModelSettingsForSave(context.Background(), []CreativeModelSetting{setting})
	require.NoError(t, err)
}

func TestCreativeAudioCapabilitiesCannotBeExpandedByPersistedSettings(t *testing.T) {
	var input []CreativeModelSetting
	require.NoError(t, json.Unmarshal([]byte(`[{"group_id":12,"model":"grok-voice-tts","media_type":"audio","operations":["generate"],"audio_capabilities":{"voices":["alloy"],"formats":["pcm"],"speed_max":99}}]`), &input))
	normalized, err := NormalizeCreativeModelSettings(input)
	require.NoError(t, err)
	svc := newCreativeAudioCatalogTestService()
	svc.Settings.(*creativeFakeSettingReader).models = normalized
	models, err := svc.ListModels(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, models.Data, 1)
	caps := models.Data[0].AudioCapabilities
	require.Equal(t, []string{"eve", "ara", "rex", "sal", "leo"}, caps.Voices)
	require.Equal(t, []string{"mp3", "wav"}, caps.Formats)
	require.Equal(t, 0.7, caps.SpeedMin)
	require.Equal(t, 1.5, caps.SpeedMax)
	require.Equal(t, 15000, caps.MaxInputChars)
	caps.Voices[0] = "changed"
	require.Equal(t, "eve", creativeGrokTTSCapabilities().Voices[0])
	require.True(t, svc.creativeAudioConfigured(context.Background(), 12, CreativeGrokTTSModel))
	require.False(t, svc.creativeAudioConfigured(context.Background(), 99, CreativeGrokTTSModel))
}
