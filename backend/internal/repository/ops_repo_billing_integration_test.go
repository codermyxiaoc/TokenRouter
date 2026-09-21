//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// 用真实 PostgreSQL 执行关联谓词，验证相同请求标识不能跨付款人或 Key 读取套餐。
func TestOpsBillingSubscriptionsAssociationIsolation(t *testing.T) {
	const fixtures = `WITH
ops_error_logs(id, user_id, api_key_id, client_request_id, request_id) AS (VALUES
  (1::bigint, 10::bigint, 20::bigint, 'request-a', 'local-a'),
  (2, 11, 20, 'request-a', 'local-a'),
  (3, 10, 21, 'request-a', 'local-a'),
  (4, 10, 20, '', ''),
  (5, 10, 20, 'missing', 'client:request-a'),
  (6, 10, 20, '', 'legacy'),
  (7, 10, 20, 'team', ''),
  (8, 12, 20, 'team', ''),
  (9, 10, 20, 'unbilled', ''),
  (10, 10, 20, 'partial', ''),
  (11, 10, 20, '', 'upstream-forged'),
  (12, 10, 20, 'blank-billing-user', ''),
  (13, 10, 20, 'recovered', '')
),
usage_logs(id, user_id, billing_user_id, api_key_id, request_id, subscription_id,
           billing_type, actual_cost, subscription_amount_usd, balance_amount_usd, billing_allocations) AS (VALUES
  (101::bigint, 10::bigint, 10::bigint, 20::bigint, 'client:request-a', 30::bigint, 1, 0.2, 0.2, 0, '[]'::jsonb),
  (102, 10, 10, 20, 'local:legacy', 30, 1, 0.2, 0.2, 0, '[]'),
  (103, 12, 10, 20, 'client:team', 31, 1, 0.2, 0.2, 0, '[]'),
  (104, 10, 10, 20, 'client:unbilled', 30, 1, 0, 0.2, 0, '[]'),
  (105, 10, 10, 20, 'client:partial', 32, 1, 0.1, 0.1, 0, '[]'),
  (106, 10, 10, 20, 'upstream-forged', 30, 1, 0.2, 0.2, 0, '[]'),
  (107, 10, 0, 20, 'client:blank-billing-user', 30, 1, 0.2, 0.2, 0, '[]'),
  (108, 10, 10, 20, 'client:recovered', 33, 1, 0.3, 0.3, 0, '[]')
)
`
	rows, err := integrationDB.QueryContext(context.Background(), fixtures+errorBillingSubscriptionsQuery,
		pq.Array([]int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}))
	require.NoError(t, err)
	defer rows.Close()
	got := make(map[int64]int64)
	for rows.Next() {
		var errorID, usageID, actorID, payerID, keyID, subscriptionID, billingType int64
		var actual, subscription, balance float64
		var allocations []byte
		require.NoError(t, rows.Scan(&errorID, &usageID, &actorID, &payerID, &keyID, &subscriptionID,
			&billingType, &actual, &subscription, &balance, &allocations))
		got[errorID] = usageID
	}
	require.NoError(t, rows.Err())
	// 正常、内部旧 ID、团队付款人、部分输出、旧付款字段和恢复结算可以关联。
	// 其它用户/Key、无 ID、伪造上游 ID、团队成员及零实际扣费均不能命中。
	require.Equal(t, map[int64]int64{1: 101, 6: 102, 7: 103, 10: 105, 12: 107, 13: 108}, got)
}
