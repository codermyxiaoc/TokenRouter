//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 目录测试使用现有只读账号/渠道替身和显式 Token 价格，不连接数据库或供应商。
func newCreativeTextCatalogTestService() (*CreativePublicService, *Group) {
	svc := newCreativeTestService()
	group := &Group{ID: 12, Name: "Text", Platform: PlatformOpenAI, Status: StatusActive,
		AllowedClientProtocols: []GroupClientProtocol{GroupClientProtocolOpenAIChatCompletions},
		ModelPricing: []ChannelModelPricing{{Models: []string{"unit-chat"}, BillingMode: BillingModeToken,
			InputPrice: testPtrFloat64(1e-6), OutputPrice: testPtrFloat64(2e-6)}}}
	svc.GroupRepo = &creativeFakeGroupRepo{byID: map[int64]*Group{12: group}, active: []Group{*group}}
	accounts := []Account{{ID: 91, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		GroupIDs: []int64{12}, Credentials: map[string]any{"model_whitelist": []string{"unit-chat"}}}}
	svc.AccountRepo = &creativeFakeAccountRepo{byGroup: map[int64][]Account{12: accounts}}
	svc.TextCatalog = &GatewayService{accountRepo: &modelsListAccountRepoStub{byGroup: map[int64][]Account{12: accounts}}}
	svc.Pricing = &BillingService{fallbackPrices: map[string]*ModelPricing{}}
	svc.PricingResolver = NewModelPricingResolver(nil, svc.Pricing)
	svc.Settings = &creativeFakeSettingReader{enabled: true, models: []CreativeModelSetting{{GroupID: 12, Model: "unit-chat", MediaType: "text", Operations: []string{CreativeOperationGenerate}}}}
	return svc, group
}

func TestCreativeTextCatalogPlatformProtocolAndDependencies(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformGrok, PlatformAnthropic, PlatformGemini, PlatformAntigravity, PlatformQoder, PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax, PlatformOpenCodeGo, PlatformCline, PlatformCommandCode} {
		require.True(t, creativeTextPlatformSupported(platform), platform)
	}
	for _, platform := range []string{PlatformVideo, PlatformTypeSafe, "", "unknown"} {
		require.False(t, creativeTextPlatformSupported(platform), platform)
	}
	for name, mutate := range map[string]func(*CreativePublicService, *Group){
		"协议关闭": func(_ *CreativePublicService, g *Group) { g.AllowedClientProtocols = nil },
		"仅Responses": func(_ *CreativePublicService, g *Group) {
			g.AllowedClientProtocols = []GroupClientProtocol{GroupClientProtocolOpenAIResponses}
		},
		"分组禁用":        func(_ *CreativePublicService, g *Group) { g.Status = StatusDisabled },
		"视频平台":        func(_ *CreativePublicService, g *Group) { g.Platform = PlatformVideo },
		"SystemOne平台": func(_ *CreativePublicService, g *Group) { g.Platform = PlatformTypeSafe },
		"simple模式":    func(s *CreativePublicService, _ *Group) { s.Config.RunMode = "simple" },
		"缺少目录服务":      func(s *CreativePublicService, _ *Group) { s.TextCatalog = nil },
		"缺少账号仓储":      func(s *CreativePublicService, _ *Group) { s.AccountRepo = nil },
		"缺少价格服务":      func(s *CreativePublicService, _ *Group) { s.PricingResolver = nil },
		"无可调度账号":      func(s *CreativePublicService, _ *Group) { s.AccountRepo.(*creativeFakeAccountRepo).byGroup[12] = nil },
		"账号停止调度": func(s *CreativePublicService, _ *Group) {
			s.AccountRepo.(*creativeFakeAccountRepo).byGroup[12][0].Schedulable = false
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc, group := newCreativeTextCatalogTestService()
			mutate(svc, group)
			models, err := svc.creativeTextCandidatesForGroup(context.Background(), group)
			require.NoError(t, err)
			require.Empty(t, models)
		})
	}
}

