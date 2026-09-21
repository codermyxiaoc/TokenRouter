package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestHydrateUsageBillingSubscriptionsBatchAndDeletedFallback(t *testing.T) {
	db, mock := newSQLMock(t)
	subA, subB, deleted, planA := int64(11), int64(12), int64(13), int64(101)
	logs := []*service.UsageLog{
		{UserID: 7, BillingUserID: 99, ActualCost: 0.6, BillingAllocations: []domain.BillingAllocation{
			{Type: domain.BillingAllocationTypeSubscription, SubscriptionID: &subA, PlanID: &planA, AmountUSD: 0.4},
			{Type: domain.BillingAllocationTypeSubscription, SubscriptionID: &subB, AmountUSD: 0.2},
		}},
		{UserID: 7, BillingUserID: 99, ActualCost: 0.1, BillingAllocations: []domain.BillingAllocation{
			{Type: domain.BillingAllocationTypeSubscription, SubscriptionID: &subA, PlanID: &planA, AmountUSD: 0.1},
		}},
		{UserID: 8, SubscriptionID: &deleted, BillingType: service.BillingTypeSubscription, ActualCost: 0.3},
	}
	// 同一付款人/订阅/套餐只批量查询一次；团队使用付款 owner，不能使用行为成员身份关联订单。
	mock.ExpectQuery(`SELECT source.user_id, source.subscription_id, source.plan_id,`).
		WithArgs(int64(99), subA, planA, int64(99), subB, int64(0), int64(8), deleted, int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "subscription_id", "source_plan_id", "plan_id", "plan_name"}).
			AddRow(99, subA, planA, planA, "购买时套餐名称").
			AddRow(99, subB, 0, 102, "后台分配套餐").
			AddRow(8, deleted, 0, nil, ""))
	require.NoError(t, hydrateUsageBillingSubscriptions(context.Background(), db, logs))
	require.Equal(t, "购买时套餐名称", logs[0].BillingSubscriptions[0].PlanName)
	require.Equal(t, "后台分配套餐", logs[0].BillingSubscriptions[1].PlanName)
	require.Equal(t, int64(102), *logs[0].BillingSubscriptions[1].PlanID)
	require.Equal(t, "购买时套餐名称", logs[1].BillingSubscriptions[0].PlanName)
	require.Len(t, logs[2].BillingSubscriptions, 1)
	require.Equal(t, deleted, logs[2].BillingSubscriptions[0].SubscriptionID)
	require.Empty(t, logs[2].BillingSubscriptions[0].PlanName)
	require.Nil(t, logs[2].BillingSubscriptions[0].PlanID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestHydrateUsageBillingSubscriptionsUnchargedSkipsQuery(t *testing.T) {
	db, mock := newSQLMock(t)
	subID := int64(1)
	logs := []*service.UsageLog{
		{SubscriptionID: &subID, BillingType: service.BillingTypeSubscription},
		{ActualCost: 1, BalanceAmountUSD: 1},
		nil,
	}
	require.NoError(t, hydrateUsageBillingSubscriptions(context.Background(), db, logs))
	require.Empty(t, logs[0].BillingSubscriptions)
	require.Empty(t, logs[1].BillingSubscriptions)
	require.NoError(t, mock.ExpectationsWereMet())
}
