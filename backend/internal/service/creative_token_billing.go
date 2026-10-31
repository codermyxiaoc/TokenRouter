package service

import (
	"context"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"go.uber.org/zap"
)

// resolveGroupChannelPricing 返回分组或渠道为模型配置的定价；默认目录价返回 nil。
// API 与创作台用同一个函数判断图片按 token 还是按张结算，避免两条路径计价方式分叉。
// @project-doc docs/domains/creative_studio.md#creative_token_billing
func resolveGroupChannelPricing(ctx context.Context, resolver *ModelPricingResolver, model string, group *Group) *ResolvedPricing {
	if resolver == nil || group == nil || strings.TrimSpace(model) == "" {
		return nil
	}
	gid := group.ID
	resolved := resolver.Resolve(ctx, PricingInput{Model: model, GroupID: &gid, Group: group})
	if resolved == nil || (resolved.Source != PricingSourceGroup && resolved.Source != PricingSourceChannel) {
		return nil
	}
	return resolved
}

// creativeTokenPricing 在分组/渠道把图片模型定为 token 价时返回解析结果；MJ 只接受按张价。
func (s *CreativePublicService) creativeTokenPricing(ctx context.Context, group *Group, model string) *ResolvedPricing {
	if s == nil || IsMidjourneyImageGenerationModel(model) {
		return nil
	}
	resolved := resolveGroupChannelPricing(ctx, s.PricingResolver, model, group)
	if resolved == nil || resolved.Mode != BillingModeToken {
		return nil
	}
	return resolved
}

// creativeTokenHoldImageOutputTokens 是各尺寸单张图片输出 token 的保守上限，用于预扣估算。
// 实际扣费按上游返回的用量，多冻结部分在结算时退回；超出上限的部分转人工核对。
var creativeTokenHoldImageOutputTokens = map[string]int{
	"512": 2000,
	"1K":  8000,
	"2K":  20000,
	"4K":  48000,
}

const (
	// creativeTokenHoldPromptTokensPerRune 以每字符 2 token 保守估算提示词（覆盖中日韩文字）。
	creativeTokenHoldPromptTokensPerRune = 2
	// creativeTokenHoldPromptOverheadTokens 覆盖系统指令、工具声明等固定开销。
	creativeTokenHoldPromptOverheadTokens = 1000
	// creativeTokenHoldReferenceImageTokens 是每张参考图或遮罩的图片输入 token 上限。
	creativeTokenHoldReferenceImageTokens = 6000
	// creativeTokenHoldTextOutputTokens 覆盖图片之外的文本输出（如修订后的提示词）。
	creativeTokenHoldTextOutputTokens = 1000
)

// creativeTokenHoldUsage 按提示词长度、参考图数量与尺寸估算单张图片的 token 用量上限。
func creativeTokenHoldUsage(prompt string, referenceImages int, imageSize string) UsageTokens {
	imageOutput, ok := creativeTokenHoldImageOutputTokens[strings.TrimSpace(imageSize)]
	if !ok {
		imageOutput = creativeTokenHoldImageOutputTokens["4K"]
	}
	// 与上游口径一致：输入总量包含图片输入，图片输入是其中的明细。
	imageInput := max(referenceImages, 0) * creativeTokenHoldReferenceImageTokens
	return UsageTokens{
		InputTokens:       utf8.RuneCountInString(prompt)*creativeTokenHoldPromptTokensPerRune + creativeTokenHoldPromptOverheadTokens + imageInput,
		ImageInputTokens:  imageInput,
		OutputTokens:      imageOutput + creativeTokenHoldTextOutputTokens,
		ImageOutputTokens: imageOutput,
	}
}

// creativeTokenCost 用与 API 相同的统一计费函数计算 token 基础费用（倍率为 1，倍率在扣费时按来源分别应用）。
func (s *CreativePublicService) creativeTokenCost(ctx context.Context, group *Group, model string, resolved *ResolvedPricing, tokens UsageTokens, pricingAt time.Time) (*CostBreakdown, error) {
	pricing := s.Pricing
	if pricing == nil {
		pricing = &BillingService{}
	}
	gid := group.ID
	return pricing.CalculateCostUnified(CostInput{
		Ctx: ctx, Model: model, GroupID: &gid, Group: group, Tokens: tokens, RequestCount: 1,
		RateMultiplier: 1, PricingAt: pricingAt, Resolver: s.PricingResolver, Resolved: resolved,
	})
}

// creativeTokenRateMultipliers 返回 token 结算使用的来源倍率：与 API token 路径一致，
// 在用户/订阅倍率上叠加分组高峰因子，不使用图片独立倍率。
func creativeTokenRateMultipliers(group *Group, subscriptionRate, balanceRate, effective float64, now time.Time) (float64, float64, float64) {
	peak := 1.0
	if group != nil {
		peak = group.PeakMultiplierAt(now)
	}
	return subscriptionRate * peak, balanceRate * peak, effective * peak
}

