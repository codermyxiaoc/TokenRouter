package admin

import (
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/handler/dto"
	"github.com/stretchr/testify/require"
)

// 精简分组不能影响账号配额、调度诊断或完整详情，ETag 也必须基于实际返回的形状。
func TestAccountListLiteProjectionKeepsRuntimeFieldsAndDoesNotMutateDetails(t *testing.T) {
	quota := 12.5
	group := &dto.Group{ID: 8, Name: "name", Platform: "gemini", DisplayBrand: "vertex", RateMultiplier: 1.2, Status: "active", Description: "detail"}
	items := []AccountWithConcurrency{{
		Account:            &dto.Account{ID: 7, Groups: []*dto.Group{group}, GroupIDs: []int64{8}, QuotaUsed: &quota},
		CurrentConcurrency: 2,
		SchedulerScore:     &AccountSchedulerScore{BaseScore: 3},
	}}
	lite := accountListResponseItems(items, true)
	raw, err := json.Marshal(lite)
	require.NoError(t, err)
	var decoded []map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, quota, decoded[0]["quota_used"])
	require.Equal(t, float64(2), decoded[0]["current_concurrency"])
	require.Equal(t, float64(3), decoded[0]["scheduler_score"].(map[string]any)["base_score"])
	badge := decoded[0]["groups"].([]any)[0].(map[string]any)
	require.Equal(t, "vertex", badge["display_brand"])
	require.Equal(t, 1.2, badge["rate_multiplier"])
	require.NotContains(t, badge, "description")

	before := buildAccountsListETag(lite, 1, 1, 20, "", "", "", "", true)
	group.Description = "changed detail"
	after := buildAccountsListETag(accountListResponseItems(items, true), 1, 1, 20, "", "", "", "", true)
	require.Equal(t, before, after)
	full := accountListResponseItems(items, false).([]AccountWithConcurrency)
	require.Equal(t, "changed detail", full[0].Groups[0].Description)
}
