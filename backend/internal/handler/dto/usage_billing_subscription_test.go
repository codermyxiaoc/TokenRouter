package dto

import (
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLogBillingSubscriptionsDTO(t *testing.T) {
	planID := int64(101)
	log := &service.UsageLog{BillingSubscriptions: []service.BillingSubscription{
		{SubscriptionID: 11, PlanID: &planID, PlanName: "订阅套餐", AmountUSD: 0.15},
		{SubscriptionID: 12, AmountUSD: 0.05},
	}}
	for _, result := range []any{UsageLogFromService(log), UsageLogFromServiceAdmin(log)} {
		body, err := json.Marshal(result)
		require.NoError(t, err)
		var payload map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(body, &payload))
		require.JSONEq(t, `[{"subscription_id":11,"plan_id":101,"plan_name":"订阅套餐","amount_usd":0.15},{"subscription_id":12,"amount_usd":0.05}]`, string(payload["billing_subscriptions"]))
	}
}
