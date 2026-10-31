//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/stretchr/testify/require"
)

const creativeTokenTestModel = "gpt-image-2.5-sunburst"

// newCreativeTokenTestGroup 构造一个把图片模型定为 token 价的分组，倍率 4.5 与线上问题日志一致。
func newCreativeTokenTestGroup() *Group {
	group := newCreativeTestGroup()
	group.Platform = PlatformOpenAI
	group.RateMultiplier = 4.5
	group.ModelPricing = []ChannelModelPricing{{
		Models: []string{creativeTokenTestModel}, BillingMode: BillingModeToken,
		InputPrice: testPtrFloat64(5e-6), OutputPrice: testPtrFloat64(30e-6),
		ImageInputPrice: testPtrFloat64(5e-6), ImageOutputPrice: testPtrFloat64(30e-6),
	}}
	return group
}

func newCreativeTokenTestService(group *Group) (*CreativePublicService, *BillingService) {
	billing := newTestBillingServiceForResolver()
	svc := newCreativeTestService()
	svc.GroupRepo = &creativeFakeGroupRepo{byID: map[int64]*Group{group.ID: group}, active: []Group{*group}}
	svc.Pricing = billing
	svc.PricingResolver = NewModelPricingResolver(nil, billing)
	return svc, billing
}

// 同一分组同一模型同一份用量：创作台算出的 token 费用必须与 API 的 token 计费完全相等。
func TestCreativeTokenBillingMatchesAPI(t *testing.T) {
	group := newCreativeTokenTestGroup()
	svc, billing := newCreativeTokenTestService(group)
	gateway := &OpenAIGatewayService{resolver: svc.PricingResolver, billingService: billing}
	apiKey := &APIKey{Group: group}
	ctx := context.Background()

	// 两条路径对计费方式的判定一致。
	resolved := gateway.resolveOpenAIChannelPricing(ctx, creativeTokenTestModel, apiKey)
	require.NotNil(t, resolved)
	require.Equal(t, BillingModeToken, resolved.Mode)
	require.NotNil(t, svc.creativeTokenPricing(ctx, group, creativeTokenTestModel))

	// 线上日志里的三组真实用量（文本输入 + 图片输入 / 输出）。
	now := time.Now()
	for _, usage := range []OpenAIUsage{
		{InputTokens: 2437 + 1024, ImageInputTokens: 1024, OutputTokens: 110, ImageOutputTokens: 110},
		{InputTokens: 2266 + 1659, ImageInputTokens: 1659, OutputTokens: 1640, ImageOutputTokens: 1640},
		{InputTokens: 2116 + 1407, ImageInputTokens: 1407, OutputTokens: 4196, ImageOutputTokens: 4196},
	} {
		tokens := creativeTokensFromOpenAIUsage(usage)
		apiCost, err := gateway.calculateOpenAIRecordUsageTokenCostAt(ctx, apiKey, creativeTokenTestModel, 1, now, tokens, "", "")
		require.NoError(t, err)
		run := &CreativeRun{RunID: "run-parity", GroupID: group.ID, Model: creativeTokenTestModel, RequestedOutputCount: 1,
			BaseUnitPrice: 100, BillingMode: domain.CreativeBillingModeToken}
		got := svc.computeCreativeProviderUsage(ctx, run, &tokens, now)
		require.True(t, got.Reported)
		require.Empty(t, got.ReviewReason)
		require.InDelta(t, apiCost.TotalCost, got.TokenBaseAmountUSD, 1e-12, "创作台与 API 的 token 费用必须一致")
		require.InDelta(t, apiCost.TotalCost, got.ChargeBaseAmountUSD, 1e-12)
		require.InDelta(t, apiCost.TotalCost, creativeCaptureBase(run2(run, got), 1), 1e-12)
	}
}

func run2(run *CreativeRun, usage *domain.CreativeProviderUsage) *CreativeRun {
	copy := *run
	copy.ProviderUsage = usage
	return &copy
}

