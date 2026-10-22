//go:build unit

package service

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 只读目录测试使用账号快照与既有价格服务，不连接数据库或发起上游任务。
func newCreativeGrokVideoCatalogTestService() (*CreativePublicService, *Group) {
	s := newCreativeTestService()
	g := &Group{ID: 12, Name: "Grok", Platform: PlatformGrok, Status: StatusActive, AllowImageGeneration: true, RateMultiplier: 1}
	accounts := []Account{{ID: 91, Platform: PlatformGrok, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		GroupIDs: []int64{12}, Credentials: map[string]any{"model_whitelist": []string{"grok-imagine-video-1.5"}}}}
	s.GroupRepo = &creativeFakeGroupRepo{byID: map[int64]*Group{12: g}, active: []Group{*g}}
	s.AccountRepo = &creativeFakeAccountRepo{byGroup: map[int64][]Account{12: accounts}}
	s.TextCatalog = &GatewayService{}
	s.Pricing = NewBillingService(nil, nil)
	s.PricingResolver = NewModelPricingResolver(nil, s.Pricing)
	s.Settings = &creativeFakeSettingReader{enabled: true, models: []CreativeModelSetting{{GroupID: 12, Model: "grok-imagine-video-1.5", MediaType: "video", VideoEndpoint: CreativeVideoEndpointGrok, Operations: []string{CreativeOperationGenerate}}}}
	return s, g
}

func TestCreativeGrokVideoCatalogEligibility(t *testing.T) {
	for name, mutate := range map[string]func(*CreativePublicService, *Group, *Account){
		"停用分组":        func(_ *CreativePublicService, g *Group, _ *Account) { g.Status = StatusDisabled },
		"媒体权限关闭":      func(_ *CreativePublicService, g *Group, _ *Account) { g.AllowImageGeneration = false },
		"独立视频不能借道":    func(_ *CreativePublicService, g *Group, _ *Account) { g.Platform = PlatformVideo },
		"账号平台不匹配":     func(_ *CreativePublicService, _ *Group, a *Account) { a.Platform = PlatformOpenAI },
		"账号类型不支持":     func(_ *CreativePublicService, _ *Group, a *Account) { a.Type = AccountTypeSetupToken },
		"账号停止调度":      func(_ *CreativePublicService, _ *Group, a *Account) { a.Schedulable = false },
		"账号不属于该组":     func(_ *CreativePublicService, _ *Group, a *Account) { a.GroupIDs = []int64{99} },
		"OAuth未知媒体资格": func(_ *CreativePublicService, _ *Group, a *Account) { a.Type = AccountTypeOAuth },
		"显式禁止媒体": func(_ *CreativePublicService, _ *Group, a *Account) {
			a.Extra = map[string]any{GrokMediaEligibleExtraKey: false}
		},
		"模型限流": func(_ *CreativePublicService, _ *Group, a *Account) {
			a.Extra = map[string]any{modelRateLimitsKey: map[string]any{"grok-imagine-video-1.5": map[string]any{"rate_limit_reset_at": time.Now().Add(time.Hour).Format(time.RFC3339)}}}
		},
		"只有文本模型": func(_ *CreativePublicService, _ *Group, a *Account) {
			a.Credentials = map[string]any{"model_whitelist": []string{"grok-4.5"}}
		},
		"缺少目录依赖": func(s *CreativePublicService, _ *Group, _ *Account) { s.TextCatalog = nil },
		"缺少价格依赖": func(s *CreativePublicService, _ *Group, _ *Account) { s.Pricing = nil },
	} {
		t.Run(name, func(t *testing.T) {
			s, g := newCreativeGrokVideoCatalogTestService()
			mutate(s, g, &s.AccountRepo.(*creativeFakeAccountRepo).byGroup[12][0])
			got, err := s.creativeGrokVideoCandidatesForGroup(context.Background(), g)
			require.NoError(t, err)
			require.Empty(t, got)
		})
	}
	for _, oauth := range []bool{false, true} {
		s, g := newCreativeGrokVideoCatalogTestService()
		if oauth {
			a := &s.AccountRepo.(*creativeFakeAccountRepo).byGroup[12][0]
			a.Type, a.Extra = AccountTypeOAuth, map[string]any{GrokMediaEligibleExtraKey: true}
		}
		got, err := s.creativeGrokVideoCandidatesForGroup(context.Background(), g)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, CreativeVideoEndpointGrok, got[0].VideoEndpoint)
		require.Equal(t, []float64{5}, got[0].VideoCapabilities.Durations)
		require.Equal(t, []string{"720p"}, got[0].VideoCapabilities.Resolutions)
		require.Equal(t, "16:9", got[0].VideoCapabilities.AspectRatios[0])
		require.Equal(t, 1, got[0].VideoCapabilities.MaxReferenceImages)
	}
}

