package service

import (
	"context"
	"strings"
	"time"
)

const clinePassNoSubscriptionReason = clinePassUnavailableReason + ": no ClinePass subscription"

// Command Code 仅在没有充值积分时受套餐窗口约束；快照必须属于当前查询身份。
func commandCodeThresholdCandidates(account *Account, now time.Time) []*accountSchedulingThresholdCandidate {
	if !account.IsCommandCode() || account.Type != AccountTypeAPIKey {
		return nil
	}
	snapshot := validCNUsageMonitorSnapshot(account)
	if snapshot == nil || snapshot.Provider != PlatformCommandCode || snapshot.Mode != "wallets" || snapshot.Unit != "USD" ||
		snapshot.ObservedAt == nil || snapshot.ObservedAt.IsZero() || snapshot.ObservedAt.After(now) || commandCodePurchasedBalance(snapshot.Balances) > 0 {
		return nil
	}
	var candidates []*accountSchedulingThresholdCandidate
	for _, limit := range snapshot.Limits {
		if (limit.Name != "5h" && limit.Name != "weekly" && limit.Name != "monthly") || limit.Used == nil || limit.Limit == nil ||
			!validNonNegativeNumber(*limit.Used) || !validFiniteNumber(*limit.Limit) || *limit.Limit <= 0 || limit.ResetAt == nil || !limit.ResetAt.After(now) {
			continue
		}
		candidates = append(candidates, &accountSchedulingThresholdCandidate{window: limit.Name, scope: PlatformCommandCode, usedPercent: *limit.Used / *limit.Limit * 100, until: cloneTimePtr(limit.ResetAt)})
	}
	return candidates
}

// 窗口额度只在有效时间内生效；已购钱包可用于 Command Code 窗口外的请求。
func aggregatorExhaustedReset(limits []UpstreamUsageLimit, now time.Time) *time.Time {
	var until *time.Time
	for _, limit := range limits {
		if limit.Used == nil || limit.Limit == nil || *limit.Limit <= 0 || *limit.Used < *limit.Limit || limit.ResetAt == nil || !limit.ResetAt.After(now) {
			continue
		}
		if until == nil || limit.ResetAt.After(*until) {
			until = cloneTimePtr(limit.ResetAt)
		}
	}
	return until
}

func commandCodePurchasedBalance(balances []UpstreamUsageBalanceEntry) float64 {
	for _, wallet := range balances {
		if wallet.Kind == "purchased" && wallet.Currency == "USD" {
			return wallet.Remaining
		}
	}
	return 0
}

// 查询只能解除自己管理的同一身份冷却，组织上限、鉴权错误和手动停调不受余额查询影响。
// @project-doc docs/interfaces/aggregator_upstreams.md#provider_wallets
func (s *CNProviderBalanceCheckService) applyAggregatorWalletDecision(ctx context.Context, account *Account, result *UpstreamUsageQueryResult, threshold float64, identityHash string) {
	if result.Balance == nil || result.Balance.Remaining == nil {
		return
	}
	now := time.Now()
	if account.IsCline() {
		if *result.Balance.Remaining <= 0 || *result.Balance.Remaining < threshold {
			if !account.isRateLimitActiveForKey(clineCreditsRateLimitKey) {
				_ = s.accountRepo.SetModelRateLimit(ctx, account.ID, clineCreditsRateLimitKey, now.Add(2*s.interval), clineCreditsReason)
			}
		} else {
			s.clearAggregatorWalletLimit(ctx, account, clineCreditsRateLimitKey, clineCreditsReason)
		}
		if result.Subscription == nil {
			if !account.isRateLimitActiveForKey(clinePassRateLimitKey) {
				_ = s.accountRepo.SetModelRateLimit(ctx, account.ID, clinePassRateLimitKey, now.Add(2*s.interval), clinePassNoSubscriptionReason)
			}
		} else if until := clineExhaustedReset(result.Limits, now, now.Add(2*s.interval)); until != nil {
			if !account.isRateLimitActiveForKey(clinePassRateLimitKey) {
				_ = s.accountRepo.SetModelRateLimit(ctx, account.ID, clinePassRateLimitKey, *until, clinePassLimitReason)
			}
		} else if result.Subscription != nil && len(result.Limits) > 0 {
			s.clearAggregatorWalletLimit(ctx, account, clinePassRateLimitKey, clinePassLimitReason)
			s.clearAggregatorWalletLimit(ctx, account, clinePassRateLimitKey, clinePassNoSubscriptionReason)
		}
		return
	}
	if !account.IsCommandCode() {
		return
	}
	reason := cnUsageMonitorReason(identityHash)
	if *result.Balance.Remaining <= 0 || *result.Balance.Remaining < threshold {
		if account.IsSchedulable() {
			_ = s.accountRepo.SetTempUnschedulable(ctx, account.ID, now.Add(2*s.interval), reason)
		}
		return
	}
	if account.TempUnschedulableUntil != nil && (account.TempUnschedulableReason == reason || strings.HasPrefix(account.TempUnschedulableReason, reason+": ")) {
		_ = s.accountRepo.ClearTempUnschedulable(ctx, account.ID)
	}
	until := aggregatorExhaustedReset(result.Limits, now)
	if commandCodePurchasedBalance(result.Balances) > 0 {
		until = nil
	}
	if until != nil {
		if account.IsSchedulable() {
			_ = s.accountRepo.SetTempUnschedulable(ctx, account.ID, *until, commandCodeUsageLimitReason)
		}
	} else if account.TempUnschedulableReason == commandCodeUsageLimitReason || strings.HasPrefix(account.TempUnschedulableReason, commandCodeUsageLimitReason+": ") {
		_ = s.accountRepo.ClearTempUnschedulable(ctx, account.ID)
	}
}

// Cline 已耗尽但未返回重置时间时采用有限复查冷却，不能当作仍有额度。
func clineExhaustedReset(limits []UpstreamUsageLimit, now, fallback time.Time) *time.Time {
	copyLimits := append([]UpstreamUsageLimit(nil), limits...)
	for index := range copyLimits {
		limit := &copyLimits[index]
		if limit.ResetAt == nil || !limit.ResetAt.After(now) || limit.ResetAt.Sub(now) > clinePassMaxCooldown {
			limit.ResetAt = &fallback
		}
	}
	return aggregatorExhaustedReset(copyLimits, now)
}

func (s *CNProviderBalanceCheckService) clearAggregatorWalletLimit(ctx context.Context, account *Account, key, expectedReason string) {
	limits, _ := account.Extra[modelRateLimitsKey].(map[string]any)
	entry, _ := limits[key].(map[string]any)
	reason, _ := entry["reason"].(string)
	if reason == expectedReason || strings.HasPrefix(reason, expectedReason+": ") {
		// 只使指定钱包过期，保留其它模型及钱包的冷却记录。
		_ = s.accountRepo.SetModelRateLimit(ctx, account.ID, key, time.Now().Add(-time.Second), expectedReason)
	}
}