// OpenAI 用量拆桶与 API RecordUsage 一致：input_tokens 含缓存读写，图片输入扣除明确报告的图片缓存。
func TestCreativeTokensFromOpenAIUsageMatchesAPI(t *testing.T) {
	tokens := creativeTokensFromOpenAIUsage(OpenAIUsage{InputTokens: 1000, CacheReadInputTokens: 300, CacheCreationInputTokens: 100,
		ImageInputTokens: 400, ImageCacheReadTokens: 150, OutputTokens: 900, ImageOutputTokens: 800})
	require.Equal(t, UsageTokens{InputTokens: 600, ImageInputTokens: 250, ImageCacheReadTokens: 150, OutputTokens: 900,
		CacheCreationTokens: 100, CacheReadTokens: 300, ImageOutputTokens: 800}, tokens)
	gemini := extractGeminiUsage([]byte(`{"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":1290,"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1290}]}}`))
	require.Equal(t, UsageTokens{InputTokens: 120, OutputTokens: 1290, ImageOutputTokens: 1290}, creativeTokensFromGeminiUsage(gemini))
}

// 报价：token 价模型按估算上限预扣，倍率与 API token 路径一致（分组倍率，不用图片独立倍率）；按张价模型不变。
func TestCreativeTokenPricingQuote(t *testing.T) {
	group := newCreativeTokenTestGroup()
	group.ImageRateIndependent, group.ImageRateMultiplier = true, 1
	svc, _ := newCreativeTokenTestService(group)
	ctx := context.Background()
	validated := &validatedCreativeParams{group: group, finalModel: creativeTokenTestModel, operation: CreativeOperationGenerate,
		prompt: "一只猫", imageSize: "2K", outputCount: 1}
	quote, err := svc.resolveCreativePricing(ctx, 7, validated)
	require.NoError(t, err)
	require.Equal(t, domain.CreativeBillingModeToken, quote.BillingMode)
	require.Greater(t, quote.BaseUnitPrice, 0.0)
	require.InDelta(t, 4.5, quote.BalanceRateMultiplier, 1e-12, "token 模式使用分组倍率，不用图片独立倍率")
	require.InDelta(t, quote.BaseUnitPrice*4.5, quote.EstimatedCost, 1e-9)
	// 预扣上限覆盖线上最大的一次真实用量，正常情况下不会超出。
	worst, err := svc.creativeTokenCost(ctx, group, creativeTokenTestModel, svc.creativeTokenPricing(ctx, group, creativeTokenTestModel),
		creativeTokensFromOpenAIUsage(OpenAIUsage{InputTokens: 3523, ImageInputTokens: 1407, OutputTokens: 4196, ImageOutputTokens: 4196}), time.Now())
	require.NoError(t, err)
	require.Greater(t, quote.BaseUnitPrice, worst.TotalCost)
	// 参考图越多预扣越高。
	withRefs := *validated
	withRefs.sources = []CreativeInputImage{{}, {}}
	refQuote, err := svc.resolveCreativePricing(ctx, 7, &withRefs)
	require.NoError(t, err)
	require.Greater(t, refQuote.BaseUnitPrice, quote.BaseUnitPrice)

	// 分组改为按张价：仍按张，不受影响。
	perImage := newCreativeTestGroup()
	perImage.Platform = PlatformOpenAI
	perImage.ModelPricing = []ChannelModelPricing{{Models: []string{creativeTokenTestModel}, BillingMode: BillingModeImage, PerRequestPrice: testPtrFloat64(0.134)}}
	svc2, _ := newCreativeTokenTestService(perImage)
	quote2, err := svc2.resolveCreativePricing(ctx, 7, &validatedCreativeParams{group: perImage, finalModel: creativeTokenTestModel, operation: CreativeOperationGenerate, prompt: "x", imageSize: "1K", outputCount: 1})
	require.NoError(t, err)
	require.Empty(t, quote2.BillingMode)
	require.InDelta(t, 0.134, quote2.BaseUnitPrice, 1e-12)
}

