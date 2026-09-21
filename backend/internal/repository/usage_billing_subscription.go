package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

// usageBillingSubscriptionSource 同时绑定付款人和历史套餐 ID，防止当前订阅信息覆盖历史来源。
type usageBillingSubscriptionSource struct {
	userID, subscriptionID, planID int64
}

type usageBillingSubscriptionName struct {
	planID int64
	name   string
}

// hydrateUsageBillingSubscriptions 批量富化日志的实际订阅扣费摘要；余额请求不产生额外查询。
// 只读取匹配付款人的订阅与订单，保留软删除记录以支持历史对账；完整删除时保留原有 ID。
func hydrateUsageBillingSubscriptions(ctx context.Context, db sqlQueryer, logs []*service.UsageLog) error {
	var sources []usageBillingSubscriptionSource
	seen := make(map[usageBillingSubscriptionSource]struct{})
	for _, log := range logs {
		if log == nil {
			continue
		}
		log.BillingSubscriptions = service.BuildUsageBillingSubscriptions(log)
		for _, subscription := range log.BillingSubscriptions {
			source := usageBillingSubscriptionSourceFor(log, subscription)
			if _, exists := seen[source]; exists {
				continue
			}
			seen[source] = struct{}{}
			sources = append(sources, source)
		}
	}
	if len(sources) == 0 {
		return nil
	}
	names := make(map[usageBillingSubscriptionSource]usageBillingSubscriptionName, len(sources))
	// 大批量导出也按固定批次查询，避免每条日志查库以及 SQL 参数数量无界增长。
	const batchSize = 500
	for start := 0; start < len(sources); start += batchSize {
		end := min(start+batchSize, len(sources))
		if err := loadUsageBillingSubscriptionNames(ctx, db, sources[start:end], names); err != nil {
			return err
		}
	}
	for _, log := range logs {
		if log == nil {
			continue
		}
		for i := range log.BillingSubscriptions {
			item := &log.BillingSubscriptions[i]
			name := names[usageBillingSubscriptionSourceFor(log, *item)]
			item.PlanName = name.name
			if item.PlanID == nil && name.planID > 0 {
				planID := name.planID
				item.PlanID = &planID
			}
		}
	}
	return nil
}

func usageBillingSubscriptionSourceFor(log *service.UsageLog, subscription service.BillingSubscription) usageBillingSubscriptionSource {
	userID := log.BillingUserID
	if userID <= 0 {
		userID = log.UserID
	}
	source := usageBillingSubscriptionSource{userID: userID, subscriptionID: subscription.SubscriptionID}
	if subscription.PlanID != nil {
		source.planID = *subscription.PlanID
	}
	return source
}

// loadUsageBillingSubscriptionNames 优先使用发放订单的套餐名称快照；无快照时回退该套餐当前名称。
// 订单、订阅和结算记录的套餐 ID 必须一致，避免套餐关系发生变化后串用其它套餐名称。
func loadUsageBillingSubscriptionNames(ctx context.Context, db sqlQueryer, sources []usageBillingSubscriptionSource, names map[usageBillingSubscriptionSource]usageBillingSubscriptionName) (err error) {
	if len(sources) == 0 {
		return nil
	}
	values := make([]string, 0, len(sources))
	args := make([]any, 0, len(sources)*3)
	for _, source := range sources {
		position := len(args) + 1
		values = append(values, fmt.Sprintf("($%d::bigint, $%d::bigint, $%d::bigint)", position, position+1, position+2))
		args = append(args, source.userID, source.subscriptionID, source.planID)
	}
	query := `SELECT source.user_id, source.subscription_id, source.plan_id,
		COALESCE(NULLIF(source.plan_id, 0), subscription.plan_id),
		COALESCE(NULLIF(BTRIM(payment.plan_snapshot->>'name'), ''), plan.name, '')
		FROM (VALUES ` + strings.Join(values, ", ") + `) AS source(user_id, subscription_id, plan_id)
		LEFT JOIN user_subscriptions subscription ON subscription.id = source.subscription_id
			AND subscription.user_id = source.user_id
			AND (source.plan_id = 0 OR subscription.plan_id = source.plan_id)
		LEFT JOIN payment_orders payment ON payment.id = subscription.source_order_id
			AND payment.user_id = source.user_id
			AND payment.plan_id = COALESCE(NULLIF(source.plan_id, 0), subscription.plan_id)
		LEFT JOIN subscription_plans plan ON plan.id = COALESCE(NULLIF(source.plan_id, 0), subscription.plan_id)`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("load usage billing subscription names: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var source usageBillingSubscriptionSource
		var planID sql.NullInt64
		var name string
		if err := rows.Scan(&source.userID, &source.subscriptionID, &source.planID, &planID, &name); err != nil {
			return err
		}
		names[source] = usageBillingSubscriptionName{planID: planID.Int64, name: strings.TrimSpace(name)}
	}
	return rows.Err()
}
