package dto

import (
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// TestSubscriptionPlanFromService_GroupRates 保证展示元数据正确透传、有效零倍率不被省略，未知分组不伪装成免费。
func TestSubscriptionPlanFromService_GroupRates(t *testing.T) {
	t.Parallel()
	freeRate := 0.0
	discountRate := 0.5
	plan := &service.SubscriptionPlan{
		GroupIDs: []int64{1, 2, 3},
		ApplicableGroups: []service.SubscriptionPlanGroup{
			{ID: 1, Name: "免费分组", Platform: service.PlatformOpenAI, DisplayBrand: "anthropic", RateMultiplier: &freeRate},
			{ID: 2, Name: "折扣分组", Platform: service.PlatformGemini, RateMultiplier: &discountRate},
			{ID: 3},
		},
	}

	encoded, err := json.Marshal(SubscriptionPlanFromServiceShallow(plan))
	require.NoError(t, err)
	var got struct {
		ApplicableGroups []map[string]any `json:"applicable_groups"`
	}
	require.NoError(t, json.Unmarshal(encoded, &got))
	require.Len(t, got.ApplicableGroups, 3)
	require.Equal(t, service.PlatformOpenAI, got.ApplicableGroups[0]["platform"])
	require.Equal(t, "anthropic", got.ApplicableGroups[0]["display_brand"])
	require.Equal(t, service.PlatformGemini, got.ApplicableGroups[1]["platform"])
	require.NotContains(t, got.ApplicableGroups[1], "display_brand")
	require.Contains(t, got.ApplicableGroups[0], "rate_multiplier")
	require.Equal(t, 0.0, got.ApplicableGroups[0]["rate_multiplier"])
	require.Equal(t, 0.5, got.ApplicableGroups[1]["rate_multiplier"])
	require.NotContains(t, got.ApplicableGroups[2], "rate_multiplier")
	require.Equal(t, 3.0, got.ApplicableGroups[2]["id"])
	require.Equal(t, "", got.ApplicableGroups[2]["name"])
	require.NotContains(t, got.ApplicableGroups[2], "platform")
	require.NotContains(t, got.ApplicableGroups[2], "display_brand")
}