// 结算兜底：缺少用量按预扣上限扣并标记待核对；超出上限只扣到上限并标记待核对；不当作免费。
func TestCreativeTokenProviderUsageFallbacks(t *testing.T) {
	group := newCreativeTokenTestGroup()
	svc, _ := newCreativeTokenTestService(group)
	ctx := context.Background()
	run := &CreativeRun{RunID: "run-fallback", GroupID: group.ID, Model: creativeTokenTestModel, RequestedOutputCount: 1,
		BaseUnitPrice: 0.05, BillingMode: domain.CreativeBillingModeToken}

	missing := svc.computeCreativeProviderUsage(ctx, run, nil, time.Now())
	require.False(t, missing.Reported)
	require.Equal(t, domain.CreativeBillingReviewTokenUsageMissing, missing.ReviewReason)
	require.InDelta(t, 0.05, missing.ChargeBaseAmountUSD, 1e-12)

	huge := UsageTokens{InputTokens: 10_000_000, OutputTokens: 10_000_000, ImageOutputTokens: 10_000_000}
	exceeded := svc.computeCreativeProviderUsage(ctx, run, &huge, time.Now())
	require.True(t, exceeded.Reported)
	require.Equal(t, domain.CreativeBillingReviewTokenCostExceedsHold, exceeded.ReviewReason)
	require.Greater(t, exceeded.TokenBaseAmountUSD, 0.05)
	require.InDelta(t, 0.05, exceeded.ChargeBaseAmountUSD, 1e-12)

	// 没有持久化用量记录时结算按预扣上限；未交付不扣费；按张模式保持单价 × 成功张数。
	require.InDelta(t, 0.05, creativeCaptureBase(run, 1), 1e-12)
	require.Zero(t, creativeCaptureBase(run, 0))
	perImage := &CreativeRun{BaseUnitPrice: 0.134, RequestedOutputCount: 4}
	require.InDelta(t, 0.134*3, creativeCaptureBase(perImage, 3), 1e-12)
}

// 用量收集器：全零用量视为未返回；后一次成功记录覆盖前一次。
func TestCreativeUsageCollector(t *testing.T) {
	ctx, collector := withCreativeUsageCollector(context.Background())
	recordCreativeProviderTokens(ctx, UsageTokens{})
	require.Nil(t, collector.result())
	recordCreativeProviderTokens(ctx, UsageTokens{InputTokens: 1, OutputTokens: 2})
	recordCreativeProviderTokens(ctx, UsageTokens{InputTokens: 3, OutputTokens: 4})
	require.Equal(t, &UsageTokens{InputTokens: 3, OutputTokens: 4}, collector.result())
	recordCreativeProviderTokens(context.Background(), UsageTokens{InputTokens: 9})
}

// 模型目录：token 价模型标记计费模式，展示 token 单价，各尺寸价格为预扣上限。
func TestCreativeTokenCatalogDisplay(t *testing.T) {
	group := newCreativeTokenTestGroup()
	svc, _ := newCreativeTokenTestService(group)
	item := &CreativeModelPublic{Price1K: 0.134}
	svc.applyCreativeTokenDisplay(context.Background(), group, creativeTokenTestModel, item)
	require.Equal(t, domain.CreativeBillingModeToken, item.BillingMode)
	require.NotNil(t, item.TokenPrices)
	require.InDelta(t, 5*4.5, item.TokenPrices.InputPerMillion, 1e-9)
	require.InDelta(t, 30*4.5, item.TokenPrices.ImageOutputPerMillion, 1e-9)
	require.Greater(t, item.Price2K, item.Price1K)
	require.Greater(t, item.Price4K, item.Price2K)

	perImage := newCreativeTestGroup()
	svc2, _ := newCreativeTokenTestService(perImage)
	plain := &CreativeModelPublic{Price1K: 0.134}
	svc2.applyCreativeTokenDisplay(context.Background(), perImage, creativeTokenTestModel, plain)
	require.Empty(t, plain.BillingMode)
	require.InDelta(t, 0.134, plain.Price1K, 1e-12)
}

