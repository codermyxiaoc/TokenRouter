package service

import (
	"math"

	"github.com/TokenFlux/TokenRouter/internal/domain"
)

// BillingSubscription 是实际订阅扣费的只读摘要，不参与结算分配或幂等指纹。
type BillingSubscription struct {
	SubscriptionID int64   `json:"subscription_id"`
	PlanID         *int64  `json:"plan_id,omitempty"`
	PlanName       string  `json:"plan_name,omitempty"`
	AmountUSD      float64 `json:"amount_usd"`
}

// BuildUsageBillingSubscriptions 只从使用记录的结算事实生成套餐摘要，不使用 Key 当前配置推断历史。
// @project-doc docs/domains/routing_and_billing.md#billing_subscription_display
func BuildUsageBillingSubscriptions(log *UsageLog) []BillingSubscription {
	// actual_cost 为零的失败占位或结算失败记录不代表已经扣费，即使残留预计算分配也不能展示套餐。
	if log == nil || !positiveBillingAmount(log.ActualCost) {
		return nil
	}
	type sourceKey struct{ subscriptionID, planID int64 }
	positions := make(map[sourceKey]int)
	var result []BillingSubscription
	for _, allocation := range log.BillingAllocations {
		if allocation.Type != domain.BillingAllocationTypeSubscription || allocation.SubscriptionID == nil || *allocation.SubscriptionID <= 0 || !positiveBillingAmount(allocation.AmountUSD) {
			continue
		}
		key := sourceKey{subscriptionID: *allocation.SubscriptionID}
		if allocation.PlanID != nil && *allocation.PlanID > 0 {
			key.planID = *allocation.PlanID
		}
		if index, found := positions[key]; found {
			result[index].AmountUSD += allocation.AmountUSD
			continue
		}
		item := BillingSubscription{SubscriptionID: key.subscriptionID, AmountUSD: allocation.AmountUSD}
		if key.planID > 0 {
			planID := key.planID
			item.PlanID = &planID
		}
		positions[key] = len(result)
		result = append(result, item)
	}
	// 新记录的分配清单优先；余额记录和未实际扣费的失败占位记录不能借旧 subscription_id 显示套餐。
	if len(log.BillingAllocations) > 0 || log.SubscriptionID == nil || *log.SubscriptionID <= 0 {
		return result
	}
	amount := log.SubscriptionAmountUSD
	if !positiveBillingAmount(amount) && log.BillingType == BillingTypeSubscription {
		amount = log.ActualCost
	}
	if positiveBillingAmount(amount) {
		result = append(result, BillingSubscription{SubscriptionID: *log.SubscriptionID, AmountUSD: amount})
	}
	return result
}

// positiveBillingAmount 排除零费用、负值和异常浮点值，避免把未结算记录展示为已扣费。
func positiveBillingAmount(amount float64) bool {
	return amount > 0 && !math.IsNaN(amount) && !math.IsInf(amount, 0)
}