func TestCreativeTextCatalogPermissionsAndAdminWhitelist(t *testing.T) {
	svc, _ := newCreativeTextCatalogTestService()
	models, err := svc.ListModels(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, models.Data, 1)
	require.Equal(t, "text", models.Data[0].MediaType)
	require.Equal(t, creativeDefaultTextCapabilities(), models.Data[0].TextCapabilities)
	require.Equal(t, []string{CreativeOperationGenerate}, models.Data[0].Operations)
	require.Nil(t, models.Data[0].AudioCapabilities)
	require.Nil(t, models.Data[0].VideoCapabilities)
	require.Empty(t, models.Data[0].ImageSizes)
	for name, mutate := range map[string]func(*CreativePublicService){
		"管理员未启用":    func(s *CreativePublicService) { s.Settings.(*creativeFakeSettingReader).models = nil },
		"管理员错组":     func(s *CreativePublicService) { s.Settings.(*creativeFakeSettingReader).models[0].GroupID = 99 },
		"管理员错模型":    func(s *CreativePublicService) { s.Settings.(*creativeFakeSettingReader).models[0].Model = "other-chat" },
		"图片授权不能转文字": func(s *CreativePublicService) { s.Settings.(*creativeFakeSettingReader).models[0].MediaType = "image" },
		"音频授权不能转文字": func(s *CreativePublicService) { s.Settings.(*creativeFakeSettingReader).models[0].MediaType = "audio" },
		"edit不授权文字": func(s *CreativePublicService) {
			s.Settings.(*creativeFakeSettingReader).models[0].Operations = []string{CreativeOperationEdit}
		},
		"专属组无权限": func(s *CreativePublicService) { s.GroupRepo.(*creativeFakeGroupRepo).active[0].IsExclusive = true },
		"禁用公开组": func(s *CreativePublicService) {
			s.UserRepo.(*creativeFakeUserRepo).user.DisabledPublicGroups = []int64{12}
		},
		"创作台关闭": func(s *CreativePublicService) { s.Settings.(*creativeFakeSettingReader).enabled = false },
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newCreativeTextCatalogTestService()
			mutate(s)
			got, err := s.ListModels(context.Background(), 7)
			require.NoError(t, err)
			require.Empty(t, got.Data)
		})
	}
	svc.GroupRepo.(*creativeFakeGroupRepo).active[0].IsExclusive = true
	svc.UserRepo.(*creativeFakeUserRepo).user.AllowedGroups = []int64{12}
	models, err = svc.ListModels(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, models.Data, 1)
}

func TestCreativeTextCatalogRequiresEffectiveTokenPrice(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pricing []ChannelModelPricing
		want    bool
	}{
		{name: "显式付费Token", pricing: []ChannelModelPricing{{Models: []string{"unit-chat"}, BillingMode: BillingModeToken, InputPrice: testPtrFloat64(1e-6), OutputPrice: testPtrFloat64(2e-6)}}, want: true},
		{name: "显式免费Token", pricing: []ChannelModelPricing{{Models: []string{"unit-chat"}, BillingMode: BillingModeToken, InputPrice: testPtrFloat64(0), OutputPrice: testPtrFloat64(0)}}, want: true},
		{name: "没有基础Token价格", pricing: nil},
		{name: "空Token价卡不能冒充免费", pricing: []ChannelModelPricing{{Models: []string{"unit-chat"}, BillingMode: BillingModeToken}}},
		{name: "按次不是Token", pricing: []ChannelModelPricing{{Models: []string{"unit-chat"}, BillingMode: BillingModePerRequest, PerRequestPrice: testPtrFloat64(0.2)}}},
		{name: "图片不是Token", pricing: []ChannelModelPricing{{Models: []string{"unit-chat"}, BillingMode: BillingModeImage, PerRequestPrice: testPtrFloat64(0.2)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, group := newCreativeTextCatalogTestService()
			group.ModelPricing = tc.pricing
			got, err := svc.creativeTextCandidatesForGroup(context.Background(), group)
			require.NoError(t, err)
			if tc.want {
				require.Len(t, got, 1)
			} else {
				require.Empty(t, got)
			}
		})
	}
}

