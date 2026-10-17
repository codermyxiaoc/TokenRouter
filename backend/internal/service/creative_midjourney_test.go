//go:build unit

package service

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// newCreativeMidjourneyTestService 使用显式 API Key 模型规则构造 MJ 目录与创建夹具。
func newCreativeMidjourneyTestService() *CreativePublicService {
	svc := newCreativeTestService()
	group := newCreativeTestGroup()
	group.Platform = PlatformOpenAI
	group.Name = "Midjourney Images"
	svc.GroupRepo = &creativeFakeGroupRepo{byID: map[int64]*Group{group.ID: group}, active: []Group{*group}}
	svc.AccountRepo = &creativeFakeAccountRepo{byGroup: map[int64][]Account{group.ID: {{
		ID: 57, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"model_mapping": map[string]any{"studio-image": "MJ-v7"}, "model_whitelist": []string{"MJ-v7"}},
	}}}}
	svc.Settings = &creativeFakeSettingReader{enabled: true, models: []CreativeModelSetting{
		{GroupID: group.ID, Model: "studio-image", Operations: []string{CreativeOperationGenerate, CreativeOperationEdit, CreativeOperationInpaint}},
		{GroupID: group.ID, Model: "MJ-v7", Operations: []string{CreativeOperationGenerate, CreativeOperationEdit, CreativeOperationInpaint}},
	}}
	return svc
}

// TestCreativeMidjourneyExplicitCandidates 验证 MJ 仅来自显式 API Key 规则，不污染默认 GPT 目录。
func TestCreativeMidjourneyExplicitCandidates(t *testing.T) {
	tests := []struct {
		name        string
		platform    string
		accountType string
		credentials map[string]any
		want        []string
	}{
		{name: "普通账号不附送MJ", platform: PlatformOpenAI, accountType: AccountTypeAPIKey},
		{name: "显式白名单", platform: PlatformOpenAI, accountType: AccountTypeAPIKey,
			credentials: map[string]any{"model_whitelist": []string{"MJ-v7", "MJ-v8.1", "MJ-v8.2", "gpt-image-2"}}, want: []string{"MJ-v7", "MJ-v8.1", "MJ-v8.2"}},
		{name: "非版本MJ前缀白名单", platform: PlatformOpenAI, accountType: AccountTypeAPIKey,
			credentials: map[string]any{"model_whitelist": []string{"MJ", "mj_custom", "MjFast", "prefix-mj-v7"}}, want: []string{"MJ", "mj_custom", "MjFast"}},
		{name: "自映射旧白名单", platform: PlatformOpenAI, accountType: AccountTypeAPIKey,
			credentials: map[string]any{"model_mapping": map[string]any{"MJ-v7": "MJ-v7"}}, want: []string{"MJ-v7"}},
		{name: "别名映射", platform: PlatformOpenAI, accountType: AccountTypeAPIKey,
			credentials: map[string]any{"model_mapping": map[string]any{"image-alias": "MJ-v8.1"}}, want: []string{"image-alias"}},
		{name: "通配透传限定三版本", platform: PlatformOpenAI, accountType: AccountTypeAPIKey,
			credentials: map[string]any{"model_mapping": map[string]any{"MJ-*": ""}}, want: []string{"MJ-v7", "MJ-v8.1", "MJ-v8.2"}},
		{name: "通配映射最终MJ", platform: PlatformOpenAI, accountType: AccountTypeAPIKey,
			credentials: map[string]any{"model_mapping": map[string]any{"MJ-*": "MJ-v7"}}, want: []string{"MJ-v7", "MJ-v8.1", "MJ-v8.2"}},
		{name: "映射后白名单拒绝", platform: PlatformOpenAI, accountType: AccountTypeAPIKey,
			credentials: map[string]any{"model_mapping": map[string]any{"image-alias": "MJ-v8.1"}, "model_whitelist": []string{"gpt-image-2"}}},
		{name: "MJ别名映射GPT不是MJ", platform: PlatformOpenAI, accountType: AccountTypeAPIKey,
			credentials: map[string]any{"model_mapping": map[string]any{"MJ-v7": "gpt-image-2"}}},
		{name: "OAuth不可调用MJ", platform: PlatformOpenAI, accountType: AccountTypeOAuth,
			credentials: map[string]any{"model_mapping": map[string]any{"image-alias": "MJ-v7"}}},
		{name: "其他平台不借道", platform: PlatformGemini, accountType: AccountTypeAPIKey,
			credentials: map[string]any{"model_whitelist": []string{"MJ-v7"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{Platform: tt.platform, Type: tt.accountType, Credentials: tt.credentials}
			require.ElementsMatch(t, tt.want, creativeMidjourneyModelsForAccount(account))
		})
	}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	require.Equal(t, []string{"gpt-image-1", "gpt-image-2"}, creativeExpandAccountModels(account, defaultCreativeOpenAIModelCandidates(), IsGPTImageGenerationModel))
}

// TestCreativeMidjourneyCatalogCapabilities 检查公开和管理候选均按最终 MJ 模型返回保守能力。
func TestCreativeMidjourneyCatalogCapabilities(t *testing.T) {
	svc := newCreativeMidjourneyTestService()
	models, err := svc.ListModels(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, models.Data, 2)
	for _, model := range models.Data {
		require.Equal(t, []string{CreativeOperationGenerate}, model.Operations)
		require.Equal(t, []string{"1K"}, model.ImageSizes)
		require.Equal(t, []string{"1:1"}, model.AspectRatios)
		require.Empty(t, model.Qualities)
		require.Empty(t, model.BackgroundOptions)
		require.Empty(t, model.ThinkingLevels)
		require.Empty(t, model.OutputFormats)
		require.Nil(t, model.OutputCompression)
		require.Equal(t, 0, model.MaxReferenceImages)
		require.Equal(t, 4, model.MaxOutputCount)
		require.Equal(t, 4, model.OutputCount)
		require.InDelta(t, 0.02, model.Price1K, 1e-12)
	}
	candidates, err := svc.ListCreativeModelCandidates(context.Background())
	require.NoError(t, err)
	require.Len(t, candidates, 2)
	for _, candidate := range candidates {
		require.Equal(t, []string{CreativeOperationGenerate}, candidate.Operations)
	}
	// 多账号同别名映射冲突不能靠枚举顺序放开 GPT 编辑能力。
	accounts := svc.AccountRepo.(*creativeFakeAccountRepo)
	mj := accounts.byGroup[12][0]
	gpt := Account{ID: 58, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"model_mapping": map[string]any{"studio-image": "gpt-image-2"}}}
	for _, order := range [][]Account{{mj, gpt}, {gpt, mj}} {
		accounts.byGroup[12] = order
		resolved, err := svc.creativeModelsForGroup(context.Background(), svc.GroupRepo.(*creativeFakeGroupRepo).byID[12])
		require.NoError(t, err)
		require.Equal(t, "MJ-v7", resolved["studio-image"])
	}
}

