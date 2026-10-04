package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
)

// OpenAIImageBillingQuote 固定生成前的各尺寸单价，不持久化提示词、图片或凭据。
type OpenAIImageBillingQuote struct {
	BillingModel   string             `json:"billing_model"`
	BillingMode    string             `json:"billing_mode"`
	UnitPrices     map[string]float64 `json:"unit_prices"`
	RequestedCount int                `json:"requested_count"`
	RequestedSize  string             `json:"requested_size"`
}

// OpenAIImageBillingReservation 在同一生成请求的账号重试间共享，最终用实际图片数量结算。
type OpenAIImageBillingReservation struct {
	Reservation *ImageBillingReservation
	Quote       *OpenAIImageBillingQuote
}

// cost 仅使用预占时保存的报价；上游额外返回图片或更高价尺寸不能突破预占预算。
func (q *OpenAIImageBillingQuote) cost(size string, count int, multiplier float64) (*CostBreakdown, error) {
	if q == nil || count <= 0 || count > q.RequestedCount || (q.BillingMode != string(BillingModeImage) && q.BillingMode != string(BillingModePerRequest)) {
		return nil, ErrImageBillingReservationInvalid
	}
	price, ok := q.UnitPrices[NormalizeImageBillingTierOrDefault(size)]
	if !ok || price < 0 || math.IsNaN(price) || math.IsInf(price, 0) || multiplier < 0 || math.IsNaN(multiplier) || math.IsInf(multiplier, 0) {
		return nil, ErrImageBillingReservationInvalid
	}
	base := price * float64(count)
	if math.IsNaN(base) || math.IsInf(base, 0) || math.IsInf(base*multiplier, 0) {
		return nil, ErrImageBillingReservationInvalid
	}
	return &CostBreakdown{TotalCost: base, ActualCost: QuantizeUsageBillingAmount(base * multiplier), BillingMode: q.BillingMode}, nil
}

// settledCost 预填实际资金价格，尤其余额免费但订阅非零时仍须累计 Key 额度。
// 最终金额以仓储事务的原始预占分配为准，此处不能重选订阅或读取改后的倍率。
func (q *OpenAIImageBillingQuote) settledCost(size string, count int, hold *ImageBillingReservation) (*CostBreakdown, error) {
	if hold == nil {
		return nil, ErrImageBillingReservationInvalid
	}
	cost, err := q.cost(size, count, hold.Hold.BalanceRateMultiplier)
	if err != nil {
		return nil, err
	}
	if cost.TotalCost-hold.Hold.BaseAmountUSD > 1e-10 {
		return nil, ErrBatchImageSettlementCostExceedsHold
	}
	remaining, actual := cost.TotalCost, 0.0
	for _, allocation := range hold.Hold.SubscriptionHoldAllocations {
		covered := math.Min(remaining, allocation.BaseAmountUSD)
		actual += math.Min(allocation.AmountUSD, covered*allocation.RateMultiplier)
		remaining = math.Max(0, remaining-covered)
	}
	cost.ActualCost = actual + QuantizeUsageBillingAmount(remaining*hold.Hold.BalanceRateMultiplier)
	return cost, nil
}

