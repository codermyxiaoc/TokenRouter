package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
)

// GeminiCacheBillingHandle 独立标记缓存费用，复用本站持久资金账本，不使用普通请求的估额或 Redis 预扣。
type GeminiCacheBillingHandle struct {
	Reservation       *ImageBillingReservation
	InputCost         *CostBreakdown
	StorageCost       *CostBreakdown
	InputTokens       int
	StorageTokenHours float64
}

// PrepareGeminiCacheBilling 按数据库锁内的最新订阅和余额倍率原子预留，明确免费也保留独立资金合同。
func (s *GatewayService) PrepareGeminiCacheBilling(ctx context.Context, input *RecordUsageInput) (*GeminiCacheBillingHandle, error) {
	if s == nil || input == nil || input.APIKey == nil || input.APIKey.User == nil || input.APIKey.Group == nil || input.APIKey.GroupID == nil || input.Result == nil {
		return nil, ErrImageBillingReservationInvalid
	}
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		return nil, nil
	}
	repo, ok := s.usageBillingRepo.(ImageBillingReservationRepository)
	if !ok {
		return nil, ErrBillingServiceUnavailable
	}
	key, result := input.APIKey, input.Result
	if !strings.HasPrefix(result.RequestID, GeminiCachedContentUsageRequestIDPrefix) {
		return nil, ErrImageBillingReservationInvalid
	}
	tokenInput := *input
	tokenResult := *result
	tokenResult.CacheStorageTokenHours = 0
	tokenInput.Result = &tokenResult
	var inputCost *CostBreakdown
	if result.Usage.InputTokens > 0 {
		inputCost = s.QuoteUsageCostBreakdown(ctx, &tokenInput)
	} else {
		inputCost = &CostBreakdown{}
	}
	storageInput := *input
	storageResult := *result
	storageResult.Usage = ClaudeUsage{}
	storageInput.Result = &storageResult
	var storageCost *CostBreakdown
	if result.CacheStorageTokenHours > 0 {
		storageCost = s.QuoteUsageCostBreakdown(ctx, &storageInput)
	} else {
		storageCost = &CostBreakdown{}
	}
	if inputCost == nil || storageCost == nil {
		return nil, ErrBillingServiceUnavailable
	}
	base := inputCost.TotalCost + storageCost.TotalCost
	if base < 0 || math.IsNaN(base) || math.IsInf(base, 0) {
		return nil, ErrImageBillingReservationInvalid
	}
	balanceRate := s.getUserGroupRateMultiplier(ctx, key.User.ID, *key.GroupID, key.Group.RateMultiplier)
	pricingAt := input.PricingAt
	if pricingAt.IsZero() {
		pricingAt = time.Now()
	}
	peakScale, _ := computePeakAwareMultipliers(key, 1, pricingAt)
	rates := resolveUsageBillingAllocationRates(key, string(BillingModeToken), BillingModeToken, key.Group.RateMultiplier, balanceRate, peakScale)
	actorID := key.User.ID
	if key.ActorUser != nil {
		actorID = key.ActorUser.ID
	}
	hold := BatchImageBalanceHoldCommand{
		RequestID: result.RequestID, BatchID: result.RequestID, APIKeyID: key.ID, UserID: key.User.ID, ActorUserID: actorID,
		GroupID: key.GroupID, TeamID: key.TeamID, APIKeyBillingMode: APIKeyEffectiveBillingMode(key), PreferredSubscriptionID: key.PreferredSubscriptionID,
		BaseAmountUSD: base, RequestPayloadHash: input.RequestPayloadHash,
		SubscriptionRateMultiplier: rates.SubscriptionRateMultiplier, SubscriptionRateMultiplierScale: rates.SubscriptionRateMultiplierScale,
		BalanceRateMultiplier: rates.BalanceRateMultiplier, DisablePlanGroupRateMultiplier: rates.DisablePlanGroupRateMultiplier,
	}
	quote, err := json.Marshal(map[string]any{"kind": "gemini_cache", "input_cost": inputCost, "storage_cost": storageCost, "input_tokens": result.Usage.InputTokens, "storage_token_hours": result.CacheStorageTokenHours})
	if err != nil {
		return nil, err
	}
	reservation, err := repo.ReserveImageBilling(ctx, &ImageBillingReserveCommand{Hold: hold, Quote: quote, ExpiresAt: time.Now().Add(10 * time.Minute)})
	if err != nil {
		if errors.Is(err, ErrBatchImageInsufficientBalance) {
			return nil, ErrInsufficientBalance
		}
		return nil, err
	}
	if reservation == nil || !reservation.Applied || reservation.State != ImageBillingReserved {
		return nil, ErrImageBillingReservationConflict
	}
	if s.billingCacheService != nil {
		_ = s.billingCacheService.InvalidateUserBalance(context.WithoutCancel(ctx), key.User.ID)
	}
	return &GeminiCacheBillingHandle{Reservation: reservation, InputCost: inputCost, StorageCost: storageCost, InputTokens: result.Usage.InputTokens, StorageTokenHours: result.CacheStorageTokenHours}, nil
}

// FrozenCost 根据本次实际确认的时长缩减冻结报价；不重新读取期间可能已修改的价卡。
func (h *GeminiCacheBillingHandle) FrozenCost(inputTokens int, tokenHours float64) *CostBreakdown {
	if h == nil {
		return nil
	}
	inputScale, storageScale := 0.0, 0.0
	// 输入费使用同一份上游确认 Token 数；渠道按次价不能按 Token 比例拆分。
	if h.InputTokens > 0 && inputTokens > 0 {
		inputScale = 1
	}
	if h.StorageTokenHours > 0 {
		storageScale = math.Max(0, math.Min(1, tokenHours/h.StorageTokenHours))
	}
	cost := &CostBreakdown{BillingMode: string(BillingModeToken)}
	if inputScale > 0 {
		*cost = *h.InputCost
	}
	cost.TotalCost += h.StorageCost.TotalCost * storageScale
	cost.ActualCost += h.StorageCost.ActualCost * storageScale
	return cost
}

// FinishGeminiCacheBilling 只对明确失败退款；已经提交但捕获失败的记录进入待核对，绝不重复扣款。
func (s *GatewayService) FinishGeminiCacheBilling(ctx context.Context, handle *GeminiCacheBillingHandle, uncertain bool) error {
	if handle == nil || handle.Reservation == nil {
		return nil
	}
	repo, ok := s.usageBillingRepo.(ImageBillingReservationRepository)
	if !ok {
		return ErrBillingServiceUnavailable
	}
	r := handle.Reservation
	if uncertain {
		return repo.MarkImageBillingReconciliation(ctx, r.ID, r.Hold.UserID, r.Hold.APIKeyID)
	}
	err := repo.ReleaseImageBilling(ctx, r.ID, r.Hold.UserID, r.Hold.APIKeyID)
	if err == nil && s.billingCacheService != nil {
		_ = s.billingCacheService.InvalidateUserBalance(ctx, r.Hold.UserID)
	}
	return err
}

// FrozenStorageCost 账号统计也沿用请求开始时的存储价，防止热更新价卡改变已冻结的成本分量。
func (h *GeminiCacheBillingHandle) FrozenStorageCost(hours float64) float64 {
	if h == nil || h.StorageCost == nil || h.StorageTokenHours <= 0 {
		return 0
	}
	return h.StorageCost.TotalCost * math.Max(0, math.Min(1, hours/h.StorageTokenHours))
}