// TestCreativeMidjourneyCreateRejectsUnsupportedParams 在建单和预扣之前拒绝不支持的输入。
func TestCreativeMidjourneyCreateRejectsUnsupportedParams(t *testing.T) {
	tests := []struct {
		name string
		edit func(*CreateCreativeRunParamsPublic)
		err  error
	}{
		{name: "编辑", edit: func(p *CreateCreativeRunParamsPublic) { p.Operation = CreativeOperationEdit }, err: ErrCreativeOperationUnsupported},
		{name: "局部重绘", edit: func(p *CreateCreativeRunParamsPublic) { p.Operation = CreativeOperationInpaint }, err: ErrCreativeOperationUnsupported},
		{name: "参考图", edit: func(p *CreateCreativeRunParamsPublic) { p.SourceImages = []CreativeInputImage{{}} }, err: ErrCreativeInvalidParams},
		{name: "空蒙版也拒绝", edit: func(p *CreateCreativeRunParamsPublic) { p.Mask = &CreativeInputImage{} }, err: ErrCreativeInvalidParams},
		{name: "质量", edit: func(p *CreateCreativeRunParamsPublic) { p.Quality = "medium" }, err: ErrCreativeInvalidParams},
		{name: "背景", edit: func(p *CreateCreativeRunParamsPublic) { p.Background = "auto" }, err: ErrCreativeInvalidParams},
		{name: "推理", edit: func(p *CreateCreativeRunParamsPublic) { p.ThinkingLevel = "minimal" }, err: ErrCreativeInvalidParams},
		{name: "2K", edit: func(p *CreateCreativeRunParamsPublic) { p.ImageSize = "2K" }, err: ErrCreativeInvalidParams},
		{name: "4K", edit: func(p *CreateCreativeRunParamsPublic) { p.ImageSize = "4K" }, err: ErrCreativeInvalidParams},
		{name: "比例", edit: func(p *CreateCreativeRunParamsPublic) { p.AspectRatio = "16:9" }, err: ErrCreativeInvalidParams},
		{name: "多张", edit: func(p *CreateCreativeRunParamsPublic) { p.OutputCount = 2 }, err: ErrCreativeInvalidParams},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newCreativeMidjourneyTestService()
			params := validCreateParams()
			params.Model = "studio-image"
			tt.edit(&params)
			_, err := svc.CreateRun(context.Background(), testCreativeScope(7), params, "reject-unsupported")
			require.ErrorIs(t, err, tt.err)
			require.Empty(t, svc.Repo.(*creativeFakeRunRepo).runs)
			require.Zero(t, svc.BillingRepo.(*creativeFakeBillingRepo).reserveN)
		})
	}
	svc := newCreativeMidjourneyTestService()
	params := validCreateParams()
	params.Model = "studio-image"
	validated, err := svc.validateCreateParams(context.Background(), 7, &params)
	require.NoError(t, err)
	require.Equal(t, "MJ-v7", validated.finalModel)
	require.Equal(t, "1:1", validated.aspectRatio)
	require.Empty(t, validated.quality)
	require.Empty(t, validated.background)
	pricing, err := svc.resolveCreativePricing(context.Background(), 7, validated)
	require.NoError(t, err)
	require.InDelta(t, 0.02, pricing.BaseUnitPrice, 1e-12)
	require.InDelta(t, 0.08, pricing.EstimatedCost, 1e-12)
}