// PrepareAsyncImageBilling 在最终选组和账号并发等待之后、调用上游之前原子预占资金。
// @project-doc docs/domains/media_tasks.md#async_image_billing
func (s *OpenAIGatewayService) PrepareAsyncImageBilling(ctx context.Context, key *APIKey, account *Account, _ *UserSubscription, model string, count int, size string, mapping ChannelMappingResult) (*OpenAIImageBillingReservation, error) {
	if async, _ := ctx.Value(asyncImageExecutionContextKey{}).(bool); !async {
		return nil, nil
	}
	if s != nil && s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		return nil, nil
	}
	if s == nil || s.billingService == nil || key == nil || key.User == nil || key.GroupID == nil || key.Group == nil || account == nil || count <= 0 {
		return nil, ErrImageBillingReservationInvalid
	}
	mapped := strings.TrimSpace(mapping.MappedModel)
	if mapped == "" {
		mapped = model
	}
	upstream := resolveOpenAIAccountUpstreamModelForRequest(account, mapped, false)
	result := &OpenAIForwardResult{Model: mapped, UpstreamModel: upstream, ImageCount: 1}
	if account.Platform == PlatformGrok {
		// Grok 媒体在端点别名归一化、账号映射后保留计费 SKU，与真实转发结果一致。
		result.Model = NormalizeGrokMediaModelForEndpoint(GrokMediaEndpointImagesGenerations, mapped, false)
		result.BillingModel = result.Model
		if accountMapped := strings.TrimSpace(account.GetMappedModel(result.Model)); accountMapped != "" {
			result.BillingModel = accountMapped
		}
		result.UpstreamModel = normalizeOpenAIModelForUpstream(account, result.BillingModel)
	}
	billingModel := openAIUsageBillingModel(result, mapping.ToUsageFields(model, result.UpstreamModel))
	if resolved := s.resolveOpenAIChannelPricing(ctx, billingModel, key); resolved != nil && resolved.Mode == BillingModeToken {
		// Token 图片的实际用量未知，继续走原有后结算；不伪造固定价预占。
		return nil, nil
	}
	repo, ok := s.usageBillingRepo.(ImageBillingReservationRepository)
	if !ok {
		return nil, ErrBillingServiceUnavailable
	}
	quote := &OpenAIImageBillingQuote{BillingModel: billingModel, RequestedCount: count, RequestedSize: NormalizeImageBillingTierOrDefault(size), UnitPrices: make(map[string]float64, 3)}
	for _, tier := range []string{"1K", "2K", "4K"} {
		result.ImageSize = tier
		cost := s.calculateOpenAIImageCost(ctx, billingModel, key, result, 1)
		if cost == nil || cost.TotalCost < 0 || math.IsNaN(cost.TotalCost) || math.IsInf(cost.TotalCost, 0) {
			return nil, ErrImageBillingReservationInvalid
		}
		quote.UnitPrices[tier] = cost.TotalCost
		if tier == quote.RequestedSize {
			quote.BillingMode = cost.BillingMode
		}
	}
	quotedCost, err := quote.cost(quote.RequestedSize, count, 1)
	if err != nil {
		return nil, err
	}
	// 预检订阅可能在并发排队时耗尽，实际资金来源由仓储锁内重新选择，不能复用旧快照。
	balanceRate := s.resolveUserGroupRateMultiplier(ctx, key.User.ID, *key.GroupID, key.Group.RateMultiplier)
	rates := resolveUsageBillingAllocationRates(key, quote.BillingMode, BillingModeImage, key.Group.RateMultiplier, balanceRate, 1)
	actorID := key.User.ID
	if key.ActorUser != nil {
		actorID = key.ActorUser.ID
	}
	hold := BatchImageBalanceHoldCommand{
		RequestID: resolveUsageBillingRequestID(ctx, ""), APIKeyID: key.ID, UserID: key.User.ID, ActorUserID: actorID,
		GroupID: key.GroupID, TeamID: key.TeamID, APIKeyBillingMode: APIKeyEffectiveBillingMode(key), PreferredSubscriptionID: key.PreferredSubscriptionID,
		RequestPayloadHash: resolveUsageBillingPayloadFingerprint(ctx, ""), BaseAmountUSD: quotedCost.TotalCost,
		SubscriptionRateMultiplier: rates.SubscriptionRateMultiplier, SubscriptionRateMultiplierScale: rates.SubscriptionRateMultiplierScale,
		BalanceRateMultiplier: rates.BalanceRateMultiplier, DisablePlanGroupRateMultiplier: rates.DisablePlanGroupRateMultiplier,
	}
	hold.BatchID = hold.RequestID
	encoded, err := json.Marshal(quote)
	if err != nil {
		return nil, err
	}
	// 与默认生成预算保持有界期限；进程失联后只标记待核对，不能盲目退款。
	expires := time.Now().Add(35 * time.Minute)
	if deadline, ok := ctx.Deadline(); ok {
		expires = deadline.Add(3 * time.Minute)
	}
	reservation, err := repo.ReserveImageBilling(ctx, &ImageBillingReserveCommand{Hold: hold, Quote: encoded, ExpiresAt: expires})
	if err != nil {
		if errors.Is(err, ErrBatchImageInsufficientBalance) {
			return nil, ErrInsufficientBalance
		}
		if errors.Is(err, ErrPreferredSubscriptionInvalid) || errors.Is(err, ErrPreferredSubscriptionGroup) || errors.Is(err, ErrPreferredSubscriptionInsufficient) || errors.Is(err, ErrImageBillingReservationConflict) {
			return nil, err
		}
		// 不向客户端泄露数据库细节，也不能把基础设施错误误报成余额不足。
		return nil, ErrBillingServiceUnavailable.WithCause(err)
	}
	if reservation == nil || !reservation.Applied || reservation.State != ImageBillingReserved {
		return nil, ErrImageBillingReservationConflict
	}
	s.invalidateAsyncImageBalance(ctx, key.User.ID)
	return &OpenAIImageBillingReservation{Reservation: reservation, Quote: quote}, nil
}

// ReleaseAsyncImageBilling 只用于明确未产生图片的失败；重复释放由账本保证幂等。
func (s *OpenAIGatewayService) ReleaseAsyncImageBilling(ctx context.Context, handle *OpenAIImageBillingReservation) error {
	if s == nil || handle == nil || handle.Reservation == nil {
		return ErrImageBillingReservationInvalid
	}
	repo, ok := s.usageBillingRepo.(ImageBillingReservationRepository)
	if !ok {
		return ErrImageBillingReservationInvalid
	}
	r := handle.Reservation
	if err := repo.ReleaseImageBilling(ctx, r.ID, r.Hold.UserID, r.Hold.APIKeyID); err != nil {
		return err
	}
	s.invalidateAsyncImageBalance(ctx, r.Hold.UserID)
	return nil
}

// MarkAsyncImageBillingReconciliation 保留超时、进程中断或结算失败的资金，不自动再次生成。
func (s *OpenAIGatewayService) MarkAsyncImageBillingReconciliation(ctx context.Context, handle *OpenAIImageBillingReservation) error {
	if s == nil || handle == nil || handle.Reservation == nil {
		return ErrImageBillingReservationInvalid
	}
	repo, ok := s.usageBillingRepo.(ImageBillingReservationRepository)
	if !ok {
		return ErrImageBillingReservationInvalid
	}
	r := handle.Reservation
	return repo.MarkImageBillingReconciliation(ctx, r.ID, r.Hold.UserID, r.Hold.APIKeyID)
}

// 缓存失效不改变已经提交的预占结果；下一次资金预占仍以数据库行锁内的可用金额为准。
func (s *OpenAIGatewayService) invalidateAsyncImageBalance(ctx context.Context, userID int64) {
	if s.billingCacheService != nil {
		cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if err := s.billingCacheService.InvalidateUserBalance(cacheCtx, userID); err != nil {
			logger.LegacyPrintf("service.openai_gateway", "async image balance cache invalidation failed: user=%d err=%v", userID, err)
		}
	}
}