// SetCreativeRunProviderUsage 与真实仓储一致只写一次，已有记录保留原值。
func (r *creativeFakeRunRepo) SetCreativeRunProviderUsage(_ context.Context, runID string, usage *domain.CreativeProviderUsage) error {
	if run := r.runs[runID]; run != nil && run.ProviderUsage == nil {
		copy := *usage
		run.ProviderUsage = &copy
	}
	return nil
}

// 端到端：执行成功后固定 token 用量与金额（只写一次），结算按 token 费用扣款，用量日志记录 token 与计费模式。
func TestCreativeTokenRunSettlesByTokens(t *testing.T) {
	group := newCreativeTokenTestGroup()
	svc, billingService := newCreativeTokenTestService(group)
	repo := svc.Repo.(*creativeFakeRunRepo)
	run := creativeUsageTestRun("crun_token_settle")
	run.ActualCost, run.CompletedAt = nil, nil
	run.Model, run.GroupID, run.BillingMode = creativeTokenTestModel, group.ID, domain.CreativeBillingModeToken
	run.BaseUnitPrice, run.SubscriptionHoldAllocations, run.BalanceHoldAmount = 1, nil, 1
	run.SubscriptionRateMultiplier, run.BalanceRateMultiplier = 4.5, 4.5
	repo.runs[run.RunID] = run
	repo.outputs[run.RunID] = []*CreativeRunOutput{{RunID: run.RunID, Status: CreativeRunOutputStatusSucceeded}}
	svc.TransientStore.(*creativeFakeTransient).outputs[fmtInputKey(run.RunID, 0)] = []byte("img")
	logs := &creativeUsageLogRepo{}
	svc.UsageLogRepo = logs

	tokens := creativeTokensFromOpenAIUsage(OpenAIUsage{InputTokens: 3461, ImageInputTokens: 1024, OutputTokens: 110, ImageOutputTokens: 110})
	require.NoError(t, svc.recordCreativeTokenUsage(context.Background(), run, &tokens))
	first := *run.ProviderUsage
	// 重复记录（如重试）不改变已算定的金额。
	// 用一份没有用量的过期快照模拟重试：数据库里已有的记录必须保留原值。
	more := UsageTokens{InputTokens: 999999, OutputTokens: 999999}
	stale := *run
	stale.ProviderUsage = nil
	require.NoError(t, svc.recordCreativeTokenUsage(context.Background(), &stale, &more))
	require.Equal(t, first, *repo.runs[run.RunID].ProviderUsage)

	gateway := &OpenAIGatewayService{resolver: svc.PricingResolver, billingService: billingService}
	apiCost, err := gateway.calculateOpenAIRecordUsageTokenCostAt(context.Background(), &APIKey{Group: group}, creativeTokenTestModel, 1, time.Now(), tokens, "", "")
	require.NoError(t, err)

	require.NoError(t, svc.SettleRun(context.Background(), run.RunID))
	require.Equal(t, CreativeRunStatusSucceeded, run.Status)
	billing := svc.BillingRepo.(*creativeFakeBillingRepo)
	require.Equal(t, 1, billing.captureN)
	captured := billing.captured[run.RunID]
	require.NotNil(t, captured)
	require.InDelta(t, apiCost.TotalCost, captured.ActualAmountUSD, 1e-12, "扣费基础金额等于 API 的 token 费用，而不是预扣上限")
	require.Less(t, captured.ActualAmountUSD, run.BaseUnitPrice, "多冻结部分退回")
	var log *UsageLog
	for _, item := range logs.logs {
		log = item
	}
	require.NotNil(t, log)
	require.Equal(t, string(BillingModeToken), *log.BillingMode)
	require.Equal(t, 3461, log.InputTokens)
	require.Equal(t, 110, log.ImageOutputTokens)
	require.InDelta(t, apiCost.TotalCost, log.TotalCost, 1e-12)
}
