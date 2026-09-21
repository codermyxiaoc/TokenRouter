package repository

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 恢复成功和部分输出失败都读取实际资金分配，不能用失败阶段的订阅 ID 覆盖最终套餐。
func TestOpsBillingSubscriptionsUsesSettledAllocations(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial_failure", true: "recovered"}[recovered], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			uid, keyID := int64(10), int64(20)
			item := &service.OpsErrorLog{ID: 1, UserID: &uid, APIKeyID: &keyID, ClientRequestID: "internal-id", RecoveredUpstream: recovered}
			columns := []string{"error_id", "usage_id", "user_id", "billing_user_id", "api_key_id", "subscription_id", "billing_type", "actual_cost", "subscription_amount_usd", "balance_amount_usd", "billing_allocations"}
			mock.ExpectQuery(regexp.QuoteMeta(errorBillingSubscriptionsQuery)).WithArgs("{1}").
				WillReturnRows(sqlmock.NewRows(columns).AddRow(1, 101, 10, 10, 20, 999, 1, 0.6, 0.5, 0.1,
					`[{"type":"subscription","subscription_id":31,"plan_id":41,"amount_usd":0.2},{"type":"subscription","subscription_id":32,"plan_id":42,"amount_usd":0.3},{"type":"balance","amount_usd":0.1}]`))
			mock.ExpectQuery("(?s)SELECT source.user_id.*FROM \\(VALUES").WithArgs(int64(10), int64(31), int64(41), int64(10), int64(32), int64(42)).
				WillReturnRows(sqlmock.NewRows([]string{"user_id", "subscription_id", "source_plan_id", "plan_id", "name"}).
					AddRow(10, 31, 41, 41, "月度套餐").AddRow(10, 32, 42, 42, "加量包"))
			require.NoError(t, (&opsRepository{db: db}).hydrateErrorBillingSubscriptions(context.Background(), []*service.OpsErrorLog{item}))
			require.Len(t, item.BillingSubscriptions, 2)
			require.Equal(t, int64(31), item.BillingSubscriptions[0].SubscriptionID)
			require.Equal(t, "月度套餐", item.BillingSubscriptions[0].PlanName)
			require.InDelta(t, 0.2, item.BillingSubscriptions[0].AmountUSD, 1e-10)
			require.Equal(t, int64(32), item.BillingSubscriptions[1].SubscriptionID)
			require.Equal(t, "加量包", item.BillingSubscriptions[1].PlanName)
			require.Equal(t, item.BillingSubscriptions, service.ToUserErrorRequest(item).BillingSubscriptions)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// 缺少归属或内部关联标识的错误不能靠当前 Key 配置猜测扣费套餐。
func TestOpsBillingSubscriptionsSkipsUncorrelatableErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	uid, keyID := int64(10), int64(20)
	errors := []*service.OpsErrorLog{
		nil,
		{ID: 1, UserID: &uid, APIKeyID: &keyID},
		{ID: 2, UserID: &uid, ClientRequestID: "internal-id"},
		{ID: 3, APIKeyID: &keyID, ClientRequestID: "internal-id"},
	}
	require.NoError(t, (&opsRepository{db: db}).hydrateErrorBillingSubscriptions(context.Background(), errors))
	for _, item := range errors {
		if item != nil {
			require.Empty(t, item.BillingSubscriptions)
		}
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

// 损坏的 JSON 不是合法的历史缺省分配，不能回退到可能只是预选的 subscription_id。
func TestOpsBillingSubscriptionsOmitsMalformedAllocations(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	uid, keyID := int64(10), int64(20)
	item := &service.OpsErrorLog{ID: 1, UserID: &uid, APIKeyID: &keyID, ClientRequestID: "internal-id"}
	mock.ExpectQuery(regexp.QuoteMeta(errorBillingSubscriptionsQuery)).WithArgs("{1}").
		WillReturnRows(sqlmock.NewRows([]string{"error_id", "usage_id", "user_id", "billing_user_id", "api_key_id", "subscription_id", "billing_type", "actual_cost", "subscription_amount_usd", "balance_amount_usd", "billing_allocations"}).
			AddRow(1, 101, 10, 10, 20, 999, 1, 0.6, 0.6, 0, `{"malformed":true}`))
	require.NoError(t, (&opsRepository{db: db}).hydrateErrorBillingSubscriptions(context.Background(), []*service.OpsErrorLog{item}))
	require.Empty(t, item.BillingSubscriptions)
	require.NoError(t, mock.ExpectationsWereMet())
}