func TestCreativeGrokVideoCatalogMappedAliasesAndRestrictions(t *testing.T) {
	for _, source := range []string{BillingModelSourceRequested, BillingModelSourceChannelMapped, BillingModelSourceUpstream} {
		t.Run(source, func(t *testing.T) {
			s, g := newCreativeGrokVideoCatalogTestService()
			a := &s.AccountRepo.(*creativeFakeAccountRepo).byGroup[12][0]
			a.Credentials["model_mapping"] = map[string]any{"channel-video": "grok-imagine-video-1.5"}
			channel := Channel{ID: 3, Status: StatusActive, BillingModelSource: source,
				ModelMapping: map[string]map[string]string{PlatformGrok: {"public-video": "channel-video"}}}
			s.TextCatalog.channelService = newRequestableModelsChannelService(g.ID, PlatformGrok, channel)
			s.PricingResolver = NewModelPricingResolver(s.TextCatalog.channelService, s.Pricing)
			got, err := s.creativeGrokVideoCandidatesForGroup(context.Background(), g)
			require.NoError(t, err)
			names := []string{}
			for _, item := range got {
				names = append(names, item.Model)
			}
			require.Contains(t, names, "public-video")
			require.Contains(t, names, "channel-video")
			channel.RestrictModels = true
			s.TextCatalog.channelService = newRequestableModelsChannelService(g.ID, PlatformGrok, channel)
			s.PricingResolver = NewModelPricingResolver(s.TextCatalog.channelService, s.Pricing)
			got, err = s.creativeGrokVideoCandidatesForGroup(context.Background(), g)
			require.NoError(t, err)
			require.Empty(t, got)
		})
	}
	// 渠道读取失败不能回退到无渠道限制的候选集合。
	s, g := newCreativeGrokVideoCatalogTestService()
	s.TextCatalog.channelService = &ChannelService{repo: &requestableModelsChannelRepoStub{err: errors.New("channel unavailable")}}
	_, err := s.creativeGrokVideoCandidatesForGroup(context.Background(), g)
	require.Error(t, err)
}

func TestCreativeGrokVideoCatalogRejectsMixedMediaAndAmbiguousPrice(t *testing.T) {
	for _, target := range []string{"grok-4.5", "grok-imagine-video"} {
		s, g := newCreativeGrokVideoCatalogTestService()
		a := s.AccountRepo.(*creativeFakeAccountRepo).byGroup[12][0]
		a.Credentials = map[string]any{"model_mapping": map[string]any{"public-video": "grok-imagine-video-1.5"}, "model_whitelist": []string{"grok-imagine-video-1.5"}}
		other := a
		other.ID = 92
		other.Credentials = map[string]any{"model_mapping": map[string]any{"public-video": target}, "model_whitelist": []string{target}}
		s.AccountRepo.(*creativeFakeAccountRepo).byGroup[12] = []Account{a, other}
		s.TextCatalog.channelService = newRequestableModelsChannelService(g.ID, PlatformGrok, Channel{ID: 3, Status: StatusActive, BillingModelSource: BillingModelSourceUpstream})
		got, err := s.creativeGrokVideoCandidatesForGroup(context.Background(), g)
		require.NoError(t, err)
		for _, item := range got {
			require.NotEqual(t, "public-video", item.Model)
		}
	}
}

func TestCreativeGrokVideoCatalogPricingUsesExistingContract(t *testing.T) {
	for _, price := range []float64{0, 0.28, -1, math.NaN(), math.Inf(1)} {
		s, g := newCreativeGrokVideoCatalogTestService()
		g.VideoPrice720P = &price
		got, err := s.creativeGrokVideoCandidatesForGroup(context.Background(), g)
		require.NoError(t, err)
		if finiteNonNegative(price) {
			require.Len(t, got, 1)
		} else {
			require.Empty(t, got)
		}
	}
}

func TestCreativeGrokVideoCatalogPermissionsAndPublicCapabilities(t *testing.T) {
	s, _ := newCreativeGrokVideoCatalogTestService()
	got, err := s.ListModels(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, got.Data, 1)
	model := got.Data[0]
	require.Equal(t, "video", model.MediaType)
	require.Equal(t, CreativeVideoEndpointGrok, model.VideoEndpoint)
	require.Equal(t, 5.0, model.VideoCapabilities.DefaultDuration)
	require.Equal(t, "720p", model.VideoCapabilities.DefaultResolution)
	require.Len(t, model.VideoCapabilities.Durations, 15)
	require.Equal(t, 1.0, model.VideoCapabilities.Durations[0])
	require.Equal(t, 15.0, model.VideoCapabilities.Durations[14])
	require.Equal(t, []string{"480p", "720p", "1080p"}, model.VideoCapabilities.Resolutions)
	require.False(t, model.VideoCapabilities.AllowCustomResolution)
	require.False(t, model.VideoCapabilities.AllowCustomAspectRatio)
	require.Equal(t, 1, model.MaxReferenceImages)
	candidates, err := s.ListCreativeModelCandidates(context.Background())
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	for name, change := range map[string]func(*CreativePublicService){
		"管理员关闭": func(s *CreativePublicService) { s.Settings.(*creativeFakeSettingReader).models = nil },
		"端点不匹配": func(s *CreativePublicService) {
			s.Settings.(*creativeFakeSettingReader).models[0].VideoEndpoint = VideoEndpointOpenAIVideos
		},
		"仅编辑未生成": func(s *CreativePublicService) {
			s.Settings.(*creativeFakeSettingReader).models[0].Operations = []string{CreativeOperationEdit}
		},
		"专属组无权限": func(s *CreativePublicService) { s.GroupRepo.(*creativeFakeGroupRepo).active[0].IsExclusive = true },
		"用户禁用组": func(s *CreativePublicService) {
			s.UserRepo.(*creativeFakeUserRepo).user.DisabledPublicGroups = []int64{12}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newCreativeGrokVideoCatalogTestService()
			change(s)
			got, err := s.ListModels(context.Background(), 7)
			require.NoError(t, err)
			require.Empty(t, got.Data)
		})
	}
}

