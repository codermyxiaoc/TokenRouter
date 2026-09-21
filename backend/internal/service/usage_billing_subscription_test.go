package service

import (
	"math"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestBuildUsageBillingSubscriptionsActualAllocations(t *testing.T) {
	subA, subB, preferred, planA, planB := int64(10), int64(11), int64(99), int64(100), int64(101)
	log := &UsageLog{
		ActualCost:     1,
		SubscriptionID: &preferred, // 当前 Key 与旧单值字段不得覆盖多订阅的实际分配。
		APIKey:         &APIKey{PreferredSubscriptionID: &preferred},
		BillingAllocations: []domain.BillingAllocation{
			{Type: domain.BillingAllocationTypeSubscription, SubscriptionID: &subA, PlanID: &planA, AmountUSD: 0.1},
			{Type: domain.BillingAllocationTypeSubscription, SubscriptionID: &subB, PlanID: &planB, AmountUSD: 0.2},
			{Type: domain.BillingAllocationTypeBalance, AmountUSD: 0.3},
			{Type: domain.BillingAllocationTypeSubscription, SubscriptionID: &subA, PlanID: &planA, AmountUSD: 0.4},
			{Type: domain.BillingAllocationTypeSubscription, SubscriptionID: &preferred, AmountUSD: 0},
			{Type: domain.BillingAllocationTypeSubscription, SubscriptionID: &preferred, AmountUSD: math.NaN()},
			{Type: domain.BillingAllocationTypeSubscription, SubscriptionID: &preferred, AmountUSD: math.Inf(1)},
		},
	}
	got := BuildUsageBillingSubscriptions(log)
	require.Len(t, got, 2)
	require.Equal(t, subA, got[0].SubscriptionID)
	require.Equal(t, planA, *got[0].PlanID)
	require.InDelta(t, 0.5, got[0].AmountUSD, 1e-10)
	require.Equal(t, subB, got[1].SubscriptionID)
	require.InDelta(t, 0.2, got[1].AmountUSD, 1e-10)
	*got[0].PlanID = 999
	require.Equal(t, int64(100), planA, "展示摘要不能反向改写原始分配")
}

func TestBuildUsageBillingSubscriptionsHistoricalRecords(t *testing.T) {
	subID := int64(7)
	cases := []struct {
		name   string
		log    UsageLog
		amount float64
	}{
		{name: "旧订阅实扣", log: UsageLog{SubscriptionID: &subID, BillingType: BillingTypeSubscription, ActualCost: 1.2}, amount: 1.2},
		{name: "旧混合实扣", log: UsageLog{SubscriptionID: &subID, SubscriptionAmountUSD: 0.4, ActualCost: 1.2}, amount: 0.4},
		{name: "未扣费失败", log: UsageLog{SubscriptionID: &subID, BillingType: BillingTypeSubscription}},
		{name: "结算失败残留分配", log: UsageLog{BillingAllocations: []domain.BillingAllocation{{Type: domain.BillingAllocationTypeSubscription, SubscriptionID: &subID, AmountUSD: 1.2}}}},
		{name: "余额记录残留订阅字段", log: UsageLog{SubscriptionID: &subID, BillingType: BillingTypeBalance, ActualCost: 1.2}},
		{name: "当前Key绑定不作推断", log: UsageLog{ActualCost: 1.2, APIKey: &APIKey{PreferredSubscriptionID: &subID}}},
		{name: "明确余额分配优先", log: UsageLog{SubscriptionID: &subID, BillingType: BillingTypeSubscription, ActualCost: 1.2, BillingAllocations: []domain.BillingAllocation{{Type: domain.BillingAllocationTypeBalance, AmountUSD: 1.2}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildUsageBillingSubscriptions(&tc.log)
			if tc.amount == 0 {
				require.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			require.Equal(t, subID, got[0].SubscriptionID)
			require.InDelta(t, tc.amount, got[0].AmountUSD, 1e-10)
		})
	}
}