// TestCreativeMidjourneyPricingRequiresExplicitPrice 覆盖未知价、显式零价、分层价和非有限值。
func TestCreativeMidjourneyPricingRequiresExplicitPrice(t *testing.T) {
	tests := []struct {
		name       string
		groupPrice *float64
		configured *ChannelModelPricing
		available  bool
		price      float64
	}{
		{name: "未配置价格"},
		{name: "图片零价", groupPrice: testPtrFloat64(0), available: true},
		{name: "图片单价", groupPrice: testPtrFloat64(0.34), available: true, price: 0.34},
		{name: "负数价格", groupPrice: testPtrFloat64(-1)},
		{name: "NaN价格", groupPrice: testPtrFloat64(math.NaN())},
		{name: "无穷价格", groupPrice: testPtrFloat64(math.Inf(1))},
		{name: "逐模型图片价", configured: &ChannelModelPricing{BillingMode: BillingModeImage, PerRequestPrice: testPtrFloat64(0.27)}, available: true, price: 0.27},
		{name: "逐模型按次零价", configured: &ChannelModelPricing{BillingMode: BillingModePerRequest, PerRequestPrice: testPtrFloat64(0)}, available: true},
		{name: "逐模型分层价", configured: &ChannelModelPricing{BillingMode: BillingModeImage, Intervals: []PricingInterval{{TierLabel: "1K", PerRequestPrice: testPtrFloat64(0.12)}}}, available: true, price: 0.12},
		{name: "仅Token价不能生图", configured: &ChannelModelPricing{BillingMode: BillingModeToken, InputPrice: testPtrFloat64(0.1)}},
		{name: "声明图片无金额", configured: &ChannelModelPricing{BillingMode: BillingModeImage}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newCreativeMidjourneyTestService()
			groups := svc.GroupRepo.(*creativeFakeGroupRepo)
			group := groups.byID[12]
			group.ImagePrice1K, group.ImagePrice2K, group.ImagePrice4K = tt.groupPrice, nil, nil
			if tt.configured != nil {
				configured := *tt.configured
				configured.Models = []string{"MJ-v7"}
				group.ModelPricing = []ChannelModelPricing{configured}
			}
			groups.active = []Group{*group}
			svc.PricingResolver = NewModelPricingResolver(nil, newTestBillingServiceForResolver())
			models, err := svc.ListModels(context.Background(), 7)
			require.NoError(t, err)
			candidates, err := svc.ListCreativeModelCandidates(context.Background())
			require.NoError(t, err)
			params := validCreateParams()
			params.Model = "studio-image"
			validated, err := svc.validateCreateParams(context.Background(), 7, &params)
			if !tt.available {
				require.Empty(t, models.Data)
				require.Empty(t, candidates)
				require.ErrorIs(t, err, ErrCreativeInvalidModel)
				_, err = svc.resolveCreativePricing(context.Background(), 7, &validatedCreativeParams{group: group, finalModel: "MJ-v7", imageSize: "1K", outputCount: 1})
				require.ErrorIs(t, err, ErrCreativeInvalidModel)
				return
			}
			require.NoError(t, err)
			require.Len(t, models.Data, 2)
			require.Len(t, candidates, 2)
			pricing, err := svc.resolveCreativePricing(context.Background(), 7, validated)
			require.NoError(t, err)
			require.InDelta(t, tt.price, pricing.BaseUnitPrice, 1e-12)
			require.InDelta(t, tt.price*4, pricing.EstimatedCost, 1e-12)
			require.InDelta(t, tt.price, models.Data[0].Price1K, 1e-12)
		})
	}
}

