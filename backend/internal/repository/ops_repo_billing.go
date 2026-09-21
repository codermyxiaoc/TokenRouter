package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/lib/pq"
)

// 错误记录归属付款用户；关联仅使用内部请求标识、付款用户和 Key 三个维度。
// 不匹配裸上游请求 ID，也不读取密钥当前套餐，避免恢复换组或配置变更污染历史扣费。
const errorBillingSubscriptionsQuery = `
SELECT e.id, u.id, u.user_id, u.billing_user_id, u.api_key_id,
       u.subscription_id, u.billing_type, u.actual_cost,
       u.subscription_amount_usd, u.balance_amount_usd,
       COALESCE(u.billing_allocations, '[]'::jsonb)
FROM ops_error_logs e
JOIN usage_logs u ON u.api_key_id = e.api_key_id
  AND COALESCE(NULLIF(u.billing_user_id, 0), u.user_id) = e.user_id
  AND u.request_id = CASE
    WHEN NULLIF(BTRIM(e.client_request_id), '') IS NOT NULL THEN 'client:' || BTRIM(e.client_request_id)
    WHEN NULLIF(BTRIM(e.request_id), '') IS NOT NULL THEN 'local:' || BTRIM(e.request_id)
    ELSE NULL
  END
WHERE e.id = ANY($1)
  AND u.actual_cost > 0`

// hydrateErrorBillingSubscriptions 按当前页批量读取实际结算摘要；清理或尚未写入的用量留空。
func (r *opsRepository) hydrateErrorBillingSubscriptions(ctx context.Context, errors []*service.OpsErrorLog) error {
	byID := make(map[int64]*service.OpsErrorLog, len(errors))
	ids := make([]int64, 0, len(errors))
	for _, item := range errors {
		if item == nil {
			continue
		}
		item.BillingSubscriptions = nil
		if item.ID <= 0 || item.UserID == nil || *item.UserID <= 0 || item.APIKeyID == nil || *item.APIKeyID <= 0 {
			continue
		}
		if strings.TrimSpace(item.ClientRequestID) == "" && strings.TrimSpace(item.RequestID) == "" {
			continue
		}
		if _, exists := byID[item.ID]; !exists {
			ids = append(ids, item.ID)
		}
		byID[item.ID] = item
	}
	if len(ids) == 0 {
		return nil
	}

	rows, err := r.db.QueryContext(ctx, errorBillingSubscriptionsQuery, pq.Array(ids))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	logs := make([]*service.UsageLog, 0, len(ids))
	errorIDs := make([]int64, 0, len(ids))
	for rows.Next() {
		var errorID int64
		var subscriptionID sql.NullInt64
		var allocations []byte
		item := &service.UsageLog{}
		if err := rows.Scan(&errorID, &item.ID, &item.UserID, &item.BillingUserID, &item.APIKeyID,
			&subscriptionID, &item.BillingType, &item.ActualCost, &item.SubscriptionAmountUSD,
			&item.BalanceAmountUSD, &allocations); err != nil {
			return err
		}
		if subscriptionID.Valid {
			item.SubscriptionID = &subscriptionID.Int64
		}
		// 损坏的分配不能被解释为旧版缺省分配；保留错误查询并省略不可信的套餐摘要。
		if err := json.Unmarshal(allocations, &item.BillingAllocations); err != nil {
			continue
		}
		logs = append(logs, item)
		errorIDs = append(errorIDs, errorID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := hydrateUsageBillingSubscriptions(ctx, r.db, logs); err != nil {
		return err
	}
	for i, log := range logs {
		if item := byID[errorIDs[i]]; item != nil {
			item.BillingSubscriptions = log.BillingSubscriptions
		}
	}
	return nil
}