func TestCreativeTextCatalogChecksFinalMappedModel(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		channel      bool
		want         bool
	}{
		{name: "账号别名转真实文本", target: "actual-chat", want: true},
		{name: "账号别名转图片", target: "gpt-image-2"},
		{name: "账号别名转视频", target: "grok-imagine-video"},
		{name: "账号别名转音频", target: "grok-voice-latest"},
		{name: "渠道别名转图片", target: "gpt-image-2", channel: true},
		{name: "渠道别名转语音", target: "grok-voice-tts", channel: true},
		{name: "渠道别名转文本", target: "actual-chat", channel: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, group := newCreativeTextCatalogTestService()
			accounts := svc.AccountRepo.(*creativeFakeAccountRepo).byGroup[12]
			accounts[0].Credentials = map[string]any{"model_whitelist": []string{tc.target}}
			if tc.channel {
				svc.TextCatalog.channelService = newRequestableModelsChannelService(group.ID, group.Platform, Channel{ID: 42, Status: StatusActive, BillingModelSource: BillingModelSourceRequested, ModelMapping: map[string]map[string]string{group.Platform: {"unit-chat": tc.target}}})
			} else {
				accounts[0].Credentials["model_mapping"] = map[string]any{"unit-chat": tc.target}
			}
			got, err := svc.creativeTextCandidatesForGroup(context.Background(), group)
			require.NoError(t, err)
			if tc.want {
				require.Len(t, got, 1)
				require.Equal(t, "unit-chat", got[0].Model)
			} else {
				require.Empty(t, got)
			}
		})
	}
}

func TestCreativeTextCatalogExplicitMetadataExcludesMedia(t *testing.T) {
	svc, _ := newCreativeTextCatalogTestService()
	svc.Pricing.pricingService = &PricingService{pricingData: map[string]*LiteLLMModelPricing{"neutral-name": {Mode: "image_generation", SupportedOutputModalities: []string{"image"}}}}
	require.False(t, svc.creativeTextModelAllowed("neutral-name"))
	for _, model := range []string{"MJ-v7", "gpt-image-2", "grok-imagine-video", "grok-voice-tts", "grok-voice-latest", "text-embedding-3-large", "rerank-v3", "systemone", "jev"} {
		require.False(t, svc.creativeTextModelAllowed(model), model)
	}
	require.True(t, svc.creativeTextModelAllowed("ordinary-chat"))
}

func TestCreativeTextCatalogMixedAccountAliasRequiresSafeFinalModels(t *testing.T) {
	for _, mediaSchedulable := range []bool{true, false} {
		name := "混合媒体候选隐藏别名"
		if !mediaSchedulable {
			name = "停调媒体候选不阻挡文本"
		}
		t.Run(name, func(t *testing.T) {
			svc, group := newCreativeTextCatalogTestService()
			accounts := svc.AccountRepo.(*creativeFakeAccountRepo).byGroup[group.ID]
			accounts[0].Credentials = map[string]any{"model_mapping": map[string]any{"unit-chat": "actual-chat"}, "model_whitelist": []string{"actual-chat"}}
			media := accounts[0]
			media.ID = 92
			media.Schedulable = mediaSchedulable
			media.Credentials = map[string]any{"model_mapping": map[string]any{"unit-chat": "gpt-image-2"}, "model_whitelist": []string{"gpt-image-2"}}
			svc.AccountRepo.(*creativeFakeAccountRepo).byGroup[group.ID] = append(accounts, media)
			got, err := svc.creativeTextCandidatesForGroup(context.Background(), group)
			require.NoError(t, err)
			if mediaSchedulable {
				require.Empty(t, got)
			} else {
				require.Len(t, got, 1)
				require.Equal(t, "unit-chat", got[0].Model)
			}
		})
	}
}

func TestCreativeTextSettingsNormalizeAndImageIndexIsolation(t *testing.T) {
	svc, group := newCreativeTextCatalogTestService()
	input := []CreativeModelSetting{{GroupID: 12, Model: " unit-chat ", MediaType: " TEXT ", Operations: []string{"generate", "generate"}}}
	got, err := svc.NormalizeCreativeModelSettingsForSave(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, []CreativeModelSetting{{GroupID: 12, Model: "unit-chat", MediaType: "text", Operations: []string{CreativeOperationGenerate}}}, got)
	require.Empty(t, creativeModelSettingsIndex(got))
	for _, operation := range []string{CreativeOperationEdit, CreativeOperationInpaint, "tts"} {
		changed := got[0]
		changed.Operations = []string{operation}
		_, err := NormalizeCreativeModelSettings([]CreativeModelSetting{changed})
		require.Error(t, err)
	}
	for _, platform := range []string{PlatformVideo, PlatformTypeSafe} {
		group.Platform = platform
		_, err := svc.NormalizeCreativeModelSettingsForSave(context.Background(), got)
		require.Error(t, err)
	}
	changed := got[0]
	changed.VideoEndpoint = VideoEndpointOpenAIVideos
	_, err = NormalizeCreativeModelSettings([]CreativeModelSetting{changed})
	require.Error(t, err)
}