// TestNormalizeCreativeMidjourneySettingsByFinalModel 验证设置保存对别名也只保留 generate。
func TestNormalizeCreativeMidjourneySettingsByFinalModel(t *testing.T) {
	svc := newCreativeMidjourneyTestService()
	settings, err := svc.NormalizeCreativeModelSettingsForSave(context.Background(), []CreativeModelSetting{
		{GroupID: 12, Model: "studio-image", Operations: []string{CreativeOperationGenerate, CreativeOperationEdit, CreativeOperationInpaint}},
		{GroupID: 12, Model: "MJ-v8.2", Operations: []string{CreativeOperationEdit}},
		{GroupID: 12, Model: "gpt-image-2", Operations: []string{CreativeOperationGenerate, CreativeOperationEdit, CreativeOperationInpaint}},
	})
	require.NoError(t, err)
	require.Equal(t, []CreativeModelSetting{
		{GroupID: 12, Model: "studio-image", Operations: []string{CreativeOperationGenerate}},
		{GroupID: 12, Model: "gpt-image-2", Operations: []string{CreativeOperationGenerate, CreativeOperationEdit, CreativeOperationInpaint}},
	}, settings)
}

// TestCreativeMidjourneyFourImageHoldAndReplay 确认旧客户端的单张默认值不会少预占，并保持幂等。
func TestCreativeMidjourneyFourImageHoldAndReplay(t *testing.T) {
	svc := newCreativeMidjourneyTestService()
	params := validCreateParams()
	params.Model = "studio-image"
	ctx := context.Background()
	var firstID string
	for _, count := range []int{0, 1, 4} {
		params.OutputCount = count
		run, err := svc.CreateRun(ctx, testCreativeScope(7), params, "mj-fixed-four")
		require.NoError(t, err)
		require.Equal(t, 4, run.RequestedOutputCount)
		require.InDelta(t, 0.08, run.EstimatedCost, 1e-12)
		require.InDelta(t, 0.08, run.HoldAmount, 1e-12)
		if firstID == "" {
			firstID = run.ID
			require.False(t, run.IdempotentReplay)
		} else {
			require.Equal(t, firstID, run.ID)
			require.True(t, run.IdempotentReplay)
		}
	}
	require.Equal(t, 1, svc.BillingRepo.(*creativeFakeBillingRepo).reserveN)
	require.Len(t, svc.Queue.(*creativeFakeQueue).enqueued, 1)
	created := svc.Repo.(*creativeFakeRunRepo).createParams
	require.Len(t, created, 1)
	require.Equal(t, 4, created[0].RequestedOutputCount)
	require.InDelta(t, 0.02, created[0].BaseUnitPrice, 1e-12)
	for _, count := range []int{-1, 2, 3, 5, 1000000} {
		params.OutputCount = count
		_, err := svc.CreateRun(ctx, testCreativeScope(7), params, "invalid-count")
		require.ErrorIs(t, err, ErrCreativeInvalidParams)
	}
	require.Equal(t, 1, svc.BillingRepo.(*creativeFakeBillingRepo).reserveN)
}
