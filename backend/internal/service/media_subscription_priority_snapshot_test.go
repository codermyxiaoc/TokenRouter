//go:build unit

package service

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// 预估阶段可能尚未选到收费套餐，测试替身模拟仓储锁内得到实际订阅预占。
type mediaPriorityCreativeReserveStub struct {
	UsageBillingRepository
	command *BatchImageBalanceHoldCommand
}

func (r *mediaPriorityCreativeReserveStub) ReserveBatchImageBalance(_ context.Context, cmd *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error) {
	r.command = cmd
	return &BatchImageBalanceHoldResult{Applied: true, HoldAmountUSD: 2.4, EstimatedAmountUSD: 2.4, SubscriptionAmountUSD: 2.4}, nil
}

func TestCreativeZeroEstimateStillReservesEligiblePlan(t *testing.T) {
	for _, tc := range []struct {
		name        string
		planEnabled bool
		basePrice   float64
		wantReserve bool
	}{
		{name: "priority_may_charge_subscription", planEnabled: true, basePrice: 1, wantReserve: true},
		{name: "old_independent_free_contract", basePrice: 1},
		{name: "zero_base_price_is_free", planEnabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &mediaPriorityCreativeReserveStub{}
			run := &CreativeRun{RunID: "zero-estimate", UserID: 1, APIKeyID: 2, GroupID: 3, BaseUnitPrice: tc.basePrice,
				RequestedOutputCount: 1, PlanGroupRateEnabled: tc.planEnabled}
			require.NoError(t, reserveCreativeBalanceHold(context.Background(), repo, run))
			if !tc.wantReserve {
				require.Nil(t, repo.command)
				require.Zero(t, run.EstimatedCost)
				return
			}
			require.NotNil(t, repo.command)
			require.False(t, repo.command.DisablePlanGroupRateMultiplier)
			require.Equal(t, 1.0, repo.command.BaseAmountUSD)
			require.Zero(t, repo.command.SubscriptionRateMultiplier)
			require.Zero(t, repo.command.BalanceRateMultiplier)
			require.True(t, run.AllowanceReserved)
			require.Equal(t, 2.4, run.EstimatedCost)
			require.NotNil(t, run.HoldAmount)
			require.Equal(t, 2.4, *run.HoldAmount)
		})
	}
}

// 两种图片任务冻结回退倍率与开关，报价才可使用套餐覆盖，不能把报价倍率当作余额倍率。
func TestImageTaskSubscriptionPrioritySnapshots(t *testing.T) {
	for _, tc := range []struct {
		name        string
		priority    bool
		independent bool
		rate        float64
		planRates   map[int64]float64
		noSub       bool
		wantRate    float64
	}{
		{name: "old_independent", independent: true, rate: 2, planRates: map[int64]float64{7: .8}, wantRate: 2},
		{name: "priority_override", independent: true, priority: true, rate: 2, planRates: map[int64]float64{7: .8}, wantRate: .8},
		{name: "missing_override", independent: true, priority: true, rate: 2, wantRate: 2},
		{name: "other_group_override", independent: true, priority: true, rate: 2, planRates: map[int64]float64{8: .8}, wantRate: 2},
		{name: "zero_override", independent: true, priority: true, rate: 2, planRates: map[int64]float64{7: 0}, wantRate: 2},
		{name: "invalid_override", independent: true, priority: true, rate: 2, planRates: map[int64]float64{7: math.Inf(1)}, wantRate: 2},
		{name: "no_subscription", independent: true, priority: true, rate: 2, noSub: true, wantRate: 2},
		{name: "zero_independent_paid_plan", independent: true, priority: true, rate: 0, planRates: map[int64]float64{7: .8}, wantRate: .8},
		{name: "priority_inert_without_independent", priority: true, planRates: map[int64]float64{7: .8}, wantRate: .8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			groupID := int64(7)
			price, userRate := .4, .25
			group := &Group{ID: groupID, Platform: PlatformGemini, RateMultiplier: 9,
				AllowBatchImageGeneration: true, ImagePrice1K: &price,
				ImageRateIndependent: tc.independent, ImageRateMultiplier: tc.rate, ImageSubscriptionRatePriority: tc.priority,
				BatchImageDiscountMultiplier: .5, BatchImageHoldMultiplier: .6}
			billing := &fakeBatchImageBillingRepo{}
			if !tc.noSub {
				billing.usableSubscription = &UserSubscription{ID: 101, UserID: 11,
					Plan: &SubscriptionPlan{ID: 202, GroupIDs: []int64{groupID}, GroupRateMultipliers: tc.planRates}}
			}
			batch, _, _, _, _ := newTestBatchImagePublicService(true)
			batch.BillingRepo = billing
			batch.GroupRepo = &publicBatchImageGroupRepo{groups: map[int64]*Group{groupID: group}}
			batch.UserGroupRateRepo = &publicBatchImageUserGroupRateRepo{rates: map[int64]*float64{groupID: &userRate}}
			accountRate := 1.25
			snapshot, err := batch.resolvePricingSnapshot(ctx, BatchImageOwner{UserID: 11, APIKeyID: 22, GroupID: &groupID},
				validBatchImageSubmitRequest(), BatchImageProviderGeminiAPI, &Account{RateMultiplier: &accountRate})
			require.NoError(t, err)
			wantSub, wantBalance := tc.rate, tc.rate
			if !tc.independent {
				wantSub, wantBalance = 9, userRate
			}
			require.Equal(t, !tc.independent || tc.priority, snapshot.PlanGroupRateEnabled)
			require.Equal(t, wantSub, snapshot.SubscriptionRateMultiplier)
			require.Equal(t, wantBalance, snapshot.BalanceRateMultiplier)
			require.InDelta(t, price*2*tc.wantRate*accountRate*.5, snapshot.EstimatedCost, 1e-12)
			require.InDelta(t, price*2*tc.wantRate*accountRate*.6, snapshot.HoldAmount, 1e-12)

			creative := &CreativePublicService{BillingRepo: billing, UserGroupRateRepo: batch.UserGroupRateRepo}
			creativeSnapshot, err := creative.resolveCreativePricing(ctx, 11, &validatedCreativeParams{
				group: group, operation: CreativeOperationGenerate, model: "gemini-3.1-flash-image", imageSize: "1K", outputCount: 2})
			require.NoError(t, err)
			require.Equal(t, !tc.independent || tc.priority, creativeSnapshot.PlanGroupRateEnabled)
			require.Equal(t, wantSub, creativeSnapshot.SubscriptionRateMultiplier)
			require.Equal(t, wantBalance, creativeSnapshot.BalanceRateMultiplier)
			require.InDelta(t, price*2*tc.wantRate, creativeSnapshot.EstimatedCost, 1e-12)

			// 显式余额模式不能因为存在可用订阅而把预估切成套餐价。
			balanceSnapshot, err := batch.resolvePricingSnapshot(ctx,
				BatchImageOwner{UserID: 11, APIKeyID: 22, GroupID: &groupID, BillingMode: APIKeyBillingModeBalance},
				validBatchImageSubmitRequest(), BatchImageProviderGeminiAPI, &Account{RateMultiplier: &accountRate})
			require.NoError(t, err)
			require.InDelta(t, price*2*wantBalance*accountRate*.5, balanceSnapshot.EstimatedCost, 1e-12)
		})
	}
}
