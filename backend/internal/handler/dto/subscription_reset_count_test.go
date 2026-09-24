package dto

import (
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 个人与管理接口应返回相同的自动重置次数，零值也必须明确传给前端。
func TestSubscriptionResetCountsDTO(t *testing.T) {
	for _, test := range []struct {
		name                   string
		daily, weekly, monthly int64
	}{
		{name: "续费新一期从零开始"},
		{name: "日周月独立计数", daily: 12, weekly: 2, monthly: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			sub := &service.UserSubscription{
				ID: 7, DailyResetCount: test.daily, WeeklyResetCount: test.weekly, MonthlyResetCount: test.monthly,
				Notes: "管理员内部备注",
			}
			for _, result := range []any{UserSubscriptionFromService(sub), UserSubscriptionFromServiceAdmin(sub)} {
				encoded, err := json.Marshal(result)
				require.NoError(t, err)
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(encoded, &fields))
				for key, want := range map[string]int64{
					"daily_reset_count": test.daily, "weekly_reset_count": test.weekly, "monthly_reset_count": test.monthly,
				} {
					require.Contains(t, fields, key)
					var got int64
					require.NoError(t, json.Unmarshal(fields[key], &got))
					require.Equal(t, want, got)
				}
			}
			personal, err := json.Marshal(UserSubscriptionFromService(sub))
			require.NoError(t, err)
			require.NotContains(t, string(personal), "管理员内部备注")
		})
	}
}
