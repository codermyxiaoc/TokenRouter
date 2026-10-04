//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/stretchr/testify/require"
)

// 捕获回放只能读取已有结算结果，普通结算入口与缓存扣款都不应再次执行。
type imageCaptureReplayRepo struct {
	UsageBillingRepository
	ImageBillingReservationRepository
	result       *UsageBillingApplyResult
	captureCalls int
	applyCalls   int
}

func (r *imageCaptureReplayRepo) Apply(context.Context, *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	r.applyCalls++
	return r.result, nil
}

func (r *imageCaptureReplayRepo) CaptureImageBilling(context.Context, string, *UsageBillingCommand, float64) (*UsageBillingApplyResult, error) {
	r.captureCalls++
	return r.result, nil
}

func TestApplyUsageBillingImageCaptureReplayRestoresActualAllocation(t *testing.T) {
	subscriptionID := int64(10)
	effectiveRate := 1.4166666666666667
	repo := &imageCaptureReplayRepo{result: &UsageBillingApplyResult{
		Applied: false, SubscriptionAmountUSD: 0.06, BalanceAmountUSD: 0.11,
		EffectiveRateMultiplier: &effectiveRate,
		BillingAllocations: []domain.BillingAllocation{
			{Type: domain.BillingAllocationTypeSubscription, SubscriptionID: &subscriptionID, AmountUSD: 0.06, BaseAmountUSD: 0.01, RateMultiplier: 6},
			{Type: domain.BillingAllocationTypeBalance, AmountUSD: 0.11, BaseAmountUSD: 0.11, RateMultiplier: 1},
		},
	}}
	cache := &balanceEligibilityCacheStub{balance: 1}
	cacheService := NewBillingCacheService(cache, nil, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(cacheService.Stop)
	usageLog := &UsageLog{ActualCost: 0.72, RateMultiplier: 6, BillingType: BillingTypeSubscription}
	applied, err := applyUsageBilling(context.Background(), "image-replay", usageLog, &usageBillingParams{
		User: &User{ID: 1}, APIKey: &APIKey{ID: 2}, Account: &Account{ID: 3},
		Cost:             &CostBreakdown{TotalCost: 0.12, ActualCost: 0.72, BillingMode: string(BillingModeImage)},
		ImageReservation: &ImageBillingReservation{ID: "image-replay"},
	}, &billingDeps{deferredService: &DeferredService{}, billingCacheService: cacheService}, repo)
	require.NoError(t, err)
	require.False(t, applied)
	require.Equal(t, 1, repo.captureCalls)
	require.Zero(t, repo.applyCalls)
	require.InDelta(t, 0.17, usageLog.ActualCost, 1e-12)
	require.Equal(t, effectiveRate, usageLog.RateMultiplier)
	require.Equal(t, 0.06, usageLog.SubscriptionAmountUSD)
	require.Equal(t, 0.11, usageLog.BalanceAmountUSD)
	require.Equal(t, &subscriptionID, usageLog.SubscriptionID)
	require.Equal(t, repo.result.BillingAllocations, usageLog.BillingAllocations)
	cacheService.Stop()
	require.Zero(t, cache.deductCalls.Load())
	require.Zero(t, cache.invalidateCalls.Load())
}
