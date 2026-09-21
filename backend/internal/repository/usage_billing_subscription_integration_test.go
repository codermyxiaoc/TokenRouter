//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// billingSubscriptionFixtureQueryer 使用隔离 CTE 在真实 PostgreSQL 中验证名称选择，不写业务表。
type billingSubscriptionFixtureQueryer struct{ db *sql.DB }

func (q billingSubscriptionFixtureQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	const fixtures = `WITH
user_subscriptions(id, user_id, plan_id, source_order_id, deleted_at) AS (VALUES
 (11::bigint, 10::bigint, 101::bigint, 201::bigint, NULL::timestamptz),
 (12, 10, 102, 202, NULL),
 (13, 10, 103, 203, NULL),
 (14, 10, 105, 204, NULL),
 (15, 10, 105, 205, NULL),
 (17, 10, 101, 201, NOW()),
 (18, 99, 101, 201, NULL),
 (20, 10, 108, NULL, NULL)
),
payment_orders(id, user_id, plan_id, plan_snapshot) AS (VALUES
 (201::bigint, 10::bigint, 101::bigint, '{"name":"购买时名称"}'::jsonb),
 (202, 10, 102, '{"name":"  "}'),
 (203, 99, 103, '{"name":"其它付款人订单"}'),
 (204, 10, 105, '{"name":"另一个套餐快照"}'),
 (205, 10, 106, '{"name":"不匹配的订单套餐"}')
),
subscription_plans(id, name, deleted_at) AS (VALUES
 (101::bigint, '后来修改的名称', NULL::timestamptz),
 (102, '无快照套餐', NULL),
 (103, '付款人隔离套餐', NULL),
 (104, '实际扣费原套餐', NULL),
 (105, '当前套餐名称', NULL),
 (107, '已删除套餐名称', NOW())
)
`
	return q.db.QueryContext(ctx, fixtures+query, args...)
}

func TestUsageBillingSubscriptionNamesSnapshotAndIsolation(t *testing.T) {
	cases := []struct {
		subID, planID, expectedPlanID int64
		name                          string
	}{
		{11, 101, 101, "购买时名称"},
		{12, 102, 102, "无快照套餐"},
		{13, 103, 103, "付款人隔离套餐"},
		{14, 104, 104, "实际扣费原套餐"},
		{15, 105, 105, "当前套餐名称"},
		{16, 0, 0, ""},
		{17, 0, 101, "购买时名称"},
		{18, 0, 0, ""},
		{19, 107, 107, "已删除套餐名称"},
		{20, 0, 108, ""},
	}
	logs := make([]*service.UsageLog, len(cases))
	for i, tc := range cases {
		subID, planID := tc.subID, tc.planID
		allocation := domain.BillingAllocation{Type: domain.BillingAllocationTypeSubscription, SubscriptionID: &subID, AmountUSD: 0.2}
		if planID > 0 {
			allocation.PlanID = &planID
		}
		logs[i] = &service.UsageLog{UserID: 7, BillingUserID: 10, ActualCost: 0.2, BillingAllocations: []domain.BillingAllocation{allocation}}
	}
	require.NoError(t, hydrateUsageBillingSubscriptions(context.Background(), billingSubscriptionFixtureQueryer{db: integrationDB}, logs))
	for i, tc := range cases {
		require.Len(t, logs[i].BillingSubscriptions, 1)
		item := logs[i].BillingSubscriptions[0]
		require.Equal(t, tc.subID, item.SubscriptionID)
		require.Equal(t, tc.name, item.PlanName, "订阅 %d 名称来源不匹配", tc.subID)
		if tc.expectedPlanID == 0 {
			require.Nil(t, item.PlanID)
		} else {
			require.NotNil(t, item.PlanID)
			require.Equal(t, tc.expectedPlanID, *item.PlanID)
		}
	}
}