// creativeUsageCollector 收集一次执行中最后一次成功解析的上游用量；执行器通过上下文记录，
// 不改动执行器签名。每次运行只有一次成功的上游调用，后一次记录覆盖前一次。
type creativeUsageCollector struct {
	mu     sync.Mutex
	tokens *UsageTokens
}

type creativeUsageCollectorKey struct{}

func withCreativeUsageCollector(ctx context.Context) (context.Context, *creativeUsageCollector) {
	collector := &creativeUsageCollector{}
	return context.WithValue(ctx, creativeUsageCollectorKey{}, collector), collector
}

// recordCreativeProviderTokens 记录执行器从上游响应解析出的用量；全零用量视为未返回，不能按免费结算。
func recordCreativeProviderTokens(ctx context.Context, tokens UsageTokens) {
	collector, _ := ctx.Value(creativeUsageCollectorKey{}).(*creativeUsageCollector)
	if collector == nil || creativeUsageTokensEmpty(tokens) {
		return
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	copy := tokens
	collector.tokens = &copy
}

func (c *creativeUsageCollector) result() *UsageTokens {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tokens == nil {
		return nil
	}
	copy := *c.tokens
	return &copy
}

func creativeUsageTokensEmpty(tokens UsageTokens) bool {
	return tokens.InputTokens <= 0 && tokens.ImageInputTokens <= 0 && tokens.ImageCacheReadTokens <= 0 &&
		tokens.OutputTokens <= 0 && tokens.CacheCreationTokens <= 0 && tokens.CacheReadTokens <= 0 && tokens.ImageOutputTokens <= 0
}

// creativeTokensFromOpenAIUsage 与 API RecordUsage 相同地把 OpenAI 用量拆成互斥计费桶：
// input_tokens 含缓存读写，图片输入扣除明确报告的图片缓存。
func creativeTokensFromOpenAIUsage(usage OpenAIUsage) UsageTokens {
	return UsageTokens{
		InputTokens:          max(usage.InputTokens-usage.CacheReadInputTokens-usage.CacheCreationInputTokens, 0),
		ImageInputTokens:     max(usage.ImageInputTokens-usage.ImageCacheReadTokens, 0),
		ImageCacheReadTokens: usage.ImageCacheReadTokens,
		OutputTokens:         usage.OutputTokens,
		CacheCreationTokens:  usage.CacheCreationInputTokens,
		CacheReadTokens:      usage.CacheReadInputTokens,
		ImageOutputTokens:    usage.ImageOutputTokens,
	}
}

// creativeTokensFromGeminiUsage 与 Gemini 网关 calculateTokenCost 相同地映射 usageMetadata。
func creativeTokensFromGeminiUsage(usage *ClaudeUsage) UsageTokens {
	if usage == nil {
		return UsageTokens{}
	}
	return UsageTokens{
		InputTokens:           usage.InputTokens,
		OutputTokens:          usage.OutputTokens,
		CacheCreationTokens:   usage.CacheCreationInputTokens,
		CacheReadTokens:       usage.CacheReadInputTokens,
		CacheCreation5mTokens: usage.CacheCreation5mTokens,
		CacheCreation1hTokens: usage.CacheCreation1hTokens,
		ImageOutputTokens:     usage.ImageOutputTokens,
	}
}

// computeCreativeProviderUsage 在执行成功时一次算定 token 费用并确定扣费基础金额：
// 按 API 同一公式计算；超过预扣上限时只扣到上限并标记待核对；缺少用量或无法计价时按预扣上限扣并标记待核对。
func (s *CreativePublicService) computeCreativeProviderUsage(ctx context.Context, run *CreativeRun, tokens *UsageTokens, pricingAt time.Time) *domain.CreativeProviderUsage {
	holdBase := math.Max(run.BaseUnitPrice, 0) * float64(max(run.RequestedOutputCount, 0))
	usage := &domain.CreativeProviderUsage{ChargeBaseAmountUSD: holdBase, ReviewReason: domain.CreativeBillingReviewTokenUsageMissing}
	if tokens == nil {
		return usage
	}
	usage.Reported = true
	usage.InputTokens, usage.ImageInputTokens, usage.ImageCacheReadTokens = tokens.InputTokens, tokens.ImageInputTokens, tokens.ImageCacheReadTokens
	usage.OutputTokens, usage.CacheCreationTokens, usage.CacheReadTokens = tokens.OutputTokens, tokens.CacheCreationTokens, tokens.CacheReadTokens
	usage.ImageOutputTokens = tokens.ImageOutputTokens
	group, err := s.creativeRunGroup(ctx, run)
	if err != nil {
		logger.L().Warn("creative.token_pricing_unavailable", zap.String("run_id", run.RunID), zap.Error(err))
		return usage
	}
	resolved := s.creativeTokenPricing(ctx, group, run.Model)
	if resolved == nil {
		// 结算前定价被改为非 token：已冻结的上限不变，按上限扣并交人工核对，不改用其它价格。
		logger.L().Warn("creative.token_pricing_changed", zap.String("run_id", run.RunID))
		return usage
	}
	cost, err := s.creativeTokenCost(ctx, group, run.Model, resolved, *tokens, pricingAt)
	if err != nil || cost == nil || math.IsNaN(cost.TotalCost) || math.IsInf(cost.TotalCost, 0) || cost.TotalCost < 0 {
		logger.L().Warn("creative.token_cost_failed", zap.String("run_id", run.RunID), zap.Error(err))
		return usage
	}
	usage.TokenBaseAmountUSD = cost.TotalCost
	usage.InputCost, usage.ImageInputCost, usage.OutputCost = cost.InputCost, cost.ImageInputCost, cost.OutputCost
	usage.ImageOutputCost, usage.CacheCreationCost, usage.CacheReadCost = cost.ImageOutputCost, cost.CacheCreationCost, cost.CacheReadCost
	usage.ChargeBaseAmountUSD, usage.ReviewReason = cost.TotalCost, ""
	if cost.TotalCost-holdBase > 1e-10 {
		usage.ChargeBaseAmountUSD, usage.ReviewReason = holdBase, domain.CreativeBillingReviewTokenCostExceedsHold
		logger.L().Warn("creative.token_cost_exceeds_hold", zap.String("run_id", run.RunID),
			zap.Float64("token_base_amount", cost.TotalCost), zap.Float64("hold_base_amount", holdBase))
	}
	return usage
}

// creativeRunGroup 读取任务所属分组，用于按同一分组定价计算 token 费用。
func (s *CreativePublicService) creativeRunGroup(ctx context.Context, run *CreativeRun) (*Group, error) {
	if s == nil || s.GroupRepo == nil || run == nil {
		return nil, ErrCreativeGroupForbidden
	}
	group, err := s.GroupRepo.GetByIDLite(ctx, run.GroupID)
	if err != nil {
		return nil, err
	}
	if group == nil {
		return nil, ErrCreativeGroupForbidden
	}
	return group, nil
}

// recordCreativeTokenUsage 在执行成功后为 token 模式任务持久化用量与扣费金额（只写一次）。
func (s *CreativePublicService) recordCreativeTokenUsage(ctx context.Context, run *CreativeRun, tokens *UsageTokens) error {
	if s == nil || run == nil || run.BillingMode != domain.CreativeBillingModeToken || run.ProviderUsage != nil {
		return nil
	}
	recorder, ok := s.Repo.(CreativeProviderUsageRecorder)
	if !ok {
		return nil
	}
	usage := s.computeCreativeProviderUsage(ctx, run, tokens, time.Now())
	if err := recorder.SetCreativeRunProviderUsage(ctx, run.RunID, usage); err != nil {
		return err
	}
	run.ProviderUsage = usage
	return nil
}

// creativeCaptureBase 返回本次捕获的基础金额。token 模式读取执行时算定的金额；缺少记录时按预扣上限并标记待核对。
func creativeCaptureBase(run *CreativeRun, deliveredCount int) float64 {
	if run != nil && run.BillingMode == domain.CreativeBillingModeToken {
		if deliveredCount <= 0 {
			return 0
		}
		if run.ProviderUsage != nil {
			return math.Max(run.ProviderUsage.ChargeBaseAmountUSD, 0)
		}
		return math.Max(run.BaseUnitPrice, 0) * float64(max(run.RequestedOutputCount, 0))
	}
	return math.Max(run.BaseUnitPrice, 0) * float64(max(deliveredCount, 0))
}

// applyCreativeTokenDisplay 为 token 价模型填写展示信息：每百万 token 单价与各尺寸的预扣上限（不含参考图，已乘倍率）。
func (s *CreativePublicService) applyCreativeTokenDisplay(ctx context.Context, group *Group, model string, item *CreativeModelPublic) {
	if item == nil {
		return
	}
	resolved := s.creativeTokenPricing(ctx, group, model)
	if resolved == nil {
		return
	}
	rate := 1.0
	if group != nil && group.RateMultiplier >= 0 {
		rate = group.RateMultiplier
	}
	item.BillingMode = domain.CreativeBillingModeToken
	if base := resolved.BasePricing; base != nil {
		item.TokenPrices = &CreativeTokenPrices{
			InputPerMillion:       base.InputPricePerToken * 1e6 * rate,
			OutputPerMillion:      base.OutputPricePerToken * 1e6 * rate,
			ImageInputPerMillion:  base.ImageInputPricePerToken * 1e6 * rate,
			ImageOutputPerMillion: base.ImageOutputPricePerToken * 1e6 * rate,
		}
	}
	hold := func(size string) float64 {
		cost, err := s.creativeTokenCost(ctx, group, model, resolved, creativeTokenHoldUsage("", 0, size), time.Now())
		if err != nil || cost == nil || math.IsNaN(cost.TotalCost) || math.IsInf(cost.TotalCost, 0) || cost.TotalCost < 0 {
			return 0
		}
		return cost.TotalCost * rate
	}
	item.Price512, item.Price1K, item.Price2K, item.Price4K = hold("512"), hold("1K"), hold("2K"), hold("4K")
}