func TestCreativeGrokVideoSettingsEndpointPlatformIsolation(t *testing.T) {
	for _, platform := range []string{PlatformGrok, PlatformVideo, PlatformOpenAI} {
		for _, endpoint := range []VideoEndpoint{CreativeVideoEndpointGrok, VideoEndpointOpenAIVideos} {
			s, g := newCreativeGrokVideoCatalogTestService()
			g.Platform = platform
			item := CreativeModelSetting{GroupID: 12, Model: "public-video", MediaType: "video", VideoEndpoint: endpoint, Operations: []string{CreativeOperationGenerate}}
			got, err := s.NormalizeCreativeModelSettingsForSave(context.Background(), []CreativeModelSetting{item})
			allowed := platform == PlatformGrok && endpoint == CreativeVideoEndpointGrok || platform == PlatformVideo && endpoint == VideoEndpointOpenAIVideos
			if !allowed {
				require.Error(t, err)
				continue
			}
			require.NoError(t, err)
			raw, _, err := marshalCreativeModelSettings(got)
			require.NoError(t, err)
			require.Equal(t, got, parseCreativeModelSettings(raw))
		}
	}
	require.False(t, validVideoEndpoint(CreativeVideoEndpointGrok))
	_, err := NormalizeCreativeModelSettings([]CreativeModelSetting{{GroupID: 12, Model: "public-video", MediaType: "video", VideoEndpoint: CreativeVideoEndpointGrok, Operations: []string{CreativeOperationGenerate}, VideoCapabilities: &CreativeVideoCapabilities{MaxReferenceImages: 2}}})
	require.Error(t, err)
}

func TestCreativeGrokVideoSettingsBillingBounds(t *testing.T) {
	for name, caps := range map[string]*CreativeVideoCapabilities{
		"时长超过原计费上限": {Durations: []float64{16}},
		"自定义768p":   {Resolutions: []string{"768p"}},
		"自定义4k":     {Resolutions: []string{"2160p"}},
	} {
		t.Run(name, func(t *testing.T) {
			item := CreativeModelSetting{GroupID: 12, Model: "public-video", MediaType: "video", VideoEndpoint: CreativeVideoEndpointGrok, Operations: []string{CreativeOperationGenerate}, VideoCapabilities: caps}
			_, err := NormalizeCreativeModelSettings([]CreativeModelSetting{item})
			require.Error(t, err)
			// 同一参数仍可用于既有 Video 账号，不能由 Grok 限制改变旧模型能力。
			item.VideoEndpoint = VideoEndpointOpenAIVideos
			_, err = NormalizeCreativeModelSettings([]CreativeModelSetting{item})
			require.NoError(t, err)
			s, g := newCreativeGrokVideoCatalogTestService()
			item.VideoEndpoint = CreativeVideoEndpointGrok
			item.Model = "grok-imagine-video-1.5"
			got, err := s.creativeGrokVideoModelsForGroup(context.Background(), g, []CreativeModelSetting{item})
			require.NoError(t, err)
			require.Empty(t, got)
		})
	}
	// 保存、运行目录和请求均使用相同标准档位，保留管理员默认值及参考图开关。
	input := &CreativeVideoCapabilities{Durations: []float64{15, 1}, Resolutions: []string{"1080p", "720p", "480p"}, AspectRatios: []string{"9:16", "3:2"}, MaxReferenceImages: 0, AllowCustomResolution: true}
	normalized, err := normalizeCreativeGrokVideoCapabilities(input)
	require.NoError(t, err)
	got := creativeGrokVideoCanvasCapabilities(normalized)
	require.Equal(t, 1.0, got.DefaultDuration)
	require.Equal(t, "1080p", got.DefaultResolution)
	require.Equal(t, "9:16", got.DefaultAspectRatio)
	require.Contains(t, got.AspectRatios, "adaptive")
	require.Contains(t, got.AspectRatios, "3:2")
	require.Equal(t, 0, got.MaxReferenceImages)
	require.False(t, got.AllowCustomResolution)
	require.Equal(t, []float64{15, 1}, input.Durations)
	require.Equal(t, []string{"1080p", "720p", "480p"}, input.Resolutions)
}
