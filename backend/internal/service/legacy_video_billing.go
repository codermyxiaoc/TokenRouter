package service

import (
	"context"
	"errors"
)

// LegacyVideoBillingQuote 保存首次结算计算出的安全金额与用量，重试不再读取可变价卡。
// Log 由内部计费计算器构造，不包含用户、账号或 Key 关联实体。
type LegacyVideoBillingQuote struct {
	Command                    *UsageBillingCommand
	SubscriptionScopeID        *int64
	Cost                       *CostBreakdown
	Log                        *UsageLog
	Rates                      usageBillingAllocationRates
	AccountRate                float64
	BaseAmount                 *float64
	SimpleMode                 bool
	SimpleModeKeyRateLimitOnly bool
}

// settleLegacyVideoQuote 依赖持久账本去重，使用记录写入成功后才允许任务结束恢复。
func (s *OpenAIGatewayService) settleLegacyVideoQuote(ctx context.Context, input *OpenAIRecordUsageInput) error {
	q := input.LegacyVideoQuote
	if q == nil || q.Cost == nil || q.Log == nil || input.APIKey == nil || input.User == nil || input.Account == nil ||
		q.Log.APIKeyID != input.APIKey.ID || q.Log.BillingUserID != input.User.ID || q.Log.AccountID != input.Account.ID {
		return errors.New("视频结算快照归属不匹配")
	}
	log := *q.Log
	if !q.SimpleMode {
		if q.Command == nil || q.Command.LegacyVideoTaskID <= 0 {
			return errors.New("缺少视频持久扣费命令")
		}
		command := *q.Command
		command.SubscriptionScopeID = q.SubscriptionScopeID
		result, err := s.usageBillingRepo.Apply(ctx, &command)
		if err != nil {
			return err
		}
		if result == nil {
			return errors.New("视频扣费回执为空")
		}
		applyUsageBillingResultToUsageLog(&log, result)
		// 重放必须以原资金回执为准，订阅不足转余额时不能沿用首次预估费用。
		if !q.SimpleModeKeyRateLimitOnly {
			log.ActualCost = result.SubscriptionAmountUSD + result.BalanceAmountUSD
			if log.TotalCost > 0 {
				log.RateMultiplier = log.ActualCost / log.TotalCost
			}
		}
		if result.APIKeyQuotaExhausted && input.APIKey.Key != "" {
			if invalidator, ok := input.APIKeyService.(apiKeyAuthCacheInvalidator); ok {
				invalidator.InvalidateAuthCacheByKey(ctx, input.APIKey.Key)
			}
		}
		// 崩溃可能发生在账本提交后、缓存失效前；重放清除缓存，不重复累加统计。
		if !result.Applied && s.billingCacheService != nil {
			_ = s.billingCacheService.InvalidateUserBalance(ctx, input.User.ID)
			_ = s.billingCacheService.InvalidateAPIKeyRateLimit(ctx, input.APIKey.ID)
		}
		if result.Applied {
			params := &usageBillingParams{
				SimpleModeKeyRateLimitOnly: q.SimpleModeKeyRateLimitOnly,
				Cost:                       q.Cost, User: input.User, APIKey: input.APIKey, Account: input.Account,
				Subscription: input.Subscription, SubscriptionScopeID: input.SubscriptionScopeID,
				RequestPayloadHash: input.RequestPayloadHash, AccountRateMultiplier: q.AccountRate,
				SubscriptionRateMultiplier:      q.Rates.SubscriptionRateMultiplier,
				SubscriptionRateMultiplierScale: q.Rates.SubscriptionRateMultiplierScale,
				BalanceRateMultiplier:           q.Rates.BalanceRateMultiplier,
				DisablePlanGroupRateMultiplier:  q.Rates.DisablePlanGroupRateMultiplier,
				RateMultipliersResolved:         true, APIKeyService: input.APIKeyService,
				Platform: input.QuotaPlatform, BillingBaseAmountUSD: q.BaseAmount,
			}
			finalizeUsageBilling(params, s.billingDeps(), result)
		}
	}
	// Create 的唯一键保证重试仅补写同一条记录，不会增加第二笔使用量。
	_, err := s.usageLogRepo.Create(ctx, &log)
	return err
}
